package acceptance

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/mergequeue"
	"github.com/SigmaUno/subspace/internal/model"
)

type ownerScript struct {
	owner    *client.Owner
	env      *agentEnv
	sink     *sink
	answered map[string]bool
	enqueued map[string]time.Time
	flipped  map[int]bool
	merged   map[string]bool
	probe    string
	planID   string
}

func newOwnerScript(owner *client.Owner, env *agentEnv, s *sink) *ownerScript {
	return &ownerScript{owner: owner, env: env, sink: s, answered: map[string]bool{}, enqueued: map[string]time.Time{},
		flipped: map[int]bool{}, merged: map[string]bool{}}
}

func (o *ownerScript) run(ctx context.Context) error {
	if err := o.env.bus.until(ctx, func() bool { return o.sink.first("plan") != "" }); err != nil {
		return fmt.Errorf("waiting for the plan notification: %w", err)
	}
	o.planID = o.sink.first("plan")
	if _, err := o.owner.ApprovePlan(ctx, o.planID, "approved by the acceptance owner"); err != nil {
		return fmt.Errorf("approve plan: %w", err)
	}
	for {
		ch := o.env.bus.wait()
		v := o.env.world.view()
		if v.allDone() {
			return nil
		}
		if err := o.step(ctx, v); err != nil {
			return err
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return nil
		}
	}
}

func (o *ownerScript) step(ctx context.Context, v *view) error {
	qs := make([]*model.Question, 0, len(v.questions))
	for _, q := range v.questions {
		qs = append(qs, q)
	}
	sort.Slice(qs, func(i, j int) bool { return qs[i].ID < qs[j].ID })
	for _, q := range qs {
		if q.Status == "open" && !o.answered[q.ID] && o.sink.has(q.ID, "question") {
			o.answered[q.ID] = true
			if _, err := o.owner.AnswerQuestion(ctx, q.ID, o.env.sc.answer); err != nil {
				return fmt.Errorf("answer %s: %w", q.ID, err)
			}
		}
	}
	ids := make([]string, 0, len(v.tasks))
	for id := range v.tasks {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if err := o.task(ctx, v.tasks[id]); err != nil {
			return err
		}
	}
	for _, c := range v.candidates {
		if c.State == mergequeue.StateMerged && !o.flipped[c.PR] {
			o.flipped[c.PR] = true
			o.merged[c.TaskID] = true
			o.env.forge.merge(c.PR)
		}
	}
	return nil
}

func (o *ownerScript) task(ctx context.Context, t *model.Task) error {
	switch {
	case t.Title == o.env.sc.defectTitle && t.Status == model.StatusChangesRequested && o.probe == "":
		_, err := o.enqueue(ctx, t.ID)
		if !client.IsCode(err, client.CodeConflict) {
			return fmt.Errorf("enqueueing %s while it needs changes: %v", t.ID, err)
		}
		o.probe = client.CodeOf(err)
	case t.Status == model.StatusApproved && o.sink.has(t.ID, "approved"):
		if _, done := o.enqueued[t.ID]; done {
			return nil
		}
		o.enqueued[t.ID] = time.Now()
		if t.Role == model.RoleTester {
			return o.mergeByHand(ctx, t)
		}
		if _, err := o.enqueue(ctx, t.ID); err != nil {
			return fmt.Errorf("enqueue %s: %w", t.ID, err)
		}
	}
	return nil
}

func (o *ownerScript) enqueue(ctx context.Context, taskID string) (*mergequeue.Candidate, error) {
	var c mergequeue.Candidate
	err := o.owner.Do(ctx, "enqueue", http.MethodPost, "/api/v1/mergequeue/candidates", map[string]string{"task_id": taskID}, &c)
	return &c, err
}

func (o *ownerScript) mergeByHand(ctx context.Context, t *model.Task) error {
	if t.PRURL == nil {
		return fmt.Errorf("%s has no pull request", t.ID)
	}
	n := o.env.forge.prNumber(*t.PRURL)
	o.env.forge.merge(n)
	if _, err := o.owner.MarkDone(ctx, t.ID, "merged by the owner in "+strings.TrimPrefix(*t.PRURL, o.env.forge.url())); err != nil {
		return fmt.Errorf("mark %s done: %w", t.ID, err)
	}
	return nil
}
