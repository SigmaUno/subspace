package loadtest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

func reconnectStorm(ctx context.Context, r *runner) ([]Invariant, SSEReport, error) {
	sse, err := r.steady(ctx)
	if err != nil {
		return nil, SSEReport{}, err
	}
	t := &r.tasks
	return []Invariant{
		check("no duplicate or out-of-order events per client", sse.Duplicates == 0, "%d duplicates, %d events at or below the hello seq", sse.Duplicates, sse.Overlap),
		check("every connection got hello and no stream failed", sse.ConnectErrors == 0 && sse.StreamErrors == 0 && sse.Connected >= int64(sse.Subscribers),
			"%d connections for %d clients, %d reconnects, %d connect errors, %d stream errors", sse.Connected, sse.Subscribers, sse.Reconnects, sse.ConnectErrors, sse.StreamErrors),
		check("clients really reconnected", sse.Reconnects > int64(sse.Subscribers), "%d reconnects", sse.Reconnects),
		check("the workload kept completing tasks", t.Done > 0 && t.Done == t.Approved && t.Approved == t.Submitted,
			"submitted %d, approved %d, done %d", t.Submitted, t.Approved, t.Done),
	}, sse, nil
}

type restartResult struct {
	doneBefore int64
	doneAfter  int
	downtime   time.Duration
	err        error
}

func restart(ctx context.Context, r *runner) ([]Invariant, SSEReport, error) {
	if r.proc == nil {
		return nil, SSEReport{}, errors.New("restart needs the in-process server")
	}
	r.retry = &retrier{}
	if err := r.newBrief(ctx); err != nil {
		return nil, SSEReport{}, err
	}
	leader, workers, reviewers, err := r.setupAgents(ctx)
	if err != nil {
		return nil, SSEReport{}, err
	}
	r.leader = r.mk(leader)
	if err := r.leader.Initialize(ctx); err != nil {
		return nil, SSEReport{}, fmt.Errorf("leader initialize: %w", err)
	}
	if err := r.setupBoard(ctx, workers); err != nil {
		return nil, SSEReport{}, err
	}
	total := atomic.LoadInt64(&r.tasks.Created)

	fctx, fcancel := context.WithCancel(ctx)
	defer fcancel()
	fleet := newSSEFleet(r.base, r.cfg.OwnerToken, r.cfg.SSESubs, 0)
	fleet.persist = true
	if r.cfg.SSESubs > 0 {
		fleet.Start(fctx, 15*time.Second)
	}

	var res restartResult
	restarted := make(chan struct{})
	finished := r.drain(ctx, workers, reviewers, total, func(sctx context.Context) {
		defer close(restarted)
		for atomic.LoadInt64(&r.tasks.Done) < max(1, total/4) {
			if sctx.Err() != nil {
				return
			}
			sleep(sctx, 10*time.Millisecond)
		}
		res.doneBefore = atomic.LoadInt64(&r.tasks.Done)
		begin := time.Now()
		r.retry.down.Store(true)
		res.err = r.proc.Restart()
		ts, err := r.tasksOf(ctx, r.brief)
		if res.err == nil {
			res.err = err
		}
		res.doneAfter = countDone(ts)
		sleep(sctx, restartGrace)
		r.retry.down.Store(false)
		res.downtime = time.Since(begin)
	})
	<-restarted
	time.Sleep(200 * time.Millisecond)
	fcancel()
	fleet.Wait()
	sse := fleet.Report()

	vctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	ts, err := r.tasksOf(vctx, r.brief)
	if err != nil {
		return nil, SSEReport{}, err
	}
	ids := make([]string, len(ts))
	for i, t := range ts {
		ids[i] = t.ID
	}
	invs := []Invariant{
		check("SSE clients reconnected after the restart", sse.Connected >= 2*int64(sse.Subscribers) && sse.Duplicates == 0,
			"%d connections for %d clients, %d duplicates", sse.Connected, sse.Subscribers, sse.Duplicates),
		check("server restarted mid-run", res.err == nil && res.doneBefore > 0 && res.doneBefore < total,
			"after %d of %d tasks done, down for %s, %d calls retried (err %v)", res.doneBefore, total, res.downtime.Round(time.Millisecond), r.retry.retries.Load(), res.err),
		check("acknowledged completions survived the restart", res.doneAfter >= int(res.doneBefore), "%d done acknowledged, %d after restart", res.doneBefore, res.doneAfter),
		check("all tasks done", countDone(ts) == int(total) && len(ts) == int(total), "%d of %d done, %d on the server", countDone(ts), total, len(ts)),
		check("drained in time", finished, "%d tasks", total),
	}
	for _, tool := range []string{"claim_task", "submit", "review", "mark_done"} {
		bad, err := r.onePerTask(vctx, tool, ids)
		invs = append(invs, errOr(err, check("no duplicate or lost "+tool+" events", bad == 0, "%d of %d tasks with another count", bad, len(ids))))
	}
	var v struct {
		OK bool `json:"ok"`
	}
	err = r.verify.Do(vctx, "audit_verify", http.MethodGet, "/api/v1/audit/verify", nil, &v)
	return append(invs, errOr(err, check("audit hash chain intact after restart", v.OK, "ok=%v", v.OK))), sse, nil
}
