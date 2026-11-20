package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

func seed(t *testing.T, st *sqlite.Store) {
	t.Helper()
	now := time.Date(2026, 11, 20, 21, 0, 0, 0, time.UTC)
	err := st.Atomic(context.Background(), func(tx store.Tx) error {
		if err := tx.InsertBrief(&model.Brief{
			ID: "B-1", Title: "brief", Body: "body", Repos: []string{"o/r"}, Status: "open",
			Budget: model.BriefBudget{MaxCostEUR: 50, MaxParallelAgent: 3}, AllowedProviders: []string{"anthropic"},
			PlanStatus: "approved", CreatedAt: now,
		}); err != nil {
			return err
		}
		if err := tx.InsertPlan(&model.Plan{ID: "P-1", BriefID: "B-1", Status: "approved", ProposedBy: "rex", CreatedAt: now}); err != nil {
			return err
		}
		if err := tx.InsertAgent(&model.Agent{
			ID: "w-1", Role: "worker", Kind: "claude-code", Model: "m", Provider: "anthropic",
			Status: "active", CreatedAt: now, TokenHash: "hash-w-1",
		}); err != nil {
			return err
		}
		if err := tx.InsertTask(&model.Task{
			ID: "T-1", BriefID: "B-1", PlanID: "P-1", Title: "t", Spec: "s", AcceptanceCriteria: []string{"a"},
			Repo: "o/r", Branch: "sub/t-1", Role: "worker", DependsOn: []string{}, Status: "ready",
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		if err := tx.InsertQuestion(&model.Question{
			ID: "Q-1", AgentID: "w-1", Question: "?", Options: []string{"a", "b"}, Status: "open", CreatedAt: now,
		}); err != nil {
			return err
		}
		if err := tx.InsertSpawn(&model.Spawn{
			ID: "S-1", BriefID: "B-1", AgentID: "w-1", Kind: "claude-code", Model: "m", Provider: "anthropic",
			Status: "spawned", Spawner: "noop", RequestedBy: "rex", CreatedAt: now,
		}); err != nil {
			return err
		}
		return tx.SetKV("charter", "be nice")
	})
	if err != nil {
		t.Fatal(err)
	}
}

func write(st *sqlite.Store, i int) error {
	return st.Atomic(context.Background(), func(tx store.Tx) error {
		ts := time.Date(2026, 11, 20, 21, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second)
		if _, err := tx.NextID("M"); err != nil {
			return err
		}
		if err := tx.InsertMessage(&model.Message{
			ThreadKind: "task", ThreadID: "T-1", AuthorID: "w-1", AuthorRole: "worker",
			Kind: "note", Body: fmt.Sprintf("note %d", i), CreatedAt: ts,
		}); err != nil {
			return err
		}
		return tx.InsertAudit(&model.AuditEvent{
			TS: ts, AgentID: "w-1", Role: "worker", Tool: "add_note",
			After: map[string]any{"i": float64(i)},
		})
	})
}

func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func tableCounts(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	counts := map[string]int{}
	for _, n := range names {
		var c int
		if err := db.QueryRow("SELECT count(*) FROM " + n).Scan(&c); err != nil { //nolint:gosec // table names come from sqlite_master
			t.Fatal(err)
		}
		counts[n] = c
	}
	return counts
}

func auditRows(t *testing.T, db *sql.DB) [][]any {
	t.Helper()
	rows, err := db.Query("SELECT id, ts, agent_id, role, tool, task_id, before, after, note FROM audit ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out [][]any
	for rows.Next() {
		var id, ts int64
		var a, r, tool, task, before, after, note string
		if err := rows.Scan(&id, &ts, &a, &r, &tool, &task, &before, &after, &note); err != nil {
			t.Fatal(err)
		}
		out = append(out, []any{id, ts, a, r, tool, task, before, after, note})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBackupWhileWritingThenRestore(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "live", "subspace.db")
	st, err := sqlite.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	seed(t, st)

	var n atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if err := write(st, int(n.Add(1))); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	for n.Load() < 50 {
		time.Sleep(time.Millisecond)
	}

	bak := filepath.Join(dir, "backups", "b.db")
	var out bytes.Buffer
	if err := runBackup([]string{"--db", src, "--out", bak}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), bak) {
		t.Fatalf("backup output %q", out.String())
	}
	if err := runBackup([]string{"--db", src, "--out", bak}, &out); err == nil {
		t.Fatal("backup over an existing file must fail")
	}
	floor := n.Load()
	for n.Load() < floor+20 {
		time.Sleep(time.Millisecond)
	}
	close(stop)
	wg.Wait()

	liveAudit := auditRows(t, openRaw(t, src))
	bakDB := openRaw(t, bak)
	want := tableCounts(t, bakDB)
	wantAudit := auditRows(t, bakDB)
	if want["audit"] < 50 || want["audit"] >= len(liveAudit) {
		t.Fatalf("backup audit rows %d, live %d: backup is not a mid-write snapshot", want["audit"], len(liveAudit))
	}
	if !reflect.DeepEqual(liveAudit[:len(wantAudit)], wantAudit) {
		t.Fatal("backup audit log is not a prefix of the live audit log")
	}

	if err := runRestore([]string{bak, "--db", src}, &out); err == nil || !strings.Contains(err.Error(), "in use") && !strings.Contains(err.Error(), "pending") {
		t.Fatalf("restore over a live database must be refused, got %v", err)
	}
	if got := auditRows(t, openRaw(t, src)); len(got) != len(liveAudit) {
		t.Fatal("refused restore modified the live database")
	}

	dst := filepath.Join(dir, "new", "subspace.db")
	if err := runRestore([]string{bak, "--db", dst}, &out); err != nil {
		t.Fatal(err)
	}
	restored := openRaw(t, dst)
	if got := tableCounts(t, restored); !reflect.DeepEqual(got, want) {
		t.Fatalf("row counts differ: restored %v, backup %v", got, want)
	}
	if !reflect.DeepEqual(auditRows(t, restored), wantAudit) {
		t.Fatal("restored audit log differs from the backup")
	}

	re, err := sqlite.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	if err := write(re, 1_000_000); err != nil {
		t.Fatal(err)
	}
	if err := re.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreRefusals(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.db")
	st, err := sqlite.Open(good)
	if err != nil {
		t.Fatal(err)
	}
	seed(t, st)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "dst.db")
	if err := os.WriteFile(dst, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runRestore([]string{good, "--db", dst}, &out); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("overwrite without --force must be refused, got %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "existing" { //nolint:gosec // test path
		t.Fatal("refused restore modified the target")
	}
	if err := runRestore([]string{good, "--db", dst, "--force"}, &out); err != nil {
		t.Fatal(err)
	}
	if got := tableCounts(t, openRaw(t, dst)); got["briefs"] != 1 || got["tasks"] != 1 {
		t.Fatalf("restored counts %v", got)
	}

	garbage := filepath.Join(dir, "garbage.db")
	if err := os.WriteFile(garbage, bytes.Repeat([]byte("x"), 8192), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runRestore([]string{garbage, "--db", filepath.Join(dir, "g.db")}, &out); err == nil {
		t.Fatal("corrupt backup must be refused")
	}

	foreign := filepath.Join(dir, "foreign.db")
	fdb, err := sql.Open("sqlite", "file:"+foreign)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fdb.Exec("CREATE TABLE other (x INTEGER)"); err != nil {
		t.Fatal(err)
	}
	_ = fdb.Close()
	if err := runRestore([]string{foreign, "--db", filepath.Join(dir, "f.db")}, &out); err == nil || !strings.Contains(err.Error(), "missing table") {
		t.Fatalf("foreign database must be refused, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "f.db")); err == nil {
		t.Fatal("refused restore created the target")
	}
}
