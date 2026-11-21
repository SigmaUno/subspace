package lifecycle_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/lifecycle"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/spawner"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fakeSpawner struct {
	mu      sync.Mutex
	stopped []string
	status  map[string]spawner.Status
	stopErr error
}

func (*fakeSpawner) Name() string { return "fake" }

func (*fakeSpawner) Spawn(context.Context, spawner.Request) error { return nil }

func (f *fakeSpawner) Stop(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopErr != nil {
		return f.stopErr
	}
	f.stopped = append(f.stopped, id)
	return nil
}

func (f *fakeSpawner) Status(_ context.Context, id string) (spawner.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.status[id]; ok {
		return s, nil
	}
	return spawner.StatusUnknown, nil
}

func (f *fakeSpawner) stops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.stopped)
}

type records struct{ s *sqlite.Store }

func (r records) Records(ctx context.Context) (map[string]lifecycle.Record, error) {
	m, err := r.s.SpawnLifecycles(ctx)
	out := make(map[string]lifecycle.Record, len(m))
	for k, v := range m {
		out[k] = lifecycle.Record(v)
	}
	return out, err
}

func (r records) Save(ctx context.Context, rec lifecycle.Record, sp *model.Spawn, ev *model.AuditEvent) error {
	return r.s.SaveSpawnLifecycle(ctx, sqlite.SpawnLifecycle(rec), sp, ev)
}

type sim struct {
	t   *testing.T
	ctx context.Context
	db  string
	clk *clock
	sp  *fakeSpawner
	st  *sqlite.Store
	b   *board.Board
	r   *lifecycle.Reaper
	cfg lifecycle.Config
}

func newSim(t *testing.T, cfg lifecycle.Config) *sim {
	t.Helper()
	s := &sim{t: t, ctx: context.Background(), db: filepath.Join(t.TempDir(), "t.db"),
		clk: &clock{t: time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)}, sp: &fakeSpawner{}, cfg: cfg}
	s.open()
	return s
}

func (s *sim) open() {
	s.t.Helper()
	st, err := sqlite.Open(s.db)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { _ = st.Close() })
	b, err := board.New(s.ctx, st, board.Options{PublicURL: "http://sub.test", Lease: 15 * time.Minute, Spawner: s.sp, Now: s.clk.Now})
	if err != nil {
		s.t.Fatal(err)
	}
	s.st, s.b = st, b
	s.r = lifecycle.New(lifecycle.Options{Config: s.cfg, Board: b, Store: records{st}, Spawner: s.sp, Now: s.clk.Now})
}

func (s *sim) restart() {
	s.t.Helper()
	s.b.Flush(s.ctx)
	if err := s.st.Close(); err != nil {
		s.t.Fatal(err)
	}
	s.open()
}

func (s *sim) brief() string {
	s.t.Helper()
	br, err := s.b.CreateBrief(s.ctx, board.BriefInput{Title: "B", Repos: []string{"o/r"}})
	if err != nil {
		s.t.Fatal(err)
	}
	return br.ID
}

func (s *sim) spawn(briefID string, n int) []*model.Spawn {
	s.t.Helper()
	sps, err := s.b.RequestSpawn(s.ctx, model.Owner, board.SpawnInput{BriefID: briefID, Kind: model.KindClaudeCode, Model: "m", N: n})
	if err != nil {
		s.t.Fatal(err)
	}
	return sps
}

func (s *sim) tick() {
	s.t.Helper()
	if err := s.r.Tick(s.ctx); err != nil {
		s.t.Fatal(err)
	}
}

func (s *sim) get(id string) lifecycle.Spawn {
	s.t.Helper()
	sps, _, err := s.r.List(s.ctx, "", "", 0)
	if err != nil {
		s.t.Fatal(err)
	}
	for _, sp := range sps {
		if sp.ID == id {
			return sp
		}
	}
	s.t.Fatalf("spawn %s not listed", id)
	return lifecycle.Spawn{}
}

func (s *sim) want(id, status, lc, reason string) lifecycle.Spawn {
	s.t.Helper()
	sp := s.get(id)
	got := ""
	if sp.StopReason != nil {
		got = *sp.StopReason
	}
	if sp.Status != status || sp.Lifecycle != lc || got != reason {
		s.t.Fatalf("%s: status=%s lifecycle=%s reason=%q, want %s/%s/%q", id, sp.Status, sp.Lifecycle, got, status, lc, reason)
	}
	return sp
}

func (s *sim) agentStatus(id string) string {
	s.t.Helper()
	a, err := s.b.GetAgent(s.ctx, id)
	if err != nil {
		s.t.Fatal(err)
	}
	return a.Status
}

func (s *sim) assignTask(briefID, agentID string) string {
	s.t.Helper()
	if _, err := s.b.GetAgent(s.ctx, "rex"); err != nil {
		if _, err := s.b.RegisterAgent(s.ctx, board.AgentInput{ID: "rex", Role: model.RoleLeader, Kind: model.KindOther, Model: "m"}); err != nil {
			s.t.Fatal(err)
		}
	}
	lead := model.Actor{ID: "rex", Role: model.RoleLeader}
	p, err := s.b.ProposePlan(s.ctx, lead, board.PlanInput{BriefID: briefID, Tasks: []board.TaskInput{{Title: "t", Spec: "s", Repo: "o/r"}}})
	if err != nil {
		s.t.Fatal(err)
	}
	if _, err := s.b.ApprovePlan(s.ctx, p.ID, ""); err != nil {
		s.t.Fatal(err)
	}
	tid := p.Tasks[0].ID
	if _, err := s.b.Assign(s.ctx, lead, tid, agentID, ""); err != nil {
		s.t.Fatal(err)
	}
	return tid
}

func TestConnectTimeoutFailsAndRevokes(t *testing.T) {
	s := newSim(t, lifecycle.Config{ConnectTimeout: 5 * time.Minute, IdleTTL: time.Hour})
	sps := s.spawn(s.brief(), 2)
	late, quiet := sps[0], sps[1]

	s.clk.advance(time.Minute)
	s.b.Touch(late.AgentID)
	s.clk.advance(3*time.Minute + 59*time.Second)
	s.tick()
	s.want(quiet.ID, "spawned", "connecting", "")
	s.want(late.ID, "spawned", "idle", "")

	s.clk.advance(time.Second)
	s.tick()
	sp := s.want(quiet.ID, "failed", "failed", lifecycle.ReasonConnectTimeout)
	if sp.Error == nil || sp.StoppedAt == nil || !sp.StoppedAt.Equal(s.clk.Now()) {
		t.Fatalf("failed spawn lacks error/stopped_at: %+v", sp)
	}
	if s.agentStatus(quiet.AgentID) != model.AgentRevoked || s.agentStatus(late.AgentID) != model.AgentActive {
		t.Fatalf("agents: quiet=%s late=%s", s.agentStatus(quiet.AgentID), s.agentStatus(late.AgentID))
	}
	if got := s.sp.stops(); !slices.Equal(got, []string{quiet.AgentID}) {
		t.Fatalf("stops: %v", got)
	}
	if conn := s.get(late.ID).ConnectedAt; conn == nil || !conn.Equal(time.Date(2026, 11, 21, 9, 1, 0, 0, time.UTC)) {
		t.Fatalf("connected_at: %v", conn)
	}
}

func TestIdleTTLStopsOnlyIdleAgents(t *testing.T) {
	s := newSim(t, lifecycle.Config{ConnectTimeout: 5 * time.Minute, IdleTTL: 15 * time.Minute})
	bid := s.brief()
	sps := s.spawn(bid, 2)
	busy, idle := sps[0], sps[1]
	s.b.Touch(busy.AgentID)
	s.b.Touch(idle.AgentID)
	tid := s.assignTask(bid, busy.AgentID)

	s.tick()
	s.want(busy.ID, "spawned", "active", "")
	s.want(idle.ID, "spawned", "idle", "")

	s.clk.advance(15 * time.Minute)
	s.tick()
	s.want(idle.ID, "stopped", "stopped", lifecycle.ReasonIdle)
	s.want(busy.ID, "spawned", "active", "")
	if s.agentStatus(idle.AgentID) != model.AgentRevoked {
		t.Fatal("idle agent not revoked")
	}

	if _, err := s.b.Cancel(s.ctx, tid, "done"); err != nil {
		t.Fatal(err)
	}
	s.clk.advance(time.Minute)
	s.tick()
	s.want(busy.ID, "spawned", "idle", "")
	s.clk.advance(14 * time.Minute)
	s.tick()
	s.want(busy.ID, "spawned", "idle", "")
	s.clk.advance(time.Minute)
	s.tick()
	s.want(busy.ID, "stopped", "stopped", lifecycle.ReasonIdle)
	if got := s.sp.stops(); !slices.Equal(got, []string{idle.AgentID, busy.AgentID}) {
		t.Fatalf("stops: %v", got)
	}
}

func TestBriefCloseStopsItsSpawns(t *testing.T) {
	s := newSim(t, lifecycle.Config{ConnectTimeout: time.Hour, IdleTTL: time.Hour})
	closing, other := s.brief(), s.brief()
	a := s.spawn(closing, 2)
	b := s.spawn(other, 1)
	closed := "closed"
	if _, err := s.b.UpdateBrief(s.ctx, closing, board.BriefPatch{Status: &closed}); err != nil {
		t.Fatal(err)
	}
	s.tick()
	for _, sp := range a {
		s.want(sp.ID, "stopped", "stopped", lifecycle.ReasonBriefClosed)
		if s.agentStatus(sp.AgentID) != model.AgentRevoked {
			t.Fatalf("%s not revoked", sp.AgentID)
		}
	}
	s.want(b[0].ID, "spawned", "connecting", "")
}

func TestPauseAllStopsOnlyAfterConfiguredGrace(t *testing.T) {
	off := newSim(t, lifecycle.Config{ConnectTimeout: 0, IdleTTL: 0})
	sp := off.spawn(off.brief(), 1)[0]
	if err := off.b.PauseAll(off.ctx, "x"); err != nil {
		t.Fatal(err)
	}
	off.tick()
	off.clk.advance(24 * time.Hour)
	off.tick()
	off.want(sp.ID, "spawned", "connecting", "")

	s := newSim(t, lifecycle.Config{PauseGrace: 10 * time.Minute})
	sps := s.spawn(s.brief(), 2)
	if err := s.b.PauseAll(s.ctx, "x"); err != nil {
		t.Fatal(err)
	}
	s.tick()
	s.clk.advance(9 * time.Minute)
	s.tick()
	if err := s.b.ResumeAll(s.ctx); err != nil {
		t.Fatal(err)
	}
	s.tick()
	if err := s.b.PauseAll(s.ctx, "again"); err != nil {
		t.Fatal(err)
	}
	s.clk.advance(time.Minute)
	s.tick()
	s.clk.advance(9 * time.Minute)
	s.tick()
	s.want(sps[0].ID, "spawned", "connecting", "")
	s.clk.advance(time.Minute)
	s.tick()
	for _, sp := range sps {
		s.want(sp.ID, "stopped", "stopped", lifecycle.ReasonPaused)
	}
}

func TestStopFailureIsRetried(t *testing.T) {
	s := newSim(t, lifecycle.Config{ConnectTimeout: time.Minute})
	sp := s.spawn(s.brief(), 1)[0]
	s.sp.stopErr = errors.New("host unreachable")
	s.clk.advance(time.Minute)
	if err := s.r.Tick(s.ctx); err == nil {
		t.Fatal("tick should report the stop failure")
	}
	if s.agentStatus(sp.AgentID) != model.AgentRevoked {
		t.Fatal("agent must be revoked even if the process could not be stopped")
	}
	s.want(sp.ID, "spawned", "connecting", lifecycle.ReasonConnectTimeout)

	s.sp.mu.Lock()
	s.sp.stopErr = nil
	s.sp.mu.Unlock()
	s.tick()
	s.want(sp.ID, "failed", "failed", lifecycle.ReasonConnectTimeout)
	if got := s.sp.stops(); !slices.Equal(got, []string{sp.AgentID}) {
		t.Fatalf("stops: %v", got)
	}
}

func TestReconcileAfterRestart(t *testing.T) {
	s := newSim(t, lifecycle.Config{ConnectTimeout: time.Hour, IdleTTL: time.Hour})
	sps := s.spawn(s.brief(), 3)
	running, exited, unknown := sps[0], sps[1], sps[2]
	s.b.Touch(running.AgentID)
	s.tick()
	idleSince := s.get(running.ID).IdleSince
	if idleSince == nil {
		t.Fatal("idle_since not recorded")
	}

	s.restart()
	s.sp.status = map[string]spawner.Status{running.AgentID: spawner.StatusRunning, exited.AgentID: spawner.StatusExited}
	s.clk.advance(time.Minute)
	if err := s.r.Reconcile(s.ctx); err != nil {
		t.Fatal(err)
	}
	got := s.want(running.ID, "spawned", "idle", "")
	if got.Process != "running" || got.ProcessCheckedAt == nil || !got.IdleSince.Equal(*idleSince) {
		t.Fatalf("running spawn after restart: %+v", got)
	}
	s.want(exited.ID, "stopped", "stopped", lifecycle.ReasonExited)
	if s.agentStatus(exited.AgentID) != model.AgentRevoked {
		t.Fatal("exited agent not revoked")
	}
	if got := s.want(unknown.ID, "spawned", "connecting", ""); got.Process != "unknown" {
		t.Fatalf("unknown spawn process: %s", got.Process)
	}
}

func TestOwnerStop(t *testing.T) {
	s := newSim(t, lifecycle.Config{})
	sp := s.spawn(s.brief(), 1)[0]
	got, err := s.r.Stop(s.ctx, sp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "stopped" || got.Lifecycle != "stopped" || got.Process != "exited" || *got.StopReason != lifecycle.ReasonOwner {
		t.Fatalf("stopped spawn: %+v", got)
	}
	if _, err := s.r.Stop(s.ctx, sp.ID); model.AsError(err).Code != "conflict" {
		t.Fatalf("second stop: %v", err)
	}
	if _, err := s.r.Stop(s.ctx, "S-404"); model.AsError(err).Code != "not_found" {
		t.Fatalf("unknown stop: %v", err)
	}
	audit, err := s.b.ListAudit(s.ctx, store.AuditFilter{Tool: "stop_spawn"})
	if err != nil || len(audit) != 1 || audit[0].Role != model.RoleOwner {
		t.Fatalf("audit: %v %v", audit, err)
	}
}
