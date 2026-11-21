package loadtest

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
)

func bigPlan(ctx context.Context, r *runner) ([]Invariant, SSEReport, error) {
	workers, reviewers, err := r.setupTeam(ctx, true)
	if err != nil {
		return nil, SSEReport{}, err
	}
	n := r.cfg.Tasks
	width := (n + bigPlanLayers - 1) / bigPlanLayers
	specs := make([]board.TaskInput, n)
	for i := range specs {
		specs[i] = r.specFor(r.brief, r.seq.Add(1))
		specs[i].Ref = fmt.Sprintf("t%d", i)
		if i >= width {
			specs[i].DependsOn = append(specs[i].DependsOn, fmt.Sprintf("t%d", i-width))
			if i%width != 0 {
				specs[i].DependsOn = append(specs[i].DependsOn, fmt.Sprintf("t%d", i-width-1))
			}
		}
	}
	ids, err := r.plan(ctx, r.brief, specs)
	if err != nil {
		return nil, SSEReport{}, err
	}
	r.tasks.Created = int64(len(ids))
	finished := r.drain(ctx, workers, reviewers, int64(n), func(sctx context.Context) { r.release(sctx, workers) })

	vctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	inv, err := r.allDone(vctx, n, r.brief)
	invs := []Invariant{errOr(err, inv), check("drained in time", finished, "%d tasks, %d layers", n, bigPlanLayers)}
	bad, err := r.onePerTask(vctx, "claim_task", ids)
	invs = append(invs, errOr(err, check("each task claimed once", bad == 0, "%d tasks with another claim count", bad)))
	viol, err := r.dependencyViolations(vctx, r.brief)
	return append(invs, errOr(err, check("no task claimed before its dependencies were done", viol == 0, "%d violations", viol))), SSEReport{}, nil
}

func (r *runner) release(ctx context.Context, workers []agentCreds) {
	next := 0
	for ctx.Err() == nil {
		cols, err := r.leader.Board(ctx, r.brief)
		if err == nil {
			done := map[string]bool{}
			for _, t := range cols[model.StatusDone] {
				done[t.ID] = true
			}
			var ready []string
			for _, t := range cols[model.StatusReady] {
				if !slices.ContainsFunc(t.DependsOn, func(d string) bool { return !done[d] }) {
					ready = append(ready, t.ID)
				}
			}
			_ = r.parallel(ctx, len(ready), func(i int) error {
				_, _ = r.leader.Assign(ctx, ready[i], workers[(next+i)%len(workers)].id, "")
				return nil
			})
			next += len(ready)
		}
		sleep(ctx, max(2*r.cfg.Think, 20*time.Millisecond))
	}
}

func (r *runner) dependencyViolations(ctx context.Context, brief string) (int, error) {
	ts, err := r.tasksOf(ctx, brief)
	if err != nil {
		return 0, err
	}
	claims, err := r.audit(ctx, "claim_task")
	if err != nil {
		return 0, err
	}
	dones, err := r.audit(ctx, "mark_done")
	if err != nil {
		return 0, err
	}
	viol := 0
	for _, t := range ts {
		for _, c := range claims[t.ID] {
			for _, d := range t.DependsOn {
				if !slices.ContainsFunc(dones[d], func(e *model.AuditEvent) bool { return !e.TS.After(c.TS) }) {
					viol++
				}
			}
		}
	}
	return viol, nil
}

func manyBriefs(ctx context.Context, r *runner) ([]Invariant, SSEReport, error) {
	workers, reviewers, err := r.setupTeam(ctx, false)
	if err != nil {
		return nil, SSEReport{}, err
	}
	nb, k := r.cfg.Briefs, r.cfg.Tasks
	briefs := make([]string, nb)
	ids := make([][]string, nb)
	err = r.parallel(ctx, nb, func(b int) error {
		id, err := r.createBrief(ctx, fmt.Sprintf("Load test %s brief %d", r.runID, b))
		if err != nil {
			return err
		}
		specs := make([]board.TaskInput, k)
		for i := range specs {
			specs[i] = r.specFor(id, r.seq.Add(1))
		}
		tids, err := r.plan(ctx, id, specs)
		if err != nil {
			return err
		}
		for j, tid := range tids {
			if _, err := r.leader.Assign(ctx, tid, workers[(b*k+j)%len(workers)].id, ""); err != nil {
				return fmt.Errorf("assign %s: %w", tid, err)
			}
		}
		briefs[b], ids[b] = id, tids
		return nil
	})
	if err != nil {
		return nil, SSEReport{}, err
	}
	total := nb * k
	r.tasks.Created = int64(total)
	finished := r.drain(ctx, workers, reviewers, int64(total), nil)

	vctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	inv, err := r.allDone(vctx, total, briefs...)
	invs := []Invariant{errOr(err, inv), check("drained in time", finished, "%d briefs x %d tasks", nb, k)}
	leaked := 0
	for b, id := range briefs {
		ts, err := r.tasksOf(vctx, id)
		if err != nil {
			return nil, SSEReport{}, err
		}
		got := make([]string, len(ts))
		for i, t := range ts {
			got[i] = t.ID
		}
		slices.Sort(got)
		want := slices.Clone(ids[b])
		slices.Sort(want)
		if !slices.Equal(got, want) || countDone(ts) != k {
			leaked++
		}
	}
	invs = append(invs, check("every brief holds exactly its own tasks, all done", leaked == 0, "%d of %d briefs differ", leaked, nb))
	var all []string
	for _, x := range ids {
		all = append(all, x...)
	}
	bad, err := r.onePerTask(vctx, "claim_task", all)
	return append(invs, errOr(err, check("each task claimed once", bad == 0, "%d tasks with another claim count", bad))), SSEReport{}, nil
}
