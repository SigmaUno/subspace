package board_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

type poisonEnv struct {
	t     *testing.T
	ctx   context.Context
	b     *board.Board
	now   time.Time
	lead  model.Actor
	tasks []string
}

func newPoisonEnv(t *testing.T, opts board.Options, tasks int) *poisonEnv {
	t.Helper()
	st, err := sqlite.Open(t.TempDir() + "/poison.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &poisonEnv{t: t, ctx: context.Background(), now: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC), lead: model.Actor{ID: "rex", Role: model.RoleLeader}}
	opts.Now = func() time.Time { return e.now }
	if opts.Lease == 0 {
		opts.Lease = 5 * time.Minute
	}
	if e.b, err = board.New(e.ctx, st, opts); err != nil {
		t.Fatal(err)
	}
	e.must(e.b.RegisterAgent(e.ctx, board.AgentInput{ID: "w1", Role: model.RoleWorker, Kind: model.KindClaudeCode}))
	br, err := e.b.CreateBrief(e.ctx, board.BriefInput{Title: "B"})
	if err != nil {
		t.Fatal(err)
	}
	var in []board.TaskInput
	for i := 0; i < tasks; i++ {
		in = append(in, board.TaskInput{Title: fmt.Sprintf("t%d", i), Spec: "s", Repo: "o/r"})
	}
	p, err := e.b.ProposePlan(e.ctx, e.lead, board.PlanInput{BriefID: br.ID, Tasks: in})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.ApprovePlan(e.ctx, p.ID, ""); err != nil {
		t.Fatal(err)
	}
	for _, tk := range p.Tasks {
		e.tasks = append(e.tasks, tk.ID)
		if _, err := e.b.Assign(e.ctx, e.lead, tk.ID, "w1", ""); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e *poisonEnv) must(_ any, err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *poisonEnv) claimAndExpire(id string, wait time.Duration) {
	e.t.Helper()
	e.must(e.b.ClaimTask(e.ctx, model.Actor{ID: "w1", Role: model.RoleWorker}, id))
	e.now = e.now.Add(wait)
	if err := e.b.Sweep(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}

func (e *poisonEnv) task(id string) *model.Task {
	e.t.Helper()
	tk, err := e.b.GetTask(e.ctx, e.lead, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return tk
}

func (e *poisonEnv) thread(thread string) string {
	e.t.Helper()
	ms, err := e.b.ReadThread(e.ctx, e.lead, thread, 0, 100)
	if err != nil {
		e.t.Fatal(err)
	}
	var sb strings.Builder
	for _, m := range ms {
		if m.Kind == model.MsgSystem {
			sb.WriteString(m.Body + "\n")
		}
	}
	return sb.String()
}

func TestRepeatedLeaseExpiryBlocksPoisonTask(t *testing.T) {
	for _, tc := range []struct {
		name      string
		threshold int
		expiries  int
	}{
		{"default", 0, board.DefaultPoisonExpiries},
		{"configured", 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newPoisonEnv(t, board.Options{PoisonExpiries: tc.threshold, FlapExpiries: 100}, 1)
			id := e.tasks[0]
			for i := 1; i < tc.expiries; i++ {
				e.claimAndExpire(id, 6*time.Minute)
				if tk := e.task(id); tk.Status != model.StatusAssigned || tk.LeaseExpiries != i {
					t.Fatalf("expiry %d: status %s expiries %d", i, tk.Status, tk.LeaseExpiries)
				}
			}
			e.claimAndExpire(id, 6*time.Minute)
			tk := e.task(id)
			if tk.Status != model.StatusBlocked || tk.LeaseExpiries != tc.expiries {
				t.Fatalf("status %s expiries %d", tk.Status, tk.LeaseExpiries)
			}
			if tk.BlockedReason == nil || !strings.HasPrefix(*tk.BlockedReason, "poison_task") {
				t.Fatalf("blocked reason %v", tk.BlockedReason)
			}
			if tk.LeaseExpiresAt != nil || tk.ClaimedAt != nil {
				t.Fatal("blocked task still holds a lease")
			}
			if !strings.Contains(e.thread("task:"+id), "poison task") {
				t.Fatal("no system message in the task thread")
			}
			if _, err := e.b.ClaimTask(e.ctx, model.Actor{ID: "w1", Role: model.RoleWorker}, id); err == nil {
				t.Fatal("blocked poison task was claimable")
			}

			if _, err := e.b.Reopen(e.ctx, id, model.StatusAssigned, "looked at it"); err != nil {
				t.Fatal(err)
			}
			if tk := e.task(id); tk.LeaseExpiries != 0 || tk.BlockedReason != nil {
				t.Fatalf("reopen kept state: expiries %d reason %v", tk.LeaseExpiries, tk.BlockedReason)
			}
			e.claimAndExpire(id, 6*time.Minute)
			if tk := e.task(id); tk.Status != model.StatusAssigned {
				t.Fatalf("after reopen first expiry: %s", tk.Status)
			}
		})
	}
}

func TestFlappingAgentIsPausedWithinWindowOnly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		gap    time.Duration
		paused bool
	}{
		{"inside window", 6 * time.Minute, true},
		{"spread beyond window", 20 * time.Minute, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newPoisonEnv(t, board.Options{}, 3)
			for i, id := range e.tasks {
				e.claimAndExpire(id, tc.gap)
				a, err := e.b.GetAgent(e.ctx, "w1")
				if err != nil {
					t.Fatal(err)
				}
				want := tc.paused && i == len(e.tasks)-1
				if (a.Status == model.AgentPaused) != want {
					t.Fatalf("after expiry %d: agent status %s", i+1, a.Status)
				}
				if want && (a.PauseReason == nil || *a.PauseReason != "flapping") {
					t.Fatalf("pause reason %v", a.PauseReason)
				}
			}
			if tc.paused {
				if !strings.Contains(e.thread("agent:w1"), "flapping") {
					t.Fatal("no system message in the agent thread")
				}
				if _, err := e.b.ResumeAgent(e.ctx, "w1"); err != nil {
					t.Fatal(err)
				}
				a, _ := e.b.GetAgent(e.ctx, "w1")
				if a.Status != model.AgentActive {
					t.Fatalf("owner resume left agent %s", a.Status)
				}
			}
		})
	}
}
