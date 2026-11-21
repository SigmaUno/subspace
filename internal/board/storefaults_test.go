package board_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/spawner"
	"github.com/SigmaUno/subspace/internal/store/faultstore"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

var (
	rollback = faultstore.Faults{RollbackRate: 1}
	unknown  = faultstore.Faults{CommitRate: 1}
)

type countingSpawner struct{ n atomic.Int32 }

func (*countingSpawner) Name() string { return "count" }

func (s *countingSpawner) Spawn(context.Context, spawner.Request) error {
	s.n.Add(1)
	return nil
}

func (*countingSpawner) Stop(context.Context, string) error { return spawner.ErrNotImplemented }

func (*countingSpawner) Status(context.Context, string) (spawner.Status, error) {
	return spawner.StatusUnknown, nil
}

type faultEnv struct {
	t     *testing.T
	ctx   context.Context
	path  string
	st    *sqlite.Store
	fs    *faultstore.Store
	b     *board.Board
	cache *auth.Cache
	sp    *countingSpawner
	now   time.Time
	lead  model.Actor
	w1    model.Actor
	tok   string
	brief string
	task  string
}

func newFaultEnv(t *testing.T) *faultEnv {
	t.Helper()
	e := &faultEnv{t: t, ctx: context.Background(), path: t.TempDir() + "/faults.db", sp: &countingSpawner{},
		now:  time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC),
		lead: model.Actor{ID: "rex", Role: model.RoleLeader}, w1: model.Actor{ID: "w1", Role: model.RoleWorker}}
	e.open()
	t.Cleanup(func() { _ = e.st.Close() })
	if _, err := e.b.RegisterAgent(e.ctx, board.AgentInput{ID: "rex", Role: model.RoleLeader, Kind: model.KindOther, Provider: "anthropic"}); err != nil {
		t.Fatal(err)
	}
	r, err := e.b.RegisterAgent(e.ctx, board.AgentInput{ID: "w1", Role: model.RoleWorker, Kind: model.KindClaudeCode})
	if err != nil {
		t.Fatal(err)
	}
	e.tok = r.Connect.Token
	br, err := e.b.CreateBrief(e.ctx, board.BriefInput{Title: "B", Repos: []string{"o/r"}})
	if err != nil {
		t.Fatal(err)
	}
	e.brief = br.ID
	p, err := e.b.ProposePlan(e.ctx, e.lead, board.PlanInput{BriefID: br.ID, Tasks: []board.TaskInput{{Title: "t", Spec: "s", Repo: "o/r"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.b.ApprovePlan(e.ctx, p.ID, ""); err != nil {
		t.Fatal(err)
	}
	e.task = p.Tasks[0].ID
	if _, err := e.b.Assign(e.ctx, e.lead, e.task, "w1", ""); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *faultEnv) open() {
	e.t.Helper()
	st, err := sqlite.Open(e.path)
	if err != nil {
		e.t.Fatal(err)
	}
	e.st, e.fs = st, faultstore.New(st, 1, faultstore.Faults{})
	var cache *auth.Cache
	e.b, err = board.New(e.ctx, e.fs, board.Options{Lease: 5 * time.Minute, Now: func() time.Time { return e.now }, Spawner: e.sp,
		Invalidate: func(id string) { cache.Invalidate(id) }})
	if err != nil {
		e.t.Fatal(err)
	}
	cache = auth.NewCache(e.b.LoadByTokenHash, time.Minute)
	e.cache = cache
}

func (e *faultEnv) faulty(f faultstore.Faults, fn func() error) error {
	e.t.Helper()
	e.fs.Set(f)
	defer e.fs.Set(faultstore.Faults{})
	err := fn()
	if !errors.Is(err, faultstore.ErrInjected) {
		e.t.Fatalf("got %v, want an injected fault", err)
	}
	return err
}

func (e *faultEnv) get() *model.Task {
	e.t.Helper()
	tk, err := e.b.GetTask(e.ctx, e.lead, e.task)
	if err != nil {
		e.t.Fatal(err)
	}
	return tk
}

func (e *faultEnv) claim() {
	e.t.Helper()
	if _, err := e.b.ClaimTask(e.ctx, e.w1, e.task); err != nil {
		e.t.Fatal(err)
	}
}

func (e *faultEnv) heartbeat() time.Time {
	e.t.Helper()
	hb, err := e.b.Heartbeat(e.ctx, e.w1, e.task)
	if err != nil {
		e.t.Fatal(err)
	}
	return *hb.LeaseExpiresAt
}

func TestRolledBackLeaseEndKeepsHeartbeatLease(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(e *faultEnv) error
	}{
		{"release", func(e *faultEnv) error { _, err := e.b.ReleaseTask(e.ctx, e.w1, e.task, "x"); return err }},
		{"submit", func(e *faultEnv) error {
			_, err := e.b.Submit(e.ctx, e.w1, board.SubmitInput{TaskID: e.task, PRURL: "https://git.example/o/r/pull/1"})
			return err
		}},
		{"cancel", func(e *faultEnv) error { _, err := e.b.Cancel(e.ctx, e.task, "x"); return err }},
		{"pause_all", func(e *faultEnv) error { return e.b.PauseAll(e.ctx, "x") }},
		{"revoke", func(e *faultEnv) error { _, err := e.b.RevokeAgent(e.ctx, "w1", "x"); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newFaultEnv(t)
			e.claim()
			e.now = e.now.Add(4 * time.Minute)
			lease := e.heartbeat()
			_ = e.faulty(rollback, func() error { return tc.end(e) })
			e.now = e.now.Add(2 * time.Minute)
			if err := e.b.Sweep(e.ctx); err != nil {
				t.Fatal(err)
			}
			tk := e.get()
			if tk.Status != model.StatusInProgress || tk.LeaseExpiresAt == nil || !tk.LeaseExpiresAt.Equal(lease) {
				t.Fatalf("task %s lease %v after rolled back %s, want in_progress until %s", tk.Status, tk.LeaseExpiresAt, tc.name, lease)
			}
			if p, _ := e.b.Paused(); p {
				t.Fatal("board reports paused after a rolled back pause_all")
			}
		})
	}
}

func TestFailedFlushKeepsHeartbeatsDirty(t *testing.T) {
	e := newFaultEnv(t)
	e.claim()
	e.now = e.now.Add(4 * time.Minute)
	lease := e.heartbeat()
	e.fs.Set(faultstore.Faults{ErrorRate: 1})
	e.b.Flush(e.ctx)
	e.fs.Set(faultstore.Faults{})
	e.b.Flush(e.ctx)
	if err := e.st.Close(); err != nil {
		t.Fatal(err)
	}
	e.open()
	if tk := e.get(); tk.LeaseExpiresAt == nil || !tk.LeaseExpiresAt.Equal(lease) {
		t.Fatalf("lease after restart %v, want %s", tk.LeaseExpiresAt, lease)
	}
}

func TestUnknownRevokeOutcomeInvalidatesToken(t *testing.T) {
	e := newFaultEnv(t)
	if a, err := e.cache.LookupToken(e.tok); err != nil || a == nil || a.Status != model.AgentActive {
		t.Fatalf("lookup before revoke: %v %v", a, err)
	}
	_ = e.faulty(unknown, func() error { _, err := e.b.RevokeAgent(e.ctx, "w1", "x"); return err })
	a, err := e.cache.LookupToken(e.tok)
	if err != nil {
		t.Fatal(err)
	}
	if a != nil && a.Status != model.AgentRevoked {
		t.Fatalf("revoked agent still authenticates as %s", a.Status)
	}
}

func TestRolledBackRevokeKeepsToken(t *testing.T) {
	e := newFaultEnv(t)
	_ = e.faulty(rollback, func() error { _, err := e.b.RevokeAgent(e.ctx, "w1", "x"); return err })
	if a, err := e.cache.LookupToken(e.tok); err != nil || a == nil || a.Status != model.AgentActive {
		t.Fatalf("agent after rolled back revoke: %v %v", a, err)
	}
}

func TestUnknownPauseOutcomeMatchesDatabase(t *testing.T) {
	e := newFaultEnv(t)
	e.claim()
	_ = e.faulty(unknown, func() error { return e.b.PauseAll(e.ctx, "x") })
	if p, _ := e.b.Paused(); !p {
		t.Fatal("pause_all committed but the board reports not paused")
	}
	if hb, err := e.b.Heartbeat(e.ctx, e.w1, ""); err != nil || !hb.Paused {
		t.Fatalf("heartbeat after committed pause: %+v %v", hb, err)
	}
	_ = e.faulty(unknown, func() error { return e.b.ResumeAll(e.ctx) })
	if p, _ := e.b.Paused(); p {
		t.Fatal("resume_all committed but the board reports paused")
	}
	e.claim()
}

func TestUnknownCapacityOutcomeMatchesDatabase(t *testing.T) {
	e := newFaultEnv(t)
	cfg := capacity.Config{GlobalMaxAgents: 7, Limits: []capacity.Limit{}}
	_ = e.faulty(unknown, func() error { _, err := e.b.SetCapacity(e.ctx, cfg); return err })
	v, err := e.b.Capacity(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v.GlobalMaxAgents != 7 {
		t.Fatalf("capacity view %d after committed set_capacity, want 7", v.GlobalMaxAgents)
	}
}

func (e *faultEnv) live() (live, revoked int) {
	e.t.Helper()
	ags, err := e.b.ListAgents(e.ctx, "", "")
	if err != nil {
		e.t.Fatal(err)
	}
	for _, a := range ags {
		if !a.Spawned {
			continue
		}
		if a.Status == model.AgentRevoked {
			revoked++
		} else {
			live++
		}
	}
	return live, revoked
}

func TestSpawnRetryAfterUnknownOutcome(t *testing.T) {
	e := newFaultEnv(t)
	in := board.SpawnInput{BriefID: e.brief, Kind: model.KindCodex, Model: "m", N: 1}
	_ = e.faulty(unknown, func() error { _, err := e.b.RequestSpawn(e.ctx, e.lead, in); return err })
	if n := e.sp.n.Load(); n != 0 {
		t.Fatalf("spawner called %d times for an unconfirmed spawn", n)
	}
	if live, revoked := e.live(); live != 0 || revoked != 1 {
		t.Fatalf("after unknown outcome: %d live, %d revoked spawned agents", live, revoked)
	}
	sps, err := e.b.RequestSpawn(e.ctx, e.lead, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(sps) != 1 || sps[0].Status != "spawned" || e.sp.n.Load() != 1 {
		t.Fatalf("retry: %d spawns, spawner called %d times", len(sps), e.sp.n.Load())
	}
	if live, _ := e.live(); live != 1 {
		t.Fatalf("%d live spawned agents after retry, want 1", live)
	}
	all, err := e.b.ListSpawns(e.ctx, e.brief)
	if err != nil {
		t.Fatal(err)
	}
	for _, sp := range all {
		if sp.Status == "requested" {
			t.Fatalf("spawn %s left requested", sp.ID)
		}
	}
}

func TestSpawnRespectsCapacityGateAfterSetCapacity(t *testing.T) {
	e := newFaultEnv(t)
	e.b.SetCapacityGate(func(string, string, int) error { return model.Errf("capacity", "breaker open") })
	if _, err := e.b.SetCapacity(e.ctx, capacity.Config{GlobalMaxAgents: 10}); err != nil {
		t.Fatal(err)
	}
	_, err := e.b.RequestSpawn(e.ctx, e.lead, board.SpawnInput{BriefID: e.brief, Kind: model.KindCodex, Model: "m", N: 1})
	if model.AsError(err).Code != "capacity" {
		t.Fatalf("spawn with a closed gate: %v", err)
	}
}

func TestInjectedReadFailsClosed(t *testing.T) {
	e := newFaultEnv(t)
	e.claim()
	e.now = e.now.Add(6 * time.Minute)
	e.fs.Set(faultstore.Faults{FailAt: []int64{e.fs.Ops() + 1}})
	err := e.b.Sweep(e.ctx)
	e.fs.Set(faultstore.Faults{})
	if !errors.Is(err, faultstore.ErrInjected) {
		t.Fatalf("sweep with a failing read reported %v", err)
	}
	if err := e.b.Sweep(e.ctx); err != nil {
		t.Fatal(err)
	}
	if tk := e.get(); tk.Status != model.StatusAssigned {
		t.Fatalf("expired task is %s after sweep", tk.Status)
	}
}
