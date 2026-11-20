package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

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

	autoDone := func() bool {
		t.Helper()
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		var got bool
		err = s.View(context.Background(), func(tx store.Tx) error {
			b, err := tx.GetBrief("B-1")
			got = b != nil && b.AutoDoneOnMerge
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
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
	if autoDone() {
		t.Fatal("existing brief should default to auto_done_on_merge=false")
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
		b.AutoDoneOnMerge = true
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
	if !autoDone() {
		t.Fatal("reopening must not reset auto_done_on_merge")
	}
}
