package faultstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/faultstore"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

func TestFaultsRollBackOrCommitAsDeclared(t *testing.T) {
	for _, tc := range []struct {
		name      string
		f         faultstore.Faults
		want      error
		committed bool
	}{
		{"error", faultstore.Faults{FailAt: []int64{2}}, faultstore.ErrInjected, false},
		{"cancel", faultstore.Faults{CancelRate: 1}, context.Canceled, false},
		{"panic", faultstore.Faults{PanicRate: 1}, nil, false},
		{"rollback", faultstore.Faults{RollbackRate: 1}, faultstore.ErrInjected, false},
		{"commit", faultstore.Faults{CommitRate: 1}, faultstore.ErrUnknownOutcome, true},
		{"delay", faultstore.Faults{DelayRate: 1}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner, err := sqlite.Open(t.TempDir() + "/f.db")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = inner.Close() }()
			fs := faultstore.New(inner, 1, tc.f)
			err = fs.Atomic(context.Background(), func(tx store.Tx) error {
				if err := tx.SetKV("a", "1"); err != nil {
					return err
				}
				return tx.SetKV("b", "1")
			})
			switch {
			case tc.want != nil && !errors.Is(err, tc.want):
				t.Fatalf("Atomic returned %v, want %v", err, tc.want)
			case tc.want == nil && tc.committed && err != nil:
				t.Fatalf("Atomic returned %v", err)
			case tc.want == nil && !tc.committed && err == nil:
				t.Fatal("Atomic succeeded")
			}
			fs.Set(faultstore.Faults{})
			var got int
			if err := fs.View(context.Background(), func(tx store.Tx) error {
				for _, k := range []string{"a", "b"} {
					if _, ok, err := tx.GetKV(k); err != nil {
						return err
					} else if ok {
						got++
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if want := map[bool]int{true: 2, false: 0}[tc.committed]; got != want {
				t.Fatalf("%d of 2 writes visible, want %d", got, want)
			}
		})
	}
}
