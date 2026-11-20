package backup

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

type fakeTicker struct{ c chan time.Time }

func (f *fakeTicker) C() <-chan time.Time { return f.c }
func (f *fakeTicker) Stop()               {}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func writeRun(_ context.Context, dst string) error {
	return os.WriteFile(dst, []byte("db"), 0o600)
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNewValidation(t *testing.T) {
	if _, err := New(Config{Run: writeRun}); err == nil {
		t.Fatal("want error for missing Dir")
	}
	if _, err := New(Config{Dir: t.TempDir()}); err == nil {
		t.Fatal("want error for missing Run")
	}
	if _, err := New(Config{Dir: t.TempDir(), Run: writeRun, Keep: -1}); err == nil {
		t.Fatal("want error for negative Keep")
	}
	s, err := New(Config{Dir: t.TempDir(), Run: writeRun})
	if err != nil {
		t.Fatal(err)
	}
	if s.cfg.Keep != DefaultKeep {
		t.Fatalf("Keep = %d, want %d", s.cfg.Keep, DefaultKeep)
	}
}

func TestRunOnceNameAndVerify(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	clk := &fakeClock{t: time.Date(2026, 11, 20, 21, 4, 5, 0, time.UTC)}
	var verified string
	s, err := New(Config{
		Dir: dir, Run: writeRun, Logger: quiet(), Now: clk.Now,
		Verify: func(_ context.Context, p string) error { verified = p; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "subspace-20261120-210405.db")
	if p != want || verified != want {
		t.Fatalf("path=%q verified=%q want %q", p, verified, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunOnce(context.Background()); err == nil {
		t.Fatal("want error when file for the same second exists")
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal("existing backup must not be removed on collision")
	}
}

func TestRunOnceFailuresCleanUpAndKeepOld(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "subspace-20200101-000000.db")
	if err := os.WriteFile(old, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}

	failRun := func(_ context.Context, dst string) error {
		_ = os.WriteFile(dst, []byte("partial"), 0o600)
		return errors.New("boom")
	}
	s, _ := New(Config{Dir: dir, Keep: 1, Run: failRun, Logger: quiet(), Now: clk.Now})
	if _, err := s.RunOnce(context.Background()); err == nil {
		t.Fatal("want run error")
	}
	if got := names(t, dir); !equal(got, []string{"subspace-20200101-000000.db"}) {
		t.Fatalf("after run failure: %v", got)
	}

	s, _ = New(Config{
		Dir: dir, Keep: 1, Run: writeRun, Logger: quiet(), Now: clk.Now,
		Verify: func(context.Context, string) error { return errors.New("corrupt") },
	})
	if _, err := s.RunOnce(context.Background()); err == nil {
		t.Fatal("want verify error")
	}
	if got := names(t, dir); !equal(got, []string{"subspace-20200101-000000.db"}) {
		t.Fatalf("after verify failure: %v", got)
	}
}

func TestPruneKeepsNewestAndIgnoresOthers(t *testing.T) {
	dir := t.TempDir()
	backups := []string{
		"subspace-20260101-000000.db",
		"subspace-20260102-000000.db",
		"subspace-20260103-000000.db",
		"subspace-20260104-000000.db",
		"subspace-20260105-000000.db",
	}
	others := []string{
		"notes.txt",
		"subspace.db",
		"subspace-20260101-000000.db-wal",
		"subspace-2026-01-01.db",
		"subspace-20261301-000000.db", // matches shape, not a valid time
		"other-20200101-000000.db",
	}
	for _, n := range append(append([]string{}, backups...), others...) {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// a directory with a matching name must survive
	if err := os.Mkdir(filepath.Join(dir, "subspace-20190101-000000.db"), 0o750); err != nil {
		t.Fatal(err)
	}
	s, _ := New(Config{Dir: dir, Keep: 2, Run: writeRun, Logger: quiet()})
	if err := s.Prune(); err != nil {
		t.Fatal(err)
	}
	want := append([]string{"subspace-20190101-000000.db", backups[3], backups[4]}, others...)
	sort.Strings(want)
	if got := names(t, dir); !equal(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestRunOncePrunesAfterSuccess(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	s, _ := New(Config{Dir: dir, Keep: 3, Run: writeRun, Logger: quiet(), Now: clk.Now})
	for range 6 {
		if _, err := s.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		clk.Advance(time.Hour)
	}
	want := []string{
		"subspace-20260101-030000.db",
		"subspace-20260101-040000.db",
		"subspace-20260101-050000.db",
	}
	if got := names(t, dir); !equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestRunTicksAndStopsOnCancel(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	tk := &fakeTicker{c: make(chan time.Time)}
	var gotInterval time.Duration
	done := make(chan string, 8)
	s, _ := New(Config{
		Dir: dir, Interval: 6 * time.Hour, Keep: 2, Logger: quiet(), Now: clk.Now,
		Run: func(ctx context.Context, dst string) error {
			if err := writeRun(ctx, dst); err != nil {
				return err
			}
			done <- dst
			return nil
		},
		NewTicker: func(d time.Duration) Ticker { gotInterval = d; return tk },
	})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { s.Run(ctx); close(stopped) }()

	for range 3 {
		tk.c <- clk.Now()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("backup did not run after tick")
		}
		clk.Advance(6 * time.Hour)
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
	if gotInterval != 6*time.Hour {
		t.Fatalf("interval = %v", gotInterval)
	}
	// the last tick's prune may race with our read of the dir; Run has stopped, so it is settled
	want := []string{"subspace-20260101-060000.db", "subspace-20260101-120000.db"}
	if got := names(t, dir); !equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestRunContinuesAfterFailure(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	tk := &fakeTicker{c: make(chan time.Time)}
	calls := make(chan int, 4)
	n := 0
	s, _ := New(Config{
		Dir: dir, Interval: time.Minute, Logger: quiet(), Now: clk.Now,
		Run: func(ctx context.Context, dst string) error {
			n++
			calls <- n
			if n == 1 {
				return errors.New("first fails")
			}
			return writeRun(ctx, dst)
		},
		NewTicker: func(time.Duration) Ticker { return tk },
	})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { s.Run(ctx); close(stopped) }()
	for i := 1; i <= 2; i++ {
		tk.c <- clk.Now()
		select {
		case <-calls:
		case <-time.After(5 * time.Second):
			t.Fatal("no backup call")
		}
		clk.Advance(time.Minute)
	}
	cancel()
	<-stopped
	if got := names(t, dir); len(got) != 1 {
		t.Fatalf("got %v, want exactly one backup", got)
	}
}

func TestRunDisabledWhenIntervalZero(t *testing.T) {
	s, _ := New(Config{
		Dir: t.TempDir(), Run: writeRun, Logger: quiet(),
		NewTicker: func(time.Duration) Ticker { t.Fatal("ticker must not be created"); return nil },
	})
	done := make(chan struct{})
	go func() { s.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run should return immediately when disabled")
	}
}
