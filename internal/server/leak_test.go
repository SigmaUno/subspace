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
	"testing"
	"time"

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
