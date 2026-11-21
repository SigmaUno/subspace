package acceptance

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/mergequeue"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/prsync"
)

type view struct {
	tasks      map[string]*model.Task
	questions  map[string]*model.Question
	candidates []mergequeue.Candidate
	prs        map[string]prsync.Status
}

func (v *view) allDone() bool {
	if len(v.tasks) == 0 {
		return false
	}
	for _, t := range v.tasks {
		if t.Status != model.StatusDone {
			return false
		}
	}
	return true
}

func (v *view) depsDone(t *model.Task) bool {
	for _, d := range t.DependsOn {
		if dep := v.tasks[d]; dep == nil || dep.Status != model.StatusDone {
			return false
		}
	}
	return true
}

func (v *view) ready(agent string) []*model.Task {
	var out []*model.Task
	for _, t := range v.tasks {
		if t.Assignee != nil && *t.Assignee == agent && v.depsDone(t) &&
			(t.Status == model.StatusAssigned || t.Status == model.StatusChangesRequested) {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(a, b *model.Task) int { return strings.Compare(a.ID, b.ID) })
	return out
}

type world struct {
	owner *client.Owner
	bus   *bus
	cur   atomic.Pointer[view]
}

func newWorld(owner *client.Owner, b *bus) *world {
	w := &world{owner: owner, bus: b}
	w.cur.Store(&view{})
	return w
}

func (w *world) view() *view { return w.cur.Load() }

func (w *world) refresh(ctx context.Context) error {
	tasks, err := w.owner.ListTasks(ctx, board.TaskQuery{Limit: 1000})
	if err != nil {
		return err
	}
	questions, err := w.owner.ListQuestions(ctx, "")
	if err != nil {
		return err
	}
	var cands struct {
		Candidates []mergequeue.Candidate `json:"candidates"`
	}
	if err := w.owner.Do(ctx, "candidates", "GET", "/api/v1/mergequeue/candidates", nil, &cands); err != nil {
		return err
	}
	var prs struct {
		PRs []prsync.Status `json:"prs"`
	}
	if err := w.owner.Do(ctx, "prs", "GET", "/api/v1/prs?limit=1000", nil, &prs); err != nil {
		return err
	}
	v := &view{tasks: map[string]*model.Task{}, questions: map[string]*model.Question{}, candidates: cands.Candidates, prs: map[string]prsync.Status{}}
	for _, t := range tasks {
		v.tasks[t.ID] = t
	}
	for _, q := range questions {
		v.questions[q.ID] = q
	}
	for _, p := range prs.PRs {
		v.prs[p.TaskID] = p
	}
	w.cur.Store(v)
	w.bus.fire()
	return nil
}

func (w *world) run(ctx context.Context) error {
	kick := make(chan struct{}, 1)
	watch := &sseWatch{owner: w.owner, topics: []string{"board", "questions", "plans"}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		watch.run(ctx, func() {
			select {
			case kick <- struct{}{}:
			default:
			}
		})
	}()
	defer func() { <-done }()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		if err := w.refresh(ctx); err != nil && ctx.Err() == nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-kick:
		case <-tick.C:
		}
	}
}
