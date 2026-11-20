package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

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

func TestOpenMigratesTasksWithoutPriorityAndComponent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = old.Exec(`CREATE TABLE tasks (
  id TEXT PRIMARY KEY, brief_id TEXT NOT NULL, plan_id TEXT NOT NULL, title TEXT NOT NULL, spec TEXT NOT NULL,
  acceptance TEXT NOT NULL, repo TEXT NOT NULL, branch TEXT NOT NULL, role TEXT NOT NULL,
  assignee TEXT NOT NULL DEFAULT '', reviewer TEXT NOT NULL DEFAULT '', depends_on TEXT NOT NULL,
  status TEXT NOT NULL, progress_pct INTEGER NOT NULL DEFAULT 0,
  last_heartbeat_at INTEGER NOT NULL DEFAULT 0, lease_expires_at INTEGER NOT NULL DEFAULT 0, claimed_at INTEGER NOT NULL DEFAULT 0,
  pr_url TEXT NOT NULL DEFAULT '', max_minutes INTEGER NOT NULL DEFAULT 0, max_cost_eur REAL NOT NULL DEFAULT 0,
  cost_used REAL NOT NULL DEFAULT 0, minutes_used INTEGER NOT NULL DEFAULT 0, review_rounds INTEGER NOT NULL DEFAULT 0,
  tests_passed INTEGER NOT NULL DEFAULT -1, tests_failed INTEGER NOT NULL DEFAULT 0, tests_at INTEGER NOT NULL DEFAULT 0,
  blocked_reason TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
INSERT INTO tasks(id, brief_id, plan_id, title, spec, acceptance, repo, branch, role, depends_on, status, created_at, updated_at)
VALUES ('T-1','B-1','','Old','spec','[]','o/r','sub/t-1-old','worker','[]','ready',1000,1000)`)
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
	comp := "C-2"
	err = s.Atomic(context.Background(), func(tx store.Tx) error {
		got, err := tx.GetTask("T-1")
		if err != nil {
			return err
		}
		if got.Priority != 0 || got.ComponentID != nil {
			t.Fatalf("migrated task: priority=%d component=%v", got.Priority, got.ComponentID)
		}
		got.Priority, got.ComponentID = 5, &comp
		if err := tx.UpdateTask(got); err != nil {
			return err
		}
		fresh := *got
		fresh.ID, fresh.Priority, fresh.ComponentID = "T-2", 0, nil
		return tx.InsertTask(&fresh)
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.View(context.Background(), func(tx store.Tx) error {
		got, err := tx.GetTask("T-1")
		if err != nil {
			return err
		}
		if got.Priority != 5 || got.ComponentID == nil || *got.ComponentID != comp {
			t.Fatalf("round trip: priority=%d component=%v", got.Priority, got.ComponentID)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestListTasksOrdersByPriorityThenID(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	prio := map[string]int{"T-1": 0, "T-2": 3, "T-3": 0, "T-9": 3, "T-10": 7}
	err = s.Atomic(context.Background(), func(tx store.Tx) error {
		for id, p := range prio {
			x := &model.Task{ID: id, BriefID: "B-1", Title: id, Spec: "s", Repo: "o/r", Role: model.RoleWorker,
				Status: model.StatusReady, Priority: p, CreatedAt: time.UnixMilli(1000), UpdatedAt: time.UnixMilli(1000)}
			if err := tx.InsertTask(x); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	err = s.View(context.Background(), func(tx store.Tx) error {
		ts, err := tx.ListTasks(store.TaskFilter{BriefID: "B-1"})
		for _, x := range ts {
			got = append(got, x.ID)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"T-10", "T-2", "T-9", "T-1", "T-3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v want %v", got, want)
	}
}
