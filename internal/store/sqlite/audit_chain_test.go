package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/audit"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

func chainWrite(t *testing.T, s *Store, from, n int) {
	t.Helper()
	ts := time.Date(2026, 11, 20, 21, 0, 0, 0, time.UTC)
	for i := from; i < from+n; i++ {
		err := s.Atomic(context.Background(), func(tx store.Tx) error {
			return tx.InsertAudit(&model.AuditEvent{TS: ts.Add(time.Duration(i) * time.Second), AgentID: "w-1", Role: "worker",
				Tool: "update_status", TaskID: auditStrp("T-1"), Before: map[string]any{"i": i}, After: map[string]any{"i": i + 1}})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func chainVerify(t *testing.T, s *Store) model.AuditCheck {
	t.Helper()
	var c model.AuditCheck
	if err := s.View(context.Background(), func(tx store.Tx) (e error) { c, e = tx.VerifyAudit(); return }); err != nil {
		t.Fatal(err)
	}
	return c
}

func chainOpen(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func rawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func rawExec(t *testing.T, db *sql.DB, qs ...string) {
	t.Helper()
	for _, q := range qs {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func TestAuditChainSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	s := chainOpen(t, path)
	chainWrite(t, s, 0, 5)
	first := chainVerify(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s = chainOpen(t, path)
	if got := chainVerify(t, s); got != first {
		t.Fatalf("reopen changed chain: %+v vs %+v", got, first)
	}
	chainWrite(t, s, 5, 5)
	got := chainVerify(t, s)
	if !got.OK || got.Events != 10 || got.Head.ID != 10 {
		t.Fatalf("after restart: %+v", got)
	}
}

func TestAuditChainDetectsTampering(t *testing.T) {
	const dropTriggers = `DROP TRIGGER audit_no_update; DROP TRIGGER audit_no_delete`
	cases := []struct {
		name   string
		sql    []string
		broken int64
	}{
		{"edit field", []string{`UPDATE audit SET note = 'forged' WHERE id = 4`}, 4},
		{"edit before json", []string{`UPDATE audit SET before = '{"i":99}' WHERE id = 2`}, 2},
		{"edit timestamp", []string{`UPDATE audit SET ts = ts + 1 WHERE id = 6`}, 6},
		{"delete middle", []string{`DELETE FROM audit WHERE id = 3`}, 4},
		{"delete first", []string{`DELETE FROM audit WHERE id = 1`}, 2},
		{"swap ids", []string{`UPDATE audit SET id = 100 WHERE id = 2`, `UPDATE audit SET id = 2 WHERE id = 3`, `UPDATE audit SET id = 3 WHERE id = 100`}, 2},
		{"forged hash", []string{`UPDATE audit SET hash = '` + hex.EncodeToString(make([]byte, 32)) + `' WHERE id = 5`}, 5},
		{"append without hash", []string{`INSERT INTO audit(ts, agent_id, role, tool) VALUES(1, 'x', 'owner', 'pause_all')`}, 9},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "t.db")
			s := chainOpen(t, path)
			chainWrite(t, s, 0, 8)
			if got := chainVerify(t, s); !got.OK {
				t.Fatalf("clean chain: %+v", got)
			}
			raw := rawDB(t, path)
			rawExec(t, raw, dropTriggers)
			rawExec(t, raw, c.sql...)
			got := chainVerify(t, s)
			if got.OK || got.BrokenID != c.broken {
				t.Fatalf("got %+v, want broken at %d", got, c.broken)
			}
		})
	}
}

func TestAuditChainDetectsRehashedRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	s := chainOpen(t, path)
	chainWrite(t, s, 0, 6)
	raw := rawDB(t, path)
	var prev string
	if err := raw.QueryRow(`SELECT hash FROM audit WHERE id = 2`).Scan(&prev); err != nil {
		t.Fatal(err)
	}
	var r audit.Record
	err := raw.QueryRow(`SELECT id, ts, agent_id, role, tool, task_id, before, after, note FROM audit WHERE id = 3`).
		Scan(&r.ID, &r.TS, &r.AgentID, &r.Role, &r.Tool, &r.TaskID, &r.Before, &r.After, &r.Note)
	if err != nil {
		t.Fatal(err)
	}
	r.Note = "forged"
	h, err := audit.Link(prev, r)
	if err != nil {
		t.Fatal(err)
	}
	rawExec(t, raw, `DROP TRIGGER audit_no_update`,
		fmt.Sprintf(`UPDATE audit SET note = 'forged', hash = '%s' WHERE id = 3`, h))
	if got := chainVerify(t, s); got.OK || got.BrokenID != 4 || got.Head.ID != 3 {
		t.Fatalf("got %+v, want broken at 4", got)
	}
}

func TestOpenChainsExistingAudit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	rawExec(t, old, `CREATE TABLE audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL, agent_id TEXT NOT NULL, role TEXT NOT NULL, tool TEXT NOT NULL,
  task_id TEXT NOT NULL DEFAULT '', before TEXT NOT NULL DEFAULT '', after TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT ''
);
CREATE TRIGGER audit_no_update BEFORE UPDATE ON audit BEGIN SELECT RAISE(ABORT, 'audit is append-only'); END;
CREATE TRIGGER audit_no_delete BEFORE DELETE ON audit BEGIN SELECT RAISE(ABORT, 'audit is append-only'); END;
INSERT INTO audit(ts, agent_id, role, tool, task_id, before, after, note) VALUES
  (1000, 'owner', 'owner', 'approve_plan', '', '', '{"plan_id":"P-1"}', ''),
  (2000, 'w-1', 'worker', 'claim_task', 'T-1', '{"status":"assigned"}', '{"status":"in_progress"}', 'go'),
  (3000, 'w-1', 'worker', 'submit', 'T-1', '{"status":"in_progress"}', '{"status":"submitted"}', '')`)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s := chainOpen(t, path)
	got := chainVerify(t, s)
	if !got.OK || got.Events != 3 || got.Head.ID != 3 {
		t.Fatalf("migrated chain: %+v", got)
	}
	sum := sha256.Sum256([]byte(`{"id":1,"ts":1000,"agent_id":"owner","role":"owner","tool":"approve_plan","task_id":"","before":null,"after":{"plan_id":"P-1"},"note":""}`))
	var h1 string
	if err := s.View(context.Background(), func(tx store.Tx) error {
		h, err := tx.AuditHead(1)
		h1 = h.Hash
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if want := hex.EncodeToString(sum[:]); h1 != want {
		t.Fatalf("first link %s, want %s", h1, want)
	}

	chainWrite(t, s, 0, 2)
	if got := chainVerify(t, s); !got.OK || got.Events != 5 || got.Head.ID != 5 {
		t.Fatalf("after append: %+v", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s = chainOpen(t, path)
	if got := chainVerify(t, s); !got.OK || got.Events != 5 {
		t.Fatalf("reopened: %+v", got)
	}
	if _, err := rawDB(t, path).Exec(`UPDATE audit SET note = 'x' WHERE id = 1`); err == nil {
		t.Fatal("update trigger missing after migration")
	}
}
