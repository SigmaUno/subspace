package sqlite

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

func auditOpenTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func auditStrp(s string) *string { return &s }

func TestAuditInsertList(t *testing.T) {
	s := auditOpenTest(t)
	ctx := context.Background()
	ts := time.Date(2026, 11, 20, 21, 4, 5, 123_000_000, time.UTC)

	full := &model.AuditEvent{
		TS: ts, AgentID: "claude-1", Role: "worker", Tool: "claim_task", TaskID: auditStrp("T-3"),
		Before: map[string]any{"status": "assigned"}, After: map[string]any{"status": "in_progress", "n": float64(2)},
		Note: auditStrp("hello"),
	}
	minimal := &model.AuditEvent{TS: ts.Add(time.Second), AgentID: "owner", Role: "owner", Tool: "pause_all"}
	third := &model.AuditEvent{TS: ts.Add(2 * time.Second), AgentID: "claude-1", Role: "worker", Tool: "submit", TaskID: auditStrp("T-4")}

	err := s.Atomic(ctx, func(tx store.Tx) error {
		for _, e := range []*model.AuditEvent{full, minimal, third} {
			if err := tx.InsertAudit(e); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if full.ID == 0 || minimal.ID <= full.ID || third.ID <= minimal.ID {
		t.Fatalf("ids not assigned ascending: %d %d %d", full.ID, minimal.ID, third.ID)
	}

	var all []*model.AuditEvent
	err = s.View(ctx, func(tx store.Tx) (e error) { all, e = tx.ListAudit(store.AuditFilter{}); return })
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d events", len(all))
	}
	if !reflect.DeepEqual(all[0], full) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", all[0], full)
	}
	if all[1].TaskID != nil || all[1].Note != nil || all[1].Before != nil || all[1].After != nil {
		t.Fatalf("nil fields not preserved: %+v", all[1])
	}
	if !all[1].TS.Equal(minimal.TS) {
		t.Fatalf("ts: %v vs %v", all[1].TS, minimal.TS)
	}

	list := func(f store.AuditFilter) []*model.AuditEvent {
		var out []*model.AuditEvent
		if err := s.View(ctx, func(tx store.Tx) (e error) { out, e = tx.ListAudit(f); return }); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := list(store.AuditFilter{AgentID: "claude-1"}); len(got) != 2 {
		t.Fatalf("agent filter: %d", len(got))
	}
	if got := list(store.AuditFilter{TaskID: "T-4"}); len(got) != 1 || got[0].Tool != "submit" {
		t.Fatalf("task filter: %+v", got)
	}
	if got := list(store.AuditFilter{Tool: "pause_all"}); len(got) != 1 || got[0].AgentID != "owner" {
		t.Fatalf("tool filter: %+v", got)
	}
	if got := list(store.AuditFilter{SinceID: full.ID}); len(got) != 2 || got[0].ID != minimal.ID {
		t.Fatalf("since filter: %+v", got)
	}
	if got := list(store.AuditFilter{Limit: 2}); len(got) != 2 || got[1].ID != minimal.ID {
		t.Fatalf("limit: %+v", got)
	}
	if got := list(store.AuditFilter{AgentID: "nobody"}); got == nil || len(got) != 0 {
		t.Fatalf("empty result should be empty non-nil slice, got %#v", got)
	}
}

func TestAuditDefaultLimit(t *testing.T) {
	s := auditOpenTest(t)
	ctx := context.Background()
	err := s.Atomic(ctx, func(tx store.Tx) error {
		for i := 0; i < 250; i++ {
			if err := tx.InsertAudit(&model.AuditEvent{TS: time.Now(), AgentID: "a", Role: "worker", Tool: "x"}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []*model.AuditEvent
	if err := s.View(ctx, func(tx store.Tx) (e error) { got, e = tx.ListAudit(store.AuditFilter{}); return }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 200 {
		t.Fatalf("default limit: %d", len(got))
	}
}

func TestAuditAppendOnly(t *testing.T) {
	s := auditOpenTest(t)
	ctx := context.Background()
	e := &model.AuditEvent{TS: time.Now(), AgentID: "a", Role: "worker", Tool: "x"}
	if err := s.Atomic(ctx, func(tx store.Tx) error { return tx.InsertAudit(e) }); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`UPDATE audit SET note='tampered'`,
		`UPDATE audit SET tool='y' WHERE id=1`,
		`DELETE FROM audit`,
		`DELETE FROM audit WHERE id=1`,
	} {
		_, err := s.wdb.ExecContext(ctx, stmt)
		if err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("%q: expected append-only failure, got %v", stmt, err)
		}
	}
	var got []*model.AuditEvent
	if err := s.View(ctx, func(tx store.Tx) (er error) { got, er = tx.ListAudit(store.AuditFilter{}); return }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Note != nil || got[0].Tool != "x" {
		t.Fatalf("audit row changed: %+v", got)
	}
}

func TestSpawns(t *testing.T) {
	s := auditOpenTest(t)
	ctx := context.Background()
	ts := time.Date(2026, 11, 20, 21, 0, 0, 0, time.UTC)
	sp1 := &model.Spawn{ID: "S-1", BriefID: "B-1", AgentID: "claude-b1-1", Kind: "claude-code", Model: "m", Provider: "anthropic",
		Status: "requested", Spawner: "noop", RequestedBy: "rex", CreatedAt: ts}
	sp2 := &model.Spawn{ID: "S-2", BriefID: "B-2", AgentID: "codex-b2-1", Kind: "codex", Model: "m2", Provider: "openai",
		Status: "failed", Spawner: "docker", Error: auditStrp("not implemented"), RequestedBy: "rex", CreatedAt: ts}
	sp10 := &model.Spawn{ID: "S-10", BriefID: "B-1", AgentID: "x", Kind: "opencode", Model: "m3", Provider: "deepseek",
		Status: "requested", Spawner: "noop", RequestedBy: "rex", CreatedAt: ts}

	err := s.Atomic(ctx, func(tx store.Tx) error {
		for _, sp := range []*model.Spawn{sp10, sp2, sp1} {
			if err := tx.InsertSpawn(sp); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	err = s.Atomic(ctx, func(tx store.Tx) error { return tx.InsertSpawn(sp1) })
	if !errors.Is(err, store.ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}

	list := func(brief string) []*model.Spawn {
		var out []*model.Spawn
		if err := s.View(ctx, func(tx store.Tx) (e error) { out, e = tx.ListSpawns(brief); return }); err != nil {
			t.Fatal(err)
		}
		return out
	}
	all := list("")
	if len(all) != 3 || all[0].ID != "S-1" || all[1].ID != "S-2" || all[2].ID != "S-10" {
		t.Fatalf("all: %+v", all)
	}
	if !reflect.DeepEqual(all[0], sp1) || !reflect.DeepEqual(all[1], sp2) {
		t.Fatalf("round trip mismatch: %+v / %+v", all[0], all[1])
	}
	if all[0].Error != nil {
		t.Fatal("error should be nil")
	}
	if b1 := list("B-1"); len(b1) != 2 || b1[0].ID != "S-1" || b1[1].ID != "S-10" {
		t.Fatalf("B-1: %+v", b1)
	}
	if got := list("B-none"); got == nil || len(got) != 0 {
		t.Fatalf("empty: %#v", got)
	}

	sp1.Status = "failed"
	sp1.Error = auditStrp("boom")
	if err := s.Atomic(ctx, func(tx store.Tx) error { return tx.UpdateSpawn(sp1) }); err != nil {
		t.Fatal(err)
	}
	if got := list("B-1")[0]; got.Status != "failed" || got.Error == nil || *got.Error != "boom" {
		t.Fatalf("update: %+v", got)
	}
	sp1.Error = nil
	sp1.Status = "spawned"
	if err := s.Atomic(ctx, func(tx store.Tx) error { return tx.UpdateSpawn(sp1) }); err != nil {
		t.Fatal(err)
	}
	if got := list("B-1")[0]; got.Status != "spawned" || got.Error != nil {
		t.Fatalf("update clear error: %+v", got)
	}

	err = s.Atomic(ctx, func(tx store.Tx) error { return tx.UpdateSpawn(&model.Spawn{ID: "S-99"}) })
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestKV(t *testing.T) {
	s := auditOpenTest(t)
	ctx := context.Background()

	err := s.View(ctx, func(tx store.Tx) error {
		v, ok, err := tx.GetKV("missing")
		if err != nil || ok || v != "" {
			t.Fatalf("missing: %q %v %v", v, ok, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	err = s.Atomic(ctx, func(tx store.Tx) error {
		if err := tx.SetKV("charter", "one"); err != nil {
			return err
		}
		return tx.SetKV("paused", "")
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Atomic(ctx, func(tx store.Tx) error { return tx.SetKV("charter", "two") }); err != nil {
		t.Fatal(err)
	}
	err = s.View(ctx, func(tx store.Tx) error {
		if v, ok, err := tx.GetKV("charter"); err != nil || !ok || v != "two" {
			t.Fatalf("charter: %q %v %v", v, ok, err)
		}
		if v, ok, err := tx.GetKV("paused"); err != nil || !ok || v != "" {
			t.Fatalf("paused: %q %v %v", v, ok, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
