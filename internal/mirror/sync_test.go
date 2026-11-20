package mirror

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/ratelimit"
)

type memLinks struct {
	mu sync.Mutex
	m  map[string]Link
}

func newMemLinks() *memLinks { return &memLinks{m: map[string]Link{}} }

func (l *memLinks) Get(_ context.Context, id string) (Link, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	k, ok := l.m[id]
	return k, ok, nil
}

func (l *memLinks) Put(_ context.Context, id string, k Link) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.m[id] = k
	return nil
}

type gate struct {
	f      *fakeForgejo
	srv    *httptest.Server
	mu     sync.Mutex
	calls  int
	fail   int
	bodies []string
}

func newGate(t *testing.T, f *fakeForgejo) *gate {
	g := &gate{f: f}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		g.calls++
		g.bodies = append(g.bodies, string(raw))
		fail := g.fail > 0
		if fail {
			g.fail--
		}
		g.mu.Unlock()
		if fail {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(raw)))
		f.handle(w, r)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *gate) requests() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

func (g *gate) mirror(t *testing.T) *ForgejoMirror {
	m, err := NewForgejo(ForgejoConfig{BaseURL: g.srv.URL, Token: "secret", Owner: "o", Repo: "r"})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (f *fakeForgejo) snapshot() []fakeIssue {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeIssue, len(f.issues))
	for i, is := range f.issues {
		out[i] = *is
	}
	return out
}

type rig struct {
	t     *testing.T
	hub   events.Hub
	clk   *ratelimit.FakeClock
	links *memLinks
	gate  *gate
	stop  func()
}

func newRig(t *testing.T, mod func(*SyncConfig)) *rig {
	t.Helper()
	f := newFakeForgejo(t)
	r := &rig{t: t, hub: events.NewHub(), clk: ratelimit.NewFakeClock(time.Unix(1_800_000_000, 0)), links: newMemLinks(), gate: newGate(t, f)}
	r.start(r.gate, r.links, mod)
	return r
}

func (r *rig) start(g *gate, links Links, mod func(*SyncConfig)) {
	cfg := SyncConfig{
		Mirrors:    map[string]IssueMirror{"o/r": g.mirror(r.t)},
		Links:      links,
		BriefTitle: func(context.Context, string) string { return "Auth" },
		Clock:      r.clk,
		Burst:      100,
		RatePerMin: 6000,
		BaseDelay:  time.Second,
		MaxDelay:   8 * time.Second,
	}
	if mod != nil {
		mod(&cfg)
	}
	if r.stop != nil {
		r.stop()
	}
	s := NewSyncer(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx, r.hub)
	r.stop = func() { cancel(); s.Wait() }
	r.t.Cleanup(r.stop)
}

func (r *rig) emit(typ string, t model.Task) {
	r.hub.Publish([]string{"board"}, typ, &t)
}

func (r *rig) eventually(cond func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			r.t.Fatal("condition not met in time")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func task(id, status string) model.Task {
	return model.Task{ID: id, BriefID: "B-1", Title: "Add login " + id, Repo: "o/r", Status: status, AcceptanceCriteria: []string{"works"}}
}

func TestSyncCreatesUpdatesAndClosesOneIssuePerTask(t *testing.T) {
	r := newRig(t, nil)
	f := r.gate.f

	r.emit("task.created", task("T-1", "backlog"))
	r.eventually(func() bool { return len(f.snapshot()) == 1 })
	is := f.snapshot()[0]
	if is.State != "open" || !strings.Contains(is.Body, Marker("T-1")) || !strings.Contains(is.Body, "Auth") {
		t.Fatalf("issue = %+v", is)
	}

	r.emit("task.updated", task("T-1", "in_progress"))
	labels := func() string {
		f.mu.Lock()
		defer f.mu.Unlock()
		return strings.Join(f.labelNames(f.issues[0]), ",")
	}
	r.eventually(func() bool { return strings.Contains(labels(), "status/in_progress") })
	if names := labels(); strings.Contains(names, "status/backlog") || !strings.Contains(names, "subspace") {
		t.Fatalf("labels = %s", names)
	}

	r.emit("task.created", task("T-2", "ready"))
	r.eventually(func() bool { return len(f.snapshot()) == 2 })

	r.emit("task.updated", task("T-1", "done"))
	r.emit("task.updated", task("T-2", "cancelled"))
	r.eventually(func() bool {
		s := f.snapshot()
		return len(s) == 2 && s[0].State == "closed" && s[1].State == "closed"
	})
}

func TestSyncIgnoresUnmappedRepo(t *testing.T) {
	r := newRig(t, nil)
	other := task("T-1", "ready")
	other.Repo = "x/y"
	r.emit("task.created", other)
	r.emit("task.created", task("T-2", "ready"))
	r.eventually(func() bool { return len(r.gate.f.snapshot()) == 1 })
	if !strings.Contains(r.gate.f.snapshot()[0].Body, Marker("T-2")) {
		t.Fatal("wrong task mirrored")
	}
}

func TestSyncRetriesWithBackoffOnInjectedClock(t *testing.T) {
	r := newRig(t, nil)
	r.gate.mu.Lock()
	r.gate.fail = 2
	r.gate.mu.Unlock()

	r.emit("task.created", task("T-1", "ready"))
	r.eventually(func() bool { return r.clk.Pending() == 1 })
	if r.gate.requests() != 1 {
		t.Fatalf("requests = %d", r.gate.requests())
	}
	r.clk.Advance(time.Second - time.Nanosecond)
	time.Sleep(20 * time.Millisecond)
	if r.gate.requests() != 1 || r.clk.Pending() != 1 {
		t.Fatalf("retried before the backoff elapsed: requests=%d", r.gate.requests())
	}
	r.clk.Advance(time.Nanosecond)
	r.eventually(func() bool { return r.gate.requests() == 2 && r.clk.Pending() == 1 })
	r.clk.Advance(2 * time.Second)
	r.eventually(func() bool {
		_, ok, _ := r.links.Get(context.Background(), "T-1")
		return ok && len(r.gate.f.snapshot()) == 1
	})
}

func TestSyncGivesUpAfterAttemptsAndRecoversOnNextEvent(t *testing.T) {
	r := newRig(t, func(c *SyncConfig) { c.Attempts = 2 })
	r.gate.mu.Lock()
	r.gate.fail = 2
	r.gate.mu.Unlock()

	r.emit("task.created", task("T-1", "ready"))
	r.eventually(func() bool { return r.clk.Pending() == 1 })
	r.clk.Advance(time.Second)
	r.eventually(func() bool { return r.gate.requests() == 2 })
	time.Sleep(20 * time.Millisecond)
	if len(r.gate.f.snapshot()) != 0 || r.clk.Pending() != 0 {
		t.Fatal("expected the task to be dropped after the last attempt")
	}
	if _, ok, _ := r.links.Get(context.Background(), "T-1"); ok {
		t.Fatal("link stored for a failed sync")
	}

	r.emit("task.updated", task("T-1", "assigned"))
	r.eventually(func() bool { return len(r.gate.f.snapshot()) == 1 })
}

func TestSyncIdempotentAfterRestart(t *testing.T) {
	r := newRig(t, nil)
	f := r.gate.f
	r.emit("task.created", task("T-1", "ready"))
	r.eventually(func() bool { return len(f.snapshot()) == 1 })
	before := r.gate.requests()

	r.start(r.gate, r.links, nil)
	r.emit("task.updated", task("T-1", "ready"))
	r.emit("task.created", task("T-2", "ready"))
	r.eventually(func() bool { return len(f.snapshot()) == 2 })
	for i := 0; i < 2; i++ {
		r.emit("task.updated", task("T-1", "ready"))
	}
	time.Sleep(30 * time.Millisecond)
	if len(f.snapshot()) != 2 {
		t.Fatalf("issues = %d", len(f.snapshot()))
	}
	after := r.gate.requests()
	r.emit("task.updated", task("T-1", "ready"))
	time.Sleep(30 * time.Millisecond)
	if r.gate.requests() != after || after <= before {
		t.Fatalf("unchanged task triggered requests: before=%d after=%d now=%d", before, after, r.gate.requests())
	}

	r.emit("task.updated", task("T-1", "done"))
	r.eventually(func() bool { return f.snapshot()[0].State == "closed" })
	if len(f.snapshot()) != 2 {
		t.Fatalf("issues = %d", len(f.snapshot()))
	}
}

func TestSyncDoesNotDuplicateWhenLinksAreLost(t *testing.T) {
	r := newRig(t, nil)
	f := r.gate.f
	r.emit("task.created", task("T-1", "ready"))
	r.eventually(func() bool { return len(f.snapshot()) == 1 })

	r.start(r.gate, newMemLinks(), nil)
	r.emit("task.updated", task("T-1", "in_progress"))
	r.eventually(func() bool { return strings.Contains(f.snapshot()[0].Body, "`in_progress`") })
	if len(f.snapshot()) != 1 {
		t.Fatalf("issues = %d", len(f.snapshot()))
	}
}

func TestSyncIsRateLimited(t *testing.T) {
	r := newRig(t, func(c *SyncConfig) { c.Burst = 1; c.RatePerMin = 60 })
	f := r.gate.f
	for _, id := range []string{"T-1", "T-2", "T-3"} {
		r.emit("task.created", task(id, "ready"))
	}
	r.eventually(func() bool { return len(f.snapshot()) == 1 && r.clk.Pending() == 1 })
	for want := 2; want <= 3; want++ {
		r.clk.Advance(time.Second)
		r.eventually(func() bool { return len(f.snapshot()) == want })
		if want < 3 {
			r.eventually(func() bool { return r.clk.Pending() == 1 })
		}
	}
}

func TestSyncSendsNoSecrets(t *testing.T) {
	r := newRig(t, nil)
	tk := task("T-1", "submitted")
	tk.Title = "Deploy with ghp_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"
	tk.AcceptanceCriteria = []string{"use sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcdefgh"}
	pr := "https://bot:hunter2hunter2@git.example/o/r/pulls/1?token=abc"
	tk.PRURL = &pr
	r.emit("task.created", tk)
	r.eventually(func() bool { return len(r.gate.f.snapshot()) == 1 })

	r.gate.mu.Lock()
	defer r.gate.mu.Unlock()
	for _, b := range r.gate.bodies {
		for _, secret := range []string{"ghp_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789", "sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcdefgh", "hunter2", "token=abc"} {
			if strings.Contains(b, secret) {
				t.Fatalf("request body leaks %q: %s", secret, b)
			}
		}
	}
}

func TestEnqueueNeverBlocksAndCoalesces(t *testing.T) {
	s := NewSyncer(SyncConfig{Mirrors: map[string]IssueMirror{"o/r": NoopMirror{}}, Links: newMemLinks(), MaxQueued: 3})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10_000; i++ {
			tk := task("T-"+string(rune('a'+i%26)), "ready")
			s.Enqueue(&tk)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Enqueue blocked without a consumer")
	}
	if len(s.order) > 3 || len(s.pending) > 3 {
		t.Fatalf("queue grew past its bound: %d/%d", len(s.order), len(s.pending))
	}
}
