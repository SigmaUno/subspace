package board

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

var allowedTransitions = map[string][]string{
	model.StatusBacklog:          {model.StatusReady, model.StatusCancelled},
	model.StatusReady:            {model.StatusAssigned, model.StatusBlocked, model.StatusCancelled},
	model.StatusAssigned:         {model.StatusInProgress, model.StatusBlocked, model.StatusCancelled},
	model.StatusInProgress:       {model.StatusAssigned, model.StatusBlocked, model.StatusSubmitted, model.StatusApproved, model.StatusCancelled},
	model.StatusSubmitted:        {model.StatusInReview, model.StatusApproved, model.StatusChangesRequested, model.StatusNeedsOwner, model.StatusCancelled},
	model.StatusInReview:         {model.StatusApproved, model.StatusChangesRequested, model.StatusNeedsOwner, model.StatusCancelled},
	model.StatusChangesRequested: {model.StatusInProgress, model.StatusCancelled},
	model.StatusApproved:         {model.StatusDone, model.StatusCancelled},
	model.StatusBlocked:          {model.StatusReady, model.StatusAssigned, model.StatusCancelled},
	model.StatusNeedsOwner:       {model.StatusDone, model.StatusReady, model.StatusAssigned, model.StatusCancelled},
	model.StatusCancelled:        {model.StatusReady, model.StatusAssigned},
	model.StatusDone:             nil,
}

type fuzzAgent struct{ id, role, kind string }

var fuzzAgents = []fuzzAgent{
	{"lead", model.RoleLeader, model.KindOther},
	{"w1", model.RoleWorker, model.KindClaudeCode},
	{"w2", model.RoleWorker, model.KindCodex},
	{"t1", model.RoleTester, model.KindOpenCode},
	{"rv1", model.RoleReviewer, model.KindCodex},
	{"rv2", model.RoleReviewer, model.KindClaudeCode},
}

const (
	fuzzOps      = 20
	fuzzMaxSteps = 128
	fuzzRepo     = "o/r"
	fuzzPRURL    = "https://example.test/pr/1"
)

type fuzzEnv struct {
	t      *testing.T
	ctx    context.Context
	b      *Board
	st     store.Store
	now    time.Time
	brief  string
	plan   string
	lead   model.Actor
	actors []model.Actor
	byID   map[string]model.Actor
}

func newFuzzEnv(t *testing.T) *fuzzEnv {
	t.Helper()
	st, err := sqlite.Open(t.TempDir() + "/fuzz.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &fuzzEnv{t: t, ctx: context.Background(), st: st, now: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC), byID: map[string]model.Actor{}}
	b, err := New(e.ctx, st, Options{Now: func() time.Time { return e.now }, Lease: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	e.b = b
	for _, a := range fuzzAgents {
		if _, err := b.RegisterAgent(e.ctx, AgentInput{ID: a.id, Role: a.role, Kind: a.kind}); err != nil {
			t.Fatal(err)
		}
		act := model.Actor{ID: a.id, Role: a.role}
		e.actors = append(e.actors, act)
		e.byID[a.id] = act
	}
	e.lead = e.actors[0]
	br, err := b.CreateBrief(e.ctx, BriefInput{Title: "fuzz", Repos: []string{fuzzRepo}, MaxCostEUR: 10, MaxParallel: 2})
	if err != nil {
		t.Fatal(err)
	}
	e.brief = br.ID
	p, err := b.ProposePlan(e.ctx, e.lead, fuzzPlan(br.ID))
	if err != nil {
		t.Fatal(err)
	}
	e.plan = p.ID
	return e
}

func fuzzPlan(briefID string) PlanInput {
	return PlanInput{BriefID: briefID, Tasks: []TaskInput{
		{Ref: "a", Title: "build", Spec: "build it", Repo: fuzzRepo, AcceptanceCriteria: []string{"builds"}},
		{Ref: "b", Title: "extend", Spec: "extend it", Repo: fuzzRepo, DependsOn: []string{"a"}},
		{Title: "test", Spec: "test it", Repo: fuzzRepo, Role: model.RoleTester},
	}}
}

func (e *fuzzEnv) tasks() map[string]*model.Task {
	out := map[string]*model.Task{}
	err := e.st.View(e.ctx, func(tx store.Tx) error {
		ts, err := tx.ListTasks(store.TaskFilter{BriefID: e.brief, Limit: 100000})
		for _, t := range ts {
			out[t.ID] = t
		}
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *fuzzEnv) actorFor(t *model.Task, arg byte) model.Actor {
	if t != nil && t.Assignee != nil && arg&0x80 == 0 {
		if a, ok := e.byID[*t.Assignee]; ok {
			return a
		}
	}
	return e.actors[int(arg>>4)%len(e.actors)]
}

func (e *fuzzEnv) step(op, arg byte) {
	e.t.Helper()
	before := e.tasks()
	ids := sortedIDs(before)
	var t *model.Task
	if len(ids) > 0 {
		t = before[ids[int(arg)%len(ids)]]
	}
	var actor model.Actor
	if t != nil {
		actor = e.actorFor(t, arg)
	} else {
		actor = e.actors[int(arg)%len(e.actors)]
	}
	agent := e.actors[int(arg>>3)%len(e.actors)]
	ctx := e.ctx
	var err error

	switch op {
	case 0:
		_, err = e.b.Assign(ctx, e.lead, t.ID, agent.ID, "assignee")
	case 1:
		_, err = e.b.Assign(ctx, e.lead, t.ID, agent.ID, "reviewer")
	case 2:
		_, err = e.b.ClaimTask(ctx, actor, t.ID)
		if err == nil {
			for _, d := range t.DependsOn {
				if dt := before[d]; dt == nil || dt.Status != model.StatusDone {
					e.t.Fatalf("claimed %s with dependency %s in state %v", t.ID, d, dt)
				}
			}
		}
	case 3:
		_, err = e.b.ReleaseTask(ctx, actor, t.ID, "fuzz")
	case 4:
		_, err = e.b.Submit(ctx, actor, SubmitInput{TaskID: t.ID, PRURL: fuzzPRURL, Summary: "done"})
	case 5:
		verdict := "approve"
		if arg&1 == 1 {
			verdict = "changes_requested"
		}
		_, err = e.b.Review(ctx, actor, t.ID, verdict, "fix it")
	case 6:
		to := model.StatusReady
		if arg&1 == 1 {
			to = model.StatusAssigned
		}
		_, err = e.b.Reopen(ctx, t.ID, to, "")
	case 7:
		_, err = e.b.Cancel(ctx, t.ID, "")
	case 8:
		status := model.StatusInProgress
		if arg&1 == 1 {
			status = model.StatusBlocked
		}
		cost := float64(arg % 8)
		_, err = e.b.UpdateStatus(ctx, actor, StatusInput{TaskID: t.ID, Status: status, CostEUR: &cost})
	case 9:
		_, err = e.b.SetTaskBudget(ctx, t.ID, model.Budget{MaxMinutes: int(arg%5) * 10, MaxCostEUR: float64(arg % 4)})
	case 10:
		err = e.b.PauseAll(ctx, "fuzz")
	case 11:
		err = e.b.ResumeAll(ctx)
	case 12:
		_, err = e.b.ApprovePlan(ctx, e.plan, "")
	case 13:
		if arg&1 == 1 {
			_, err = e.b.RejectPlan(ctx, e.plan, "no")
		} else {
			var p *model.Plan
			p, err = e.b.ProposePlan(ctx, e.lead, fuzzPlan(e.brief))
			if err == nil {
				e.plan = p.ID
			}
		}
	case 14:
		_, err = e.b.MarkDone(ctx, t.ID, "")
	case 15:
		s := []string{model.StatusBlocked, model.StatusReady, model.StatusCancelled}[int(arg)%3]
		_, err = e.b.UpdateTask(ctx, e.lead, t.ID, TaskPatch{Status: &s})
	case 16:
		_, err = e.b.Reassign(ctx, e.lead, t.ID, agent.ID, "")
	case 17:
		e.now = e.now.Add(time.Duration(arg) * time.Minute / 2)
		e.b.Flush(ctx)
		err = e.b.Sweep(ctx)
	case 18:
		_, err = e.b.Heartbeat(ctx, actor, "")
	case 19:
		e.b.Flush(ctx)
	}
	if err != nil {
		var me *model.Error
		if !errors.As(err, &me) {
			e.t.Fatalf("op %d: unexpected error type %T: %v", op, err, err)
		}
	}
	e.check(before)
}

func (e *fuzzEnv) check(before map[string]*model.Task) {
	e.t.Helper()
	after := e.tasks()
	for id, prev := range before {
		cur, ok := after[id]
		if !ok {
			e.t.Fatalf("task %s disappeared", id)
		}
		if cur.Status == prev.Status {
			continue
		}
		if !contains(allowedTransitions[prev.Status], cur.Status) {
			e.t.Fatalf("task %s: illegal transition %s -> %s", id, prev.Status, cur.Status)
		}
	}
	paused, _ := e.b.Paused()
	for id, t := range after {
		if _, seen := before[id]; !seen && t.Status != model.StatusBacklog {
			e.t.Fatalf("new task %s starts as %s", id, t.Status)
		}
		e.checkFields(id, t)
		if paused && t.Status == model.StatusInProgress {
			e.t.Fatalf("task %s in_progress while paused", id)
		}
	}
}

func (e *fuzzEnv) checkFields(id string, t *model.Task) {
	e.t.Helper()
	if !contains(model.AllStatuses, t.Status) {
		e.t.Fatalf("task %s: unknown status %q", id, t.Status)
	}
	leased := t.ClaimedAt != nil || t.LeaseExpiresAt != nil
	if t.Status == model.StatusInProgress {
		if t.Assignee == nil || t.ClaimedAt == nil || t.LeaseExpiresAt == nil {
			e.t.Fatalf("task %s in_progress without assignee or lease", id)
		}
	} else if leased {
		e.t.Fatalf("task %s is %s but holds a lease", id, t.Status)
	}
	switch t.Status {
	case model.StatusAssigned, model.StatusInProgress, model.StatusSubmitted, model.StatusInReview,
		model.StatusChangesRequested, model.StatusApproved, model.StatusDone:
		if t.Assignee == nil {
			e.t.Fatalf("task %s is %s without assignee", id, t.Status)
		}
	case model.StatusBlocked, model.StatusNeedsOwner:
		if t.BlockedReason == nil {
			e.t.Fatalf("task %s is %s without blocked_reason", id, t.Status)
		}
	}
	if t.ReviewRounds >= MaxReviewRounds && t.Status != model.StatusNeedsOwner {
		e.t.Fatalf("task %s has %d review rounds but is %s", id, t.ReviewRounds, t.Status)
	}
}

func sortedIDs(ts map[string]*model.Task) []string {
	ids := make([]string, 0, len(ts))
	for id := range ts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func FuzzTaskTransitions(f *testing.F) {
	f.Add([]byte{12, 0, 0, 0, 2, 0, 4, 0, 5, 0, 14, 0})
	f.Add([]byte{12, 0, 0, 0, 0, 16, 2, 0, 8, 1, 17, 255, 3, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		e := newFuzzEnv(t)
		for i := 0; i+1 < len(data) && i/2 < fuzzMaxSteps; i += 2 {
			e.step(data[i]%fuzzOps, data[i+1])
		}
	})
}
