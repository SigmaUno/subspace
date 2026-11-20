package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

func agentTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func agentInsertTask(t *testing.T, st store.Tx, id, assignee, status string) {
	t.Helper()
	_, err := st.(*tx).q.ExecContext(context.Background(),
		`INSERT INTO tasks(id, brief_id, plan_id, title, spec, acceptance, repo, branch, role, assignee, depends_on, status, created_at, updated_at)
		 VALUES(?, 'B-1', 'P-1', 't', 's', '[]', 'r', 'b', 'worker', ?, '[]', ?, 1, 1)`, id, assignee, status)
	if err != nil {
		t.Fatal(err)
	}
}

func TestAgentRoundTrip(t *testing.T) {
	s := agentTestStore(t)
	ctx := context.Background()
	created := time.UnixMilli(1700000000123).UTC()
	seen := time.UnixMilli(1700000005000).UTC()
	reason := "loop"
	in := &model.Agent{ID: "claude-1", Role: "worker", Kind: "claude-code", Model: "m", Provider: "anthropic",
		Status: "paused", PauseReason: &reason, LastSeenAt: &seen, CreatedAt: created, Spawned: true, TokenHash: "h1"}
	bare := &model.Agent{ID: "rex", Role: "leader", Kind: "other", Model: "m2", Provider: "openai",
		Status: "active", CreatedAt: created, TokenHash: "h2"}
	err := s.Atomic(ctx, func(tx store.Tx) error {
		if err := tx.InsertAgent(in); err != nil {
			return err
		}
		return tx.InsertAgent(bare)
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.View(ctx, func(tx store.Tx) error {
		got, err := tx.GetAgent("claude-1")
		if err != nil {
			return err
		}
		if got.PauseReason == nil || *got.PauseReason != "loop" || got.LastSeenAt == nil || !got.LastSeenAt.Equal(seen) ||
			!got.CreatedAt.Equal(created) || !got.Spawned || got.TokenHash != "h1" || got.Role != "worker" ||
			got.Kind != "claude-code" || got.Model != "m" || got.Provider != "anthropic" || got.Status != "paused" {
			t.Errorf("round trip mismatch: %+v", got)
		}
		if got.CurrentTaskID != nil || got.AssignedCount != 0 {
			t.Errorf("unexpected computed fields: %+v", got)
		}
		b, err := tx.GetAgent("rex")
		if err != nil {
			return err
		}
		if b.PauseReason != nil || b.LastSeenAt != nil || b.Spawned || b.CurrentTaskID != nil {
			t.Errorf("nil fields not nil: %+v", b)
		}
		if _, err := tx.GetAgent("nope"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("want ErrNotFound, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAgentDuplicates(t *testing.T) {
	s := agentTestStore(t)
	ctx := context.Background()
	mk := func(id, hash string) *model.Agent {
		return &model.Agent{ID: id, Role: "worker", Kind: "k", Model: "m", Provider: "p", Status: "active",
			CreatedAt: time.Now(), TokenHash: hash}
	}
	if err := s.Atomic(ctx, func(tx store.Tx) error { return tx.InsertAgent(mk("a", "h")) }); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*model.Agent{mk("a", "other"), mk("b", "h")} {
		err := s.Atomic(ctx, func(tx store.Tx) error { return tx.InsertAgent(a) })
		if !errors.Is(err, store.ErrDuplicate) {
			t.Errorf("%s: want ErrDuplicate, got %v", a.ID, err)
		}
	}
}

func TestAgentByTokenHashAndUpdate(t *testing.T) {
	s := agentTestStore(t)
	ctx := context.Background()
	a := &model.Agent{ID: "a", Role: "worker", Kind: "k", Model: "m", Provider: "p", Status: "active",
		CreatedAt: time.Now(), TokenHash: "h1"}
	if err := s.Atomic(ctx, func(tx store.Tx) error { return tx.InsertAgent(a) }); err != nil {
		t.Fatal(err)
	}
	err := s.Atomic(ctx, func(tx store.Tx) error {
		got, err := tx.GetAgentByTokenHash("h1")
		if err != nil {
			return err
		}
		if got.ID != "a" {
			t.Errorf("wrong agent %s", got.ID)
		}
		if _, err := tx.GetAgentByTokenHash("zzz"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("want ErrNotFound, got %v", err)
		}

		reason := "loop"
		got.Status, got.PauseReason, got.TokenHash = "paused", &reason, ""
		if err := tx.UpdateAgent(got); err != nil {
			return err
		}
		got, err = tx.GetAgent("a")
		if err != nil {
			return err
		}
		if got.Status != "paused" || got.PauseReason == nil || *got.PauseReason != "loop" || got.TokenHash != "h1" {
			t.Errorf("update mismatch: %+v", got)
		}

		got.TokenHash, got.PauseReason, got.Status = "h2", nil, "active"
		if err := tx.UpdateAgent(got); err != nil {
			return err
		}
		if _, err := tx.GetAgentByTokenHash("h1"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("old hash still valid: %v", err)
		}
		got, err = tx.GetAgentByTokenHash("h2")
		if err != nil {
			return err
		}
		if got.PauseReason != nil {
			t.Errorf("pause reason not cleared")
		}
		return tx.UpdateAgent(&model.Agent{ID: "ghost", Status: "active"})
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update of missing agent: want ErrNotFound, got %v", err)
	}
}

func TestAgentComputedFields(t *testing.T) {
	s := agentTestStore(t)
	ctx := context.Background()
	err := s.Atomic(ctx, func(tx store.Tx) error {
		for _, id := range []string{"a", "b", "c"} {
			err := tx.InsertAgent(&model.Agent{ID: id, Role: "worker", Kind: "k", Model: "m", Provider: "p",
				Status: "active", CreatedAt: time.Now(), TokenHash: "h-" + id})
			if err != nil {
				return err
			}
		}
		agentInsertTask(t, tx, "T-1", "a", "assigned")
		agentInsertTask(t, tx, "T-2", "a", "in_progress")
		agentInsertTask(t, tx, "T-3", "a", "changes_requested")
		agentInsertTask(t, tx, "T-4", "a", "submitted")
		agentInsertTask(t, tx, "T-5", "a", "done")
		agentInsertTask(t, tx, "T-6", "b", "assigned")
		agentInsertTask(t, tx, "T-7", "", "ready")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.View(ctx, func(tx store.Tx) error {
		list, err := tx.ListAgents(store.AgentFilter{})
		if err != nil {
			return err
		}
		if len(list) != 3 {
			t.Fatalf("want 3 agents, got %d", len(list))
		}
		a, b, c := list[0], list[1], list[2]
		if a.AssignedCount != 3 || a.CurrentTaskID == nil || *a.CurrentTaskID != "T-2" {
			t.Errorf("a: %+v", a)
		}
		if b.AssignedCount != 1 || b.CurrentTaskID != nil {
			t.Errorf("b: %+v", b)
		}
		if c.AssignedCount != 0 || c.CurrentTaskID != nil {
			t.Errorf("c: %+v", c)
		}
		got, err := tx.GetAgentByTokenHash("h-a")
		if err != nil {
			return err
		}
		if got.AssignedCount != 3 || got.CurrentTaskID == nil || *got.CurrentTaskID != "T-2" {
			t.Errorf("by hash: %+v", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAgentListFilterAndTouch(t *testing.T) {
	s := agentTestStore(t)
	ctx := context.Background()
	err := s.Atomic(ctx, func(tx store.Tx) error {
		for _, x := range []struct{ id, role, status string }{
			{"w-3", "worker", "active"}, {"w-1", "worker", "revoked"}, {"w-2", "worker", "active"},
			{"rex", "leader", "active"},
		} {
			err := tx.InsertAgent(&model.Agent{ID: x.id, Role: x.role, Kind: "k", Model: "m", Provider: "p",
				Status: x.status, CreatedAt: time.Now(), TokenHash: "h-" + x.id})
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := func(l []*model.Agent) []string {
		var o []string
		for _, a := range l {
			o = append(o, a.ID)
		}
		return o
	}
	err = s.View(ctx, func(tx store.Tx) error {
		l, _ := tx.ListAgents(store.AgentFilter{})
		if g := ids(l); len(g) != 4 || g[0] != "rex" || g[1] != "w-1" || g[3] != "w-3" {
			t.Errorf("order: %v", g)
		}
		l, _ = tx.ListAgents(store.AgentFilter{Role: "worker", Status: "active"})
		if g := ids(l); len(g) != 2 || g[0] != "w-2" || g[1] != "w-3" {
			t.Errorf("filter: %v", g)
		}
		l, _ = tx.ListAgents(store.AgentFilter{Limit: 2})
		if len(l) != 2 {
			t.Errorf("limit: %d", len(l))
		}
		l, err := tx.ListAgents(store.AgentFilter{Role: "tester"})
		if err != nil || l == nil || len(l) != 0 {
			t.Errorf("empty list: %v %v", l, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	err = s.Atomic(ctx, func(tx store.Tx) error {
		if err := tx.TouchAgents(map[string]int64{"w-1": 5000, "rex": 7000, "unknown": 1}); err != nil {
			return err
		}
		return tx.TouchAgents(map[string]int64{"w-1": 4000, "rex": 9000})
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.View(ctx, func(tx store.Tx) error {
		w, _ := tx.GetAgent("w-1")
		if w.LastSeenAt == nil || w.LastSeenAt.UnixMilli() != 5000 {
			t.Errorf("touch must not go backwards: %+v", w.LastSeenAt)
		}
		r, _ := tx.GetAgent("rex")
		if r.LastSeenAt == nil || r.LastSeenAt.UnixMilli() != 9000 {
			t.Errorf("touch: %+v", r.LastSeenAt)
		}
		w2, _ := tx.GetAgent("w-2")
		if w2.LastSeenAt != nil {
			t.Errorf("untouched agent has last_seen")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
