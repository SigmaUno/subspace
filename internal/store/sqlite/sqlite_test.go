package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/store"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seqValue(t *testing.T, db *sql.DB, prefix string) int64 {
	t.Helper()
	var n int64
	err := db.QueryRow(`SELECT n FROM seq WHERE name=?`, prefix).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0
	}
	if err != nil {
		t.Fatalf("read seq %s: %v", prefix, err)
	}
	return n
}

func sqlTx(t store.Tx) *sql.Tx {
	x, ok := t.(*tx)
	if !ok {
		return nil
	}
	w, ok := x.q.(*writeQ)
	if !ok {
		return nil
	}
	return w.tx
}

func blockWriter(t *testing.T, s *Store) (release func()) {
	t.Helper()
	started := make(chan struct{})
	gate := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.Atomic(context.Background(), func(store.Tx) error {
			close(started)
			<-gate
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("blocker did not start")
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			close(gate)
			if err := <-done; err != nil {
				t.Errorf("blocker: %v", err)
			}
		})
	}
	t.Cleanup(release)
	return release
}

func waitQueued(t *testing.T, s *Store, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for len(s.reqs) < n {
		if time.Now().After(deadline) {
			t.Fatalf("queue length %d, want %d", len(s.reqs), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func enqueueInOrder(t *testing.T, s *Store, fns ...func(store.Tx) error) []chan error {
	t.Helper()
	res := make([]chan error, len(fns))
	base := len(s.reqs)
	for i, fn := range fns {
		res[i] = make(chan error, 1)
		go func(fn func(store.Tx) error, out chan error) {
			out <- s.Atomic(context.Background(), fn)
		}(fn, res[i])
		waitQueued(t, s, base+i+1)
	}
	return res
}

func TestGroupCommit500(t *testing.T) {
	s := openTest(t)
	release := blockWriter(t, s)

	const n = 500
	var (
		mu  sync.Mutex
		txs = map[*sql.Tx]int{}
		ids = make([]string, n)
	)
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = s.Atomic(context.Background(), func(t store.Tx) error {
				id, err := t.NextID("G")
				if err != nil {
					return err
				}
				mu.Lock()
				txs[sqlTx(t)]++
				ids[i] = id
				mu.Unlock()
				return nil
			})
		}(i)
	}
	waitQueued(t, s, n)
	release()
	wg.Wait()

	seen := map[string]bool{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("op %d: %v", i, err)
		}
		if seen[ids[i]] {
			t.Fatalf("duplicate id %s", ids[i])
		}
		seen[ids[i]] = true
	}
	for i := 1; i <= n; i++ {
		if !seen[fmt.Sprintf("G-%d", i)] {
			t.Fatalf("missing G-%d", i)
		}
	}
	if _, ok := txs[nil]; ok {
		t.Fatal("write op did not run inside a *sql.Tx")
	}
	maxTx := (n + maxBatch - 1) / maxBatch
	if len(txs) > maxTx {
		t.Fatalf("500 queued writes used %d transactions, want <= %d", len(txs), maxTx)
	}
	for _, c := range txs {
		if c > maxBatch {
			t.Fatalf("batch of %d exceeds maxBatch %d", c, maxBatch)
		}
	}
	if got := seqValue(t, s.rdb, "G"); got != n {
		t.Fatalf("seq G = %d, want %d", got, n)
	}
}

func TestSavepointIsolation(t *testing.T) {
	s := openTest(t)
	release := blockWriter(t, s)

	var (
		mu   sync.Mutex
		txs  = map[*sql.Tx]bool{}
		got1 string
		got2 string
	)
	sentinel := errors.New("boom")
	res := enqueueInOrder(t, s,
		func(t store.Tx) error {
			id, err := t.NextID("I")
			mu.Lock()
			got1, txs[sqlTx(t)] = id, true
			mu.Unlock()
			return err
		},
		func(t store.Tx) error {
			mu.Lock()
			txs[sqlTx(t)] = true
			mu.Unlock()
			if _, err := t.NextID("I"); err != nil {
				return err
			}
			if _, err := t.NextID("BAD"); err != nil {
				return err
			}
			return sentinel
		},
		func(t store.Tx) error {
			id, err := t.NextID("I")
			mu.Lock()
			got2, txs[sqlTx(t)] = id, true
			mu.Unlock()
			return err
		},
	)
	release()

	if err := <-res[0]; err != nil {
		t.Fatalf("op1: %v", err)
	}
	if err := <-res[1]; !errors.Is(err, sentinel) {
		t.Fatalf("op2 err = %v, want sentinel", err)
	}
	if err := <-res[2]; err != nil {
		t.Fatalf("op3: %v", err)
	}
	if len(txs) != 1 {
		t.Fatalf("ops ran in %d transactions, want 1 (same batch)", len(txs))
	}
	if got1 != "I-1" || got2 != "I-2" {
		t.Fatalf("ids = %s, %s; want I-1, I-2 (failed op rolled back)", got1, got2)
	}
	if v := seqValue(t, s.rdb, "I"); v != 2 {
		t.Fatalf("seq I = %d, want 2", v)
	}
	if v := seqValue(t, s.rdb, "BAD"); v != 0 {
		t.Fatalf("seq BAD = %d, want 0 (rolled back)", v)
	}
}

func TestAtomicErrorRollsBack(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	sentinel := errors.New("nope")
	err := s.Atomic(ctx, func(t store.Tx) error {
		if _, err := t.NextID("R"); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
	if v := seqValue(t, s.rdb, "R"); v != 0 {
		t.Fatalf("seq R = %d, want 0", v)
	}
	var id string
	if err := s.Atomic(ctx, func(t store.Tx) (err error) { id, err = t.NextID("R"); return }); err != nil {
		t.Fatal(err)
	}
	if id != "R-1" {
		t.Fatalf("id = %s, want R-1", id)
	}
}

func TestPanicRecovery(t *testing.T) {
	s := openTest(t)
	release := blockWriter(t, s)

	res := enqueueInOrder(t, s,
		func(t store.Tx) error { _, err := t.NextID("P"); return err },
		func(t store.Tx) error {
			if _, err := t.NextID("P"); err != nil {
				return err
			}
			panic("kaboom")
		},
		func(t store.Tx) error { _, err := t.NextID("P"); return err },
	)
	release()

	if err := <-res[0]; err != nil {
		t.Fatalf("op1: %v", err)
	}
	err := <-res[1]
	if err == nil || !strings.Contains(err.Error(), "panic") || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("panicking op err = %v, want panic error", err)
	}
	if err := <-res[2]; err != nil {
		t.Fatalf("op3: %v", err)
	}
	if v := seqValue(t, s.rdb, "P"); v != 2 {
		t.Fatalf("seq P = %d, want 2 (panicking op rolled back)", v)
	}

	var id string
	if err := s.Atomic(context.Background(), func(t store.Tx) (err error) { id, err = t.NextID("P"); return }); err != nil {
		t.Fatal(err)
	}
	if id != "P-3" {
		t.Fatalf("id = %s, want P-3", id)
	}
}

func TestNextIDConcurrentUnique(t *testing.T) {
	s := openTest(t)
	const workers, per = 32, 25
	prefixes := []string{"T", "Q"}

	var mu sync.Mutex
	seen := map[string]bool{}
	var wg sync.WaitGroup
	errc := make(chan error, workers*per)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				err := s.Atomic(context.Background(), func(t store.Tx) error {
					for _, p := range prefixes {
						id, err := t.NextID(p)
						if err != nil {
							return err
						}
						mu.Lock()
						dup := seen[id]
						seen[id] = true
						mu.Unlock()
						if dup {
							return fmt.Errorf("duplicate id %s", id)
						}
					}
					return nil
				})
				if err != nil {
					errc <- err
					return
				}
			}
		}()
	}

	stop := make(chan struct{})
	var rwg sync.WaitGroup
	for r := 0; r < 4; r++ {
		rwg.Add(1)
		go func() {
			defer rwg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				var n int64
				_ = s.rdb.QueryRow(`SELECT COALESCE(MAX(n),0) FROM seq`).Scan(&n)
			}
		}()
	}
	wg.Wait()
	close(stop)
	rwg.Wait()
	close(errc)
	for err := range errc {
		t.Fatal(err)
	}

	const total = workers * per
	for _, p := range prefixes {
		for i := 1; i <= total; i++ {
			if id := fmt.Sprintf("%s-%d", p, i); !seen[id] {
				t.Fatalf("missing %s", id)
			}
		}
		if v := seqValue(t, s.rdb, p); v != total {
			t.Fatalf("seq %s = %d, want %d", p, v, total)
		}
	}
	if len(seen) != total*len(prefixes) {
		t.Fatalf("got %d ids, want %d", len(seen), total*len(prefixes))
	}
}

func TestBackupOpenable(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := s.Atomic(ctx, func(t store.Tx) error { _, err := t.NextID("B"); return err }); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "backups", "b.db")
	if err := s.Backup(ctx, path); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() == 0 {
		t.Fatalf("backup file: %v (size %v)", err, fi)
	}
	if err := s.Backup(ctx, path); err == nil {
		t.Fatal("backup over an existing file should fail")
	}

	if err := s.Atomic(ctx, func(t store.Tx) error { _, err := t.NextID("B"); return err }); err != nil {
		t.Fatal(err)
	}

	b, err := Open(path)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer func() { _ = b.Close() }()
	if v := seqValue(t, b.rdb, "B"); v != 5 {
		t.Fatalf("backup seq B = %d, want 5", v)
	}
	var ok string
	if err := b.rdb.QueryRow(`PRAGMA integrity_check`).Scan(&ok); err != nil || ok != "ok" {
		t.Fatalf("integrity_check = %q, %v", ok, err)
	}
	var id string
	if err := b.Atomic(ctx, func(t store.Tx) (err error) { id, err = t.NextID("B"); return }); err != nil {
		t.Fatal(err)
	}
	if id != "B-6" {
		t.Fatalf("id in backup = %s, want B-6", id)
	}
}

func TestPragmas(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Atomic(context.Background(), func(t store.Tx) error { _, err := t.NextID("W"); return err }); err != nil {
		t.Fatal(err)
	}
	for name, db := range map[string]*sql.DB{"writer": s.wdb, "reader": s.rdb} {
		var mode string
		if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if mode != "wal" {
			t.Fatalf("%s journal_mode = %q, want wal", name, mode)
		}
		var syncMode, fk, busy int
		if err := db.QueryRow(`PRAGMA synchronous`).Scan(&syncMode); err != nil {
			t.Fatal(err)
		}
		if syncMode != 1 {
			t.Fatalf("%s synchronous = %d, want 1 (NORMAL)", name, syncMode)
		}
		if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if fk != 1 {
			t.Fatalf("%s foreign_keys = %d, want 1", name, fk)
		}
		if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busy); err != nil {
			t.Fatal(err)
		}
		if busy <= 0 {
			t.Fatalf("%s busy_timeout = %d, want > 0", name, busy)
		}
	}
	if _, err := os.Stat(dir + "/t.db-wal"); err != nil {
		t.Fatalf("wal file: %v", err)
	}
}

func TestAtomicContextAndClose(t *testing.T) {
	s, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = s.Atomic(ctx, func(t store.Tx) error { _, err := t.NextID("C"); return err })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context: err = %v, want context.Canceled", err)
	}
	if v := seqValue(t, s.rdb, "C"); v != 0 {
		t.Fatalf("seq C = %d, want 0 (op must not run)", v)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	err = s.Atomic(context.Background(), func(store.Tx) error { return nil })
	if err == nil {
		t.Fatal("Atomic after Close: want error")
	}
}

func TestOpenPathWithURISpecialChars(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a#b?c%20d.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database not created at %s: %v", path, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a")); !os.IsNotExist(err) {
		t.Fatalf("database truncated at '#': stat err %v", err)
	}
}
