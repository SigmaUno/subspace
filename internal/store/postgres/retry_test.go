package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/postgres"
)

type sqlTx interface {
	Exec(q string, args ...any) (sql.Result, error)
}

func raise(tx store.Tx, code string) error {
	_, err := tx.(sqlTx).Exec(fmt.Sprintf(`DO $$ BEGIN RAISE EXCEPTION 'injected' USING ERRCODE = '%s'; END $$`, code))
	return err
}

func TestPGAtomicRetriesSerializationAndDeadlock(t *testing.T) {
	for _, code := range []string{"40001", "40P01"} {
		t.Run(code, func(t *testing.T) {
			s, _ := openPG(t)
			var attempts int
			err := s.Atomic(context.Background(), func(tx store.Tx) error {
				attempts++
				if err := tx.SetKV("k", "v"); err != nil {
					return err
				}
				if attempts < 3 {
					return raise(tx, code)
				}
				return nil
			})
			if err != nil || attempts != 3 {
				t.Fatalf("err=%v attempts=%d", err, attempts)
			}
			read(t, s, func(tx store.Tx) error {
				v, ok, err := tx.GetKV("k")
				if err != nil || !ok || v != "v" {
					t.Fatalf("kv: %q %v %v", v, ok, err)
				}
				return nil
			})
		})
	}
}

func TestPGAtomicDoesNotRetryOtherErrors(t *testing.T) {
	s, _ := openPG(t)
	var attempts int
	err := s.Atomic(context.Background(), func(tx store.Tx) error {
		attempts++
		return raise(tx, "23514")
	})
	if err == nil || attempts != 1 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}

func TestPGAtomicGivesUpAfterRepeatedConflicts(t *testing.T) {
	s, _ := openPG(t)
	var attempts int
	err := s.Atomic(context.Background(), func(tx store.Tx) error {
		attempts++
		return raise(tx, "40001")
	})
	if err == nil || attempts < 2 || attempts > 10 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}

func TestPGRetriedBatchAppliesEachWriteOnce(t *testing.T) {
	s, _ := openPG(t)
	const n = 64
	var (
		wg     sync.WaitGroup
		failed atomic.Int32
		mu     sync.Mutex
		ids    = map[string]bool{}
		errs   []error
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			first := true
			var id string
			err := s.Atomic(context.Background(), func(tx store.Tx) error {
				var err error
				if id, err = tx.NextID("R"); err != nil {
					return err
				}
				if i%8 == 0 && first {
					first = false
					failed.Add(1)
					return raise(tx, "40P01")
				}
				return nil
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			ids[id] = true
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	if failed.Load() == 0 {
		t.Fatal("no conflicts injected")
	}
	var next string
	write(t, s, func(tx store.Tx) (err error) { next, err = tx.NextID("R"); return })
	if next != fmt.Sprintf("R-%d", n+1) || len(ids) != n {
		t.Fatalf("ids=%d next=%s", len(ids), next)
	}
}

func TestPGAtomicNeverReturnsWhileWriteOpStillRuns(t *testing.T) {
	dsn, _ := pgSchemaDSN(t)
	s, err := postgres.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	var (
		wg      sync.WaitGroup
		written int
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := s.Atomic(context.Background(), func(tx store.Tx) error {
			close(started)
			time.Sleep(100 * time.Millisecond)
			written = 1
			return tx.SetKV("k", "v")
		})
		if err == nil && written != 1 {
			t.Error("write reported success without running")
		}
		_ = written
	}()
	<-started
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}
