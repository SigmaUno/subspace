package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

func TestOpenMigratesBriefsWithoutCrossReview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = old.Exec(`CREATE TABLE briefs (
  id TEXT PRIMARY KEY, title TEXT NOT NULL, body TEXT NOT NULL, repos TEXT NOT NULL, status TEXT NOT NULL,
  max_cost_eur REAL NOT NULL, max_parallel INTEGER NOT NULL, allowed_providers TEXT NOT NULL,
  plan_status TEXT NOT NULL, created_at INTEGER NOT NULL
);
INSERT INTO briefs VALUES ('B-1','Old','body','[]','open',5,2,'[]','none',1000)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	crossReview := func() bool {
		t.Helper()
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		var got bool
		err = s.View(context.Background(), func(tx store.Tx) error {
			b, err := tx.GetBrief("B-1")
			got = b != nil && b.CrossReview
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	if !crossReview() {
		t.Fatal("existing brief should default to cross_review=true")
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Atomic(context.Background(), func(tx store.Tx) error {
		b, err := tx.GetBrief("B-1")
		if err != nil {
			return err
		}
		b.CrossReview = false
		return tx.UpdateBrief(b)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if crossReview() {
		t.Fatal("reopening must not reset cross_review")
	}
}

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
