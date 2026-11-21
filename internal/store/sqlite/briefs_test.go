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

func openBriefTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func testBrief(id string, created time.Time) *model.Brief {
	return &model.Brief{
		ID: id, Title: "Title " + id, Body: "# body\nline", Repos: []string{"SigmaUno/foo", "SigmaUno/bar"},
		Status: "open", Budget: model.BriefBudget{MaxCostEUR: 12.5, MaxParallelAgent: 7},
		AllowedProviders: []string{"anthropic", "deepseek"}, PlanStatus: "none", CreatedAt: created,
	}
}

func TestBriefRoundTrip(t *testing.T) {
	s := openBriefTestStore(t)
	ctx := context.Background()
	want := testBrief("B-1", time.UnixMilli(1760000000123).UTC())
	if err := s.Atomic(ctx, func(tx store.Tx) error { return tx.InsertBrief(want) }); err != nil {
		t.Fatal(err)
	}
	err := s.View(ctx, func(tx store.Tx) error {
		got, err := tx.GetBrief("B-1")
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v want %+v", got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBriefEmptyListsAndNotFound(t *testing.T) {
	s := openBriefTestStore(t)
	ctx := context.Background()
	b := testBrief("B-1", time.UnixMilli(1000).UTC())
	b.Repos, b.AllowedProviders = nil, nil
	if err := s.Atomic(ctx, func(tx store.Tx) error { return tx.InsertBrief(b) }); err != nil {
		t.Fatal(err)
	}
	_ = s.View(ctx, func(tx store.Tx) error {
		got, err := tx.GetBrief("B-1")
		if err != nil {
			t.Fatal(err)
		}
		if got.Repos == nil || len(got.Repos) != 0 || got.AllowedProviders == nil || len(got.AllowedProviders) != 0 {
			t.Errorf("want empty non-nil slices, got %#v %#v", got.Repos, got.AllowedProviders)
		}
		if _, err := tx.GetBrief("B-9"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetBrief missing: %v", err)
		}
		return nil
	})
	err := s.Atomic(ctx, func(tx store.Tx) error { return tx.UpdateBrief(testBrief("B-9", time.Now())) })
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("UpdateBrief missing: %v", err)
	}
}

func TestBriefDuplicate(t *testing.T) {
	s := openBriefTestStore(t)
	ctx := context.Background()
	b := testBrief("B-1", time.UnixMilli(1000).UTC())
	if err := s.Atomic(ctx, func(tx store.Tx) error { return tx.InsertBrief(b) }); err != nil {
		t.Fatal(err)
	}
	err := s.Atomic(ctx, func(tx store.Tx) error { return tx.InsertBrief(b) })
	if !errors.Is(err, store.ErrDuplicate) {
		t.Fatalf("want ErrDuplicate, got %v", err)
	}
}

func TestBriefUpdate(t *testing.T) {
	s := openBriefTestStore(t)
	ctx := context.Background()
	b := testBrief("B-1", time.UnixMilli(1000).UTC())
	if err := s.Atomic(ctx, func(tx store.Tx) error { return tx.InsertBrief(b) }); err != nil {
		t.Fatal(err)
	}
	b.Title, b.Body, b.Status, b.PlanStatus = "New", "new body", "closed", "approved"
	b.Repos = []string{"x/y"}
	b.AllowedProviders = []string{}
	b.Budget = model.BriefBudget{MaxCostEUR: 99.25, MaxParallelAgent: 3}
	if err := s.Atomic(ctx, func(tx store.Tx) error { return tx.UpdateBrief(b) }); err != nil {
		t.Fatal(err)
	}
	_ = s.View(ctx, func(tx store.Tx) error {
		got, err := tx.GetBrief("B-1")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, b) {
			t.Errorf("got %+v want %+v", got, b)
		}
		return nil
	})
}

func TestListBriefs(t *testing.T) {
	s := openBriefTestStore(t)
	ctx := context.Background()
	b1 := testBrief("B-2", time.UnixMilli(1000).UTC())
	b2 := testBrief("B-1", time.UnixMilli(2000).UTC())
	b3 := testBrief("B-3", time.UnixMilli(1000).UTC())
	b3.Status = "closed"
	err := s.Atomic(ctx, func(tx store.Tx) error {
		for _, b := range []*model.Brief{b1, b2, b3} {
			if err := tx.InsertBrief(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := func(l []*model.Brief) []string {
		var out []string
		for _, b := range l {
			out = append(out, b.ID)
		}
		return out
	}
	_ = s.View(ctx, func(tx store.Tx) error {
		all, err := tx.ListBriefs(store.BriefFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if got := ids(all); !reflect.DeepEqual(got, []string{"B-2", "B-3", "B-1"}) {
			t.Errorf("all order: %v", got)
		}
		open, _ := tx.ListBriefs(store.BriefFilter{Status: "open"})
		if got := ids(open); !reflect.DeepEqual(got, []string{"B-2", "B-1"}) {
			t.Errorf("open: %v", got)
		}
		none, err := tx.ListBriefs(store.BriefFilter{Status: "nope"})
		if err != nil || len(none) != 0 {
			t.Errorf("nope: %v %v", none, err)
		}
		return nil
	})
}
