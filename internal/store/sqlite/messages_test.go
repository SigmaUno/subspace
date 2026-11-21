package sqlite

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

func openMsgStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMessages(t *testing.T) {
	s := openMsgStore(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 20, 21, 4, 5, 123_000_000, time.UTC)

	var ids []int64
	err := s.Atomic(ctx, func(tx store.Tx) error {
		for i := 0; i < 5; i++ {
			m := &model.Message{ThreadKind: "task", ThreadID: "T-1", AuthorID: "a", AuthorRole: "worker",
				Kind: "note", Body: "hello", CreatedAt: base.Add(time.Duration(i) * time.Second)}
			if err := tx.InsertMessage(m); err != nil {
				return err
			}
			ids = append(ids, m.ID)
		}
		return tx.InsertMessage(&model.Message{ThreadKind: "agent", ThreadID: "a", AuthorID: "owner",
			AuthorRole: "owner", Kind: "message", Body: "other", CreatedAt: base})
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] || ids[i-1] == 0 {
			t.Fatalf("ids not increasing: %v", ids)
		}
	}

	_ = s.View(ctx, func(tx store.Tx) error {
		all, err := tx.ListMessages("task", "T-1", 0, 0)
		if err != nil || len(all) != 5 {
			t.Fatalf("all: %v %d", err, len(all))
		}
		if all[0].ID != ids[0] || all[0].Body != "hello" || !all[0].CreatedAt.Equal(base) || all[0].AuthorRole != "worker" {
			t.Fatalf("round trip: %+v", all[0])
		}
		since, _ := tx.ListMessages("task", "T-1", ids[2], 0)
		if len(since) != 2 || since[0].ID != ids[3] {
			t.Fatalf("since: %+v", since)
		}
		lim, _ := tx.ListMessages("task", "T-1", 0, 2)
		if len(lim) != 2 || lim[1].ID != ids[1] {
			t.Fatalf("limit: %+v", lim)
		}
		none, err := tx.ListMessages("brief", "B-1", 0, 0)
		if err != nil || none == nil || len(none) != 0 {
			t.Fatalf("none: %v %v", none, err)
		}
		return nil
	})
}

func TestMessagesDefaultLimit(t *testing.T) {
	s := openMsgStore(t)
	ctx := context.Background()
	err := s.Atomic(ctx, func(tx store.Tx) error {
		for i := 0; i < 250; i++ {
			if err := tx.InsertMessage(&model.Message{ThreadKind: "brief", ThreadID: "B-1", AuthorID: "x",
				AuthorRole: "leader", Kind: "system", Body: "b", CreatedAt: time.Now()}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.View(ctx, func(tx store.Tx) error {
		got, _ := tx.ListMessages("brief", "B-1", 0, 0)
		if len(got) != 200 {
			t.Fatalf("default limit: %d", len(got))
		}
		return nil
	})
}

func TestQuestions(t *testing.T) {
	s := openMsgStore(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 20, 21, 4, 5, 0, time.UTC)
	brief, task := "B-1", "T-3"

	full := &model.Question{ID: "Q-1", BriefID: &brief, TaskID: &task, AgentID: "claude-1", Question: "which?",
		Options: []string{"a", "b"}, Status: "open", CreatedAt: base}
	bare := &model.Question{ID: "Q-2", AgentID: "claude-2", Question: "why?", Status: "open", CreatedAt: base.Add(time.Second)}
	early := &model.Question{ID: "Q-3", AgentID: "x", Question: "early", Status: "open", CreatedAt: base.Add(-time.Hour)}

	err := s.Atomic(ctx, func(tx store.Tx) error {
		for _, q := range []*model.Question{full, bare, early} {
			if err := tx.InsertQuestion(q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	err = s.Atomic(ctx, func(tx store.Tx) error { return tx.InsertQuestion(full) })
	if !errors.Is(err, store.ErrDuplicate) {
		t.Fatalf("want ErrDuplicate, got %v", err)
	}

	_ = s.View(ctx, func(tx store.Tx) error {
		g, err := tx.GetQuestion("Q-1")
		if err != nil || !reflect.DeepEqual(g, full) {
			t.Fatalf("full: %v %+v", err, g)
		}
		g, err = tx.GetQuestion("Q-2")
		if err != nil || g.BriefID != nil || g.TaskID != nil || g.Answer != nil || g.AnsweredAt != nil || g.Options == nil || len(g.Options) != 0 {
			t.Fatalf("bare: %v %+v", err, g)
		}
		if _, err := tx.GetQuestion("Q-99"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
		return nil
	})

	ans := "a"
	at := base.Add(time.Minute)
	full.Status, full.Answer, full.AnsweredAt = "answered", &ans, &at
	if err := s.Atomic(ctx, func(tx store.Tx) error { return tx.UpdateQuestion(full) }); err != nil {
		t.Fatal(err)
	}
	err = s.Atomic(ctx, func(tx store.Tx) error {
		return tx.UpdateQuestion(&model.Question{ID: "Q-404", AgentID: "x", Status: "open", CreatedAt: base})
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}

	_ = s.View(ctx, func(tx store.Tx) error {
		g, _ := tx.GetQuestion("Q-1")
		if !reflect.DeepEqual(g, full) {
			t.Fatalf("after update: %+v", g)
		}
		all, _ := tx.ListQuestions(store.QuestionFilter{})
		if len(all) != 3 || all[0].ID != "Q-3" || all[1].ID != "Q-1" || all[2].ID != "Q-2" {
			t.Fatalf("all order: %+v", all)
		}
		open, _ := tx.ListQuestions(store.QuestionFilter{Status: "open"})
		if len(open) != 2 || open[0].ID != "Q-3" || open[1].ID != "Q-2" {
			t.Fatalf("open: %+v", open)
		}
		answered, _ := tx.ListQuestions(store.QuestionFilter{Status: "answered"})
		if len(answered) != 1 || answered[0].ID != "Q-1" {
			t.Fatalf("answered: %+v", answered)
		}
		return nil
	})
}
