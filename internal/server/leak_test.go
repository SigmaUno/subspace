package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/spawner"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

func settleGoroutines(t *testing.T, base int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for runtime.NumGoroutine() > base+2 {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			t.Fatalf("goroutines %d, base %d\n%s", runtime.NumGoroutine(), base, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServerLifecycleReleasesGoroutines(t *testing.T) {
	t.Setenv("SUBSPACE_NOTIFY_WEBHOOK_URL", "http://127.0.0.1:1/hook")
	t.Setenv("SUBSPACE_NOTIFY_ALLOW_PRIVATE", "true")
	t.Setenv(mirrorEnvURL, "http://127.0.0.1:1")
	t.Setenv(mirrorEnvToken, "test-token")
	t.Setenv(mirrorEnvRepoMap, "o/r=o/r")
	dir := t.TempDir()
	cycle := func() {
		ctx, cancel := context.WithCancel(context.Background())
		s, err := New(ctx, Config{DBPath: filepath.Join(dir, "t.db"), OwnerToken: ownerTok, Log: slog.New(slog.DiscardHandler)})
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		_ = s.Close()
	}
	cycle()
	base := runtime.NumGoroutine()
	for range 10 {
		cycle()
	}
	settleGoroutines(t, base)
}

func TestRunShutdownEndsStreamsAndFlushes(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	db := filepath.Join(t.TempDir(), "t.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := New(ctx, Config{Addr: addr, DBPath: db, OwnerToken: ownerTok, PublicURL: "http://sub.test",
		FlushEvery: time.Hour, BackupInterval: time.Hour, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	ran := make(chan error, 1)
	go func() { ran <- s.Run(ctx) }()

	e := &env{t: t, ts: &httptest.Server{URL: "http://" + addr}}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(e.ts.URL + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	tok := e.register("w1", "worker", "claude-code")
	e.must(tok, "whoami", nil)

	req, _ := http.NewRequest("GET", e.ts.URL+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+ownerTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events: %d", resp.StatusCode)
	}

	http.DefaultClient.CloseIdleConnections()
	start := time.Now()
	cancel()
	select {
	case err := <-ran:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("shutdown took %v with an open event stream", d)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := sqlite.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	var seen *time.Time
	err = st.View(context.Background(), func(tx store.Tx) error {
		a, err := tx.GetAgent("w1")
		if err == nil {
			seen = a.LastSeenAt
		}
		return err
	})
	if err != nil || seen == nil {
		t.Fatalf("last_seen_at not flushed on shutdown: %v %v", seen, err)
	}
}

type blockingSpawner struct{ started chan struct{} }

func (blockingSpawner) Name() string { return "hc" }

func (s blockingSpawner) Spawn(ctx context.Context, _ spawner.Request) error {
	close(s.started)
	<-ctx.Done()
	return ctx.Err()
}

func TestSpawnSettledWhenRequestIsAborted(t *testing.T) {
	sp := blockingSpawner{started: make(chan struct{})}
	e := newEnvCfg(t, Config{Spawner: sp})
	lead := e.register("rex", "leader", "other")
	bid := e.secBrief(map[string]any{"title": "B"})

	ctx, cancel := context.WithCancel(context.Background())
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"request_spawn","arguments":{"brief_id":"` + bid + `","worker_kind":"claude-code","model":"m","n":1}}}`
	req, _ := http.NewRequestWithContext(ctx, "POST", e.ts.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+lead)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-sp.started
	cancel()
	<-done

	deadline := time.Now().Add(5 * time.Second)
	for {
		_, out := e.rest("GET", "/api/v1/spawns", nil)
		list := out["spawns"].([]any)
		if len(list) == 1 && list[0].(map[string]any)["status"] == "failed" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("spawn left unsettled after the request was aborted: %v", list)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHeartbeatLeaseIsFlushedToStore(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	wk := e.register("w1", "worker", "claude-code")
	id := e.approvedPlan(lead, e.brief(map[string]any{}), planTask("a", nil))[0]
	e.must(lead, "assign", map[string]any{"task_id": id, "agent_id": "w1"})
	e.must(wk, "claim_task", map[string]any{"task_id": id})
	time.Sleep(20 * time.Millisecond)
	hb, err := time.Parse(time.RFC3339Nano, e.must(wk, "heartbeat", map[string]any{"task_id": id})["lease_expires_at"].(string))
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		var stored time.Time
		err := e.srv.Store.View(context.Background(), func(tx store.Tx) error {
			tk, err := tx.GetTask(id)
			if err == nil && tk.LeaseExpiresAt != nil {
				stored = *tk.LeaseExpiresAt
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if !stored.Before(hb.Truncate(time.Millisecond)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("stored lease %v, heartbeat extended it to %v", stored, hb)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
