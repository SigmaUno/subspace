package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

func TestOpenAddsLeaseExpiriesToExistingTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Atomic(context.Background(), func(tx store.Tx) error {
		return tx.InsertTask(&model.Task{ID: "T-1", BriefID: "B-1", Status: model.StatusReady, Role: model.RoleWorker})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`ALTER TABLE tasks DROP COLUMN lease_expiries`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	err = s.Atomic(context.Background(), func(tx store.Tx) error {
		x, err := tx.GetTask("T-1")
		if err != nil {
			return err
		}
		if x.LeaseExpiries != 0 {
			t.Fatalf("lease_expiries %d", x.LeaseExpiries)
		}
		x.LeaseExpiries = 2
		return tx.UpdateTask(x)
	})
	if err != nil {
		t.Fatal(err)
	}
}
