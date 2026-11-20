package backup

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
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
	return out
}

func realStore(t *testing.T) *sqlite.Store {
	t.Helper()
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for range 20 {
		if err := st.Atomic(context.Background(), func(tx store.Tx) error { _, err := tx.NextID("B"); return err }); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func TestSchedulerTicksRetainsNewestAndSurvivesFailure(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	tk := &fakeTicker{c: make(chan time.Time)}
	st := realStore(t)
	var calls int
	done := make(chan struct{}, 8)
	s, err := New(Config{
		Dir: dir, Interval: 6 * time.Hour, Keep: 2, Logger: quiet(), Now: clk.Now,
		Run: func(ctx context.Context, dst string) error {
			calls++
			defer func() { done <- struct{}{} }()
			if calls == 2 || calls == 6 {
				return errors.New("disk full")
			}
			return st.Backup(ctx, dst)
		},
		Verify:    sqlite.IntegrityCheck,
		NewTicker: func(d time.Duration) Ticker { return tk },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { s.Run(ctx); close(stopped) }()
	for range 6 {
		tk.c <- clk.Now()
		<-done
		clk.Advance(6 * time.Hour)
	}
	cancel()
	<-stopped

	want := []string{"subspace-20260101T180000Z.db", "subspace-20260102T000000Z.db"}
	if got := names(t, dir); !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestRetentionIgnoresForeignFiles(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	st := realStore(t)
	for _, n := range []string{"subspace-20200101T000000Z.db", "subspace-20200102T000000Z.db", "notes.txt", "subspace.db", "subspace-20261301T000000Z.db"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(Config{Dir: dir, Keep: 2, Run: st.Backup, Verify: sqlite.IntegrityCheck, Logger: quiet(), Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"notes.txt", "subspace-20200102T000000Z.db", "subspace-20260301T120000Z.db", "subspace-20261301T000000Z.db", "subspace.db"}
	if got := names(t, dir); !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestCorruptionDetected(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	st := realStore(t)
	var corrupt bool
	s, err := New(Config{
		Dir: dir, Keep: 5, Verify: sqlite.IntegrityCheck, Logger: quiet(), Now: clk.Now,
		Run: func(ctx context.Context, dst string) error {
			if err := st.Backup(ctx, dst); err != nil {
				return err
			}
			if corrupt {
				return os.Truncate(dst, 100)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	good, err := s.RunOnce(context.Background())
	if err != nil || !good.Verified || good.Bytes == 0 {
		t.Fatalf("good backup: %+v %v", good, err)
	}

	clk.Advance(time.Hour)
	corrupt = true
	if _, err := s.RunOnce(context.Background()); err == nil {
		t.Fatal("corrupt backup must fail verification")
	}
	if got := names(t, dir); len(got) != 1 {
		t.Fatalf("corrupt backup must not be kept: %v", got)
	}

	list, err := s.List(context.Background())
	if err != nil || len(list) != 1 || !list[0].Verified {
		t.Fatalf("list before damage: %+v %v", list, err)
	}

	data, err := os.ReadFile(good.Path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 4096; i < len(data) && i < 4096+512; i++ {
		data[i] ^= 0xFF
	}
	if err := os.WriteFile(good.Path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	list, err = s.List(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("list after damage: %+v %v", list, err)
	}
	if list[0].Verified {
		t.Fatal("damaged backup reported as verified")
	}
	if list[0].AgeSeconds != 3600 {
		t.Fatalf("age = %d", list[0].AgeSeconds)
	}
}

func TestSameSecondCollisionKeepsExisting(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	st := realStore(t)
	s, _ := New(Config{Dir: dir, Run: st.Backup, Verify: sqlite.IntegrityCheck, Logger: quiet(), Now: clk.Now})
	first, err := s.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunOnce(context.Background()); !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(first.Path); err != nil {
		t.Fatal("existing backup removed")
	}
}
