package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSIGHUPReloadsConfigAndKeepsOldConfigOnError(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(cfgPath, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{}`)
	var out lockedBuffer
	level := new(slog.LevelVar)
	level.Set(slog.LevelWarn)
	log := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := server.New(ctx, server.Config{
		DBPath: filepath.Join(dir, "t.db"), OwnerToken: "owner-token-for-tests-0123456789",
		ConfigFile: cfgPath, LogLevel: level, Log: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	stop := reloadOnSIGHUP(ctx, s, log)
	defer stop()

	hup := func() {
		t.Helper()
		if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
			t.Fatal(err)
		}
	}

	write(`{"SUBSPACE_LOG_LEVEL":"debug"}`)
	hup()
	waitFor(t, "level change", func() bool { return level.Level() == slog.LevelDebug })
	waitFor(t, "reload log", func() bool { return strings.Contains(out.String(), "config reloaded") })

	write(`{"SUBSPACE_LOG_LEVEL":"error","SUBSPACE_OWNER_ALLOW_CIDRS":"bogus"}`)
	hup()
	waitFor(t, "rejection log", func() bool { return strings.Contains(out.String(), "config reload rejected") })
	if level.Level() != slog.LevelDebug {
		t.Fatal("rejected reload changed the log level")
	}

	write(`{"SUBSPACE_LOG_LEVEL":"error"}`)
	hup()
	waitFor(t, "second level change", func() bool { return level.Level() == slog.LevelError })
}
