package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/tokens"
)

func TestPGAuditAppendOnly(t *testing.T) {
	s, db := openPG(t)
	write(t, s, func(tx store.Tx) error {
		return tx.InsertAudit(&model.AuditEvent{TS: time.Now(), AgentID: "a", Role: "worker", Tool: "x"})
	})
	for _, stmt := range []string{`UPDATE audit SET note='tampered'`, `DELETE FROM audit WHERE id=1`, `DELETE FROM audit`, `TRUNCATE audit`} {
		if _, err := db.Exec(stmt); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("%q: want append-only failure, got %v", stmt, err)
		}
	}
}

func TestPGSwallowedStatementErrorIsolated(t *testing.T) {
	s, _ := openPG(t)
	write(t, s, func(tx store.Tx) error { return tx.InsertBrief(testBrief("B-1", time.Now())) })
	var wg sync.WaitGroup
	errs := make([]error, 20)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = s.Atomic(context.Background(), func(tx store.Tx) error {
				if i%2 == 0 {
					_ = tx.InsertBrief(testBrief("B-1", time.Now()))
					return nil
				}
				return tx.SetKV(fmt.Sprintf("k%d", i), "v")
			})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if i%2 == 1 && err != nil {
			t.Fatalf("op %d: %v", i, err)
		}
	}
	read(t, s, func(tx store.Tx) error {
		for i := 1; i < len(errs); i += 2 {
			if _, ok, err := tx.GetKV(fmt.Sprintf("k%d", i)); err != nil || !ok {
				t.Fatalf("k%d missing: %v", i, err)
			}
		}
		return nil
	})
}

func TestPGMultipleInstancesSerialize(t *testing.T) {
	dsn, _ := pgSchemaDSN(t)
	a, b := openPGAt(t, dsn), openPGAt(t, dsn)
	const n = 200
	var wg sync.WaitGroup
	errc := make(chan error, n)
	for i := 0; i < n; i++ {
		s := store.Store(a)
		if i%2 == 1 {
			s = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.Atomic(context.Background(), func(tx store.Tx) error {
				id, err := tx.NextID("M")
				if err != nil {
					return err
				}
				return tx.InsertMessage(&model.Message{ThreadKind: "task", ThreadID: "T-1", AuthorID: "a",
					AuthorRole: "worker", Kind: "note", Body: id, CreatedAt: time.Now()})
			})
			if err != nil {
				errc <- err
			}
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Fatal(err)
	}
	read(t, a, func(tx store.Tx) error {
		msgs, err := tx.ListMessages("task", "T-1", 0, 1000)
		if err != nil {
			return err
		}
		if len(msgs) != n {
			t.Fatalf("messages %d want %d", len(msgs), n)
		}
		for i, m := range msgs {
			if want := fmt.Sprintf("M-%d", i+1); m.Body != want {
				t.Fatalf("message %d body %s want %s: id order must follow commit order", i, m.Body, want)
			}
		}
		return nil
	})
}

func TestPGBackupRestore(t *testing.T) {
	src, _ := openPG(t)
	ctx := context.Background()
	full := taskFull("T-1")
	ev := &model.AuditEvent{TS: time.UnixMilli(1700000000000).UTC(), AgentID: "a", Role: "worker", Tool: "claim_task",
		TaskID: ptr("T-1"), After: map[string]any{"status": "in_progress"}}
	write(t, src, func(tx store.Tx) error {
		if _, err := tx.NextID("T"); err != nil {
			return err
		}
		if err := tx.InsertBrief(testBrief("B-1", time.UnixMilli(5).UTC())); err != nil {
			return err
		}
		if err := tx.InsertTask(full); err != nil {
			return err
		}
		if err := tx.InsertAgent(&model.Agent{ID: "claude-1", Role: "worker", Kind: "claude-code", Model: "m",
			Provider: "anthropic", Status: "active", CreatedAt: time.UnixMilli(7).UTC(), Spawned: true, TokenHash: "h"}); err != nil {
			return err
		}
		for i := 0; i < 3; i++ {
			if err := tx.InsertMessage(&model.Message{ThreadKind: "task", ThreadID: "T-1", AuthorID: "a",
				AuthorRole: "worker", Kind: "note", Body: "b", CreatedAt: time.UnixMilli(9).UTC()}); err != nil {
				return err
			}
		}
		if err := tx.InsertAudit(ev); err != nil {
			return err
		}
		return tx.SetKV("charter", "be nice")
	})
	path := filepath.Join(t.TempDir(), "backups", "b.jsonl")
	if err := src.Backup(ctx, path); err != nil {
		t.Fatal(err)
	}
	if err := src.Backup(ctx, path); err == nil {
		t.Fatal("backup over existing file must fail")
	}

	dst, _ := openPG(t)
	if err := dst.Restore(ctx, path); err != nil {
		t.Fatal(err)
	}
	if err := dst.Restore(ctx, path); err == nil {
		t.Fatal("restore into non-empty store must fail")
	}
	read(t, dst, func(tx store.Tx) error {
		got, err := tx.GetTask("T-1")
		if err != nil {
			return err
		}
		same(t, full, got)
		a, err := tx.GetAgentByTokenHash("h")
		if err != nil {
			return err
		}
		if !a.Spawned || a.AssignedCount != 1 || str(a.CurrentTaskID) != "T-1" {
			t.Fatalf("agent: %+v", a)
		}
		evs, err := tx.ListAudit(store.AuditFilter{})
		if err != nil {
			return err
		}
		same(t, []*model.AuditEvent{ev}, evs)
		if v, ok, err := tx.GetKV("charter"); err != nil || !ok || v != "be nice" {
			t.Fatalf("kv: %q %v %v", v, ok, err)
		}
		return nil
	})
	if id := nextID(t, dst, "T"); id != "T-2" {
		t.Fatalf("seq after restore: %s", id)
	}
	m := &model.Message{ThreadKind: "task", ThreadID: "T-1", AuthorID: "a", AuthorRole: "worker", Kind: "note",
		Body: "new", CreatedAt: time.Now()}
	write(t, dst, func(tx store.Tx) error { return tx.InsertMessage(m) })
	if m.ID != 4 {
		t.Fatalf("message id after restore: %d", m.ID)
	}
	err := dst.Atomic(ctx, func(tx store.Tx) error { return tx.InsertBrief(testBrief("B-1", time.Now())) })
	if !errors.Is(err, store.ErrDuplicate) {
		t.Fatalf("restored brief: %v", err)
	}
}

func TestPGRestoreAdvancesNonIDIdentityColumns(t *testing.T) {
	src, _ := openPG(t)
	ctx := context.Background()
	audit := func(tk *tokens.Token) *model.AuditEvent {
		return &model.AuditEvent{TS: tk.CreatedAt, AgentID: "owner", Role: "owner", Tool: "create_token"}
	}
	for i, name := range []string{"ci", "ops"} {
		tk := &tokens.Token{Name: name, Scope: "owner", CreatedAt: time.UnixMilli(int64(1000 + i)).UTC()}
		if err := src.CreateAPIToken(ctx, tk, "hash-"+name, audit); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "b.jsonl")
	if err := src.Backup(ctx, path); err != nil {
		t.Fatal(err)
	}
	dst, _ := openPG(t)
	if err := dst.Restore(ctx, path); err != nil {
		t.Fatal(err)
	}
	next := &tokens.Token{Name: "new", Scope: "viewer", CreatedAt: time.UnixMilli(2000).UTC()}
	if err := dst.CreateAPIToken(ctx, next, "hash-new", audit); err != nil {
		t.Fatal(err)
	}
	if next.ID != "K-3" {
		t.Fatalf("token id after restore: %s", next.ID)
	}
	all, err := dst.ListAPITokens(ctx)
	if err != nil || len(all) != 3 || all[0].Name != "ci" {
		t.Fatalf("tokens: %v %v", all, err)
	}
}

func TestPGMigrationBackfillsTaskOrder(t *testing.T) {
	dsn, raw := pgSchemaDSN(t)
	first := openPGAt(t, dsn)
	write(t, first, func(tx store.Tx) error {
		for _, id := range []string{"T-10", "T-2", "T-1", "T-100"} {
			if err := tx.InsertTask(taskMin(id, "B-1")); err != nil {
				return err
			}
		}
		return nil
	})
	if _, err := raw.Exec(`UPDATE tasks SET id_num = 0`); err != nil {
		t.Fatal(err)
	}
	reopened := openPGAt(t, dsn)
	read(t, reopened, func(tx store.Tx) error {
		got, err := tx.ListTasks(store.TaskFilter{})
		if err != nil {
			return err
		}
		var ids []string
		for _, x := range got {
			ids = append(ids, x.ID)
		}
		same(t, []string{"T-1", "T-2", "T-10", "T-100"}, ids)
		after, err := tx.ListTasks(store.TaskFilter{AfterID: "T-2"})
		if err != nil || len(after) != 2 || after[0].ID != "T-10" {
			t.Fatalf("after T-2: %v %v", after, err)
		}
		return nil
	})
}
