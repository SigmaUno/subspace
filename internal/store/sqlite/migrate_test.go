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

func TestOpenMigratesPlansWithoutWarnings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = old.Exec(`CREATE TABLE plans (
  id TEXT PRIMARY KEY, brief_id TEXT NOT NULL, status TEXT NOT NULL, proposed_by TEXT NOT NULL,
  note TEXT, created_at INTEGER NOT NULL, decided_at INTEGER NOT NULL DEFAULT 0
);
INSERT INTO plans(id, brief_id, status, proposed_by, created_at) VALUES ('P-1','B-1','proposed','rex',1000)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	findings := []model.PlanFinding{{Severity: "warning", Rule: "no_tester", Message: "m"}}
	err = s.Atomic(context.Background(), func(tx store.Tx) error {
		p, err := tx.GetPlan("P-1")
		if err != nil {
			return err
		}
		if p.Warnings != nil {
			t.Fatalf("existing plan should have no warnings: %v", p.Warnings)
		}
		p.Warnings = findings
		return tx.UpdatePlan(p)
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.View(context.Background(), func(tx store.Tx) error {
		p, err := tx.GetPlan("P-1")
		if err == nil && (len(p.Warnings) != 1 || p.Warnings[0] != findings[0]) {
			t.Fatalf("warnings not persisted: %v", p.Warnings)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
