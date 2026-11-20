package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

func TestPlansRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	note := "first draft"
	created := time.UnixMilli(1_700_000_000_123).UTC()
	decided := time.UnixMilli(1_700_000_500_456).UTC()
	p1 := &model.Plan{ID: "P-1", BriefID: "B-1", Status: model.PlanProposed, ProposedBy: "rex", Note: &note, CreatedAt: created}
	p2 := &model.Plan{ID: "P-2", BriefID: "B-1", Status: model.PlanApproved, ProposedBy: "rex", CreatedAt: created.Add(time.Second), DecidedAt: &decided}
	p3 := &model.Plan{ID: "P-3", BriefID: "B-2", Status: model.PlanProposed, ProposedBy: "rex", CreatedAt: created.Add(2 * time.Second)}

	if err := s.Atomic(ctx, func(tx store.Tx) error {
		for _, p := range []*model.Plan{p1, p2, p3} {
			if err := tx.InsertPlan(p); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	err = s.Atomic(ctx, func(tx store.Tx) error { return tx.InsertPlan(p1) })
	if !errors.Is(err, store.ErrDuplicate) {
		t.Fatalf("duplicate insert: got %v", err)
	}

	_ = s.View(ctx, func(tx store.Tx) error {
		got, err := tx.GetPlan("P-1")
		if err != nil {
			t.Fatal(err)
		}
		if got.BriefID != "B-1" || got.Status != model.PlanProposed || got.ProposedBy != "rex" ||
			got.Note == nil || *got.Note != note || !got.CreatedAt.Equal(created) || got.DecidedAt != nil || got.Tasks != nil {
			t.Fatalf("p1 mismatch: %+v", got)
		}
		got, err = tx.GetPlan("P-2")
		if err != nil {
			t.Fatal(err)
		}
		if got.Note != nil || got.DecidedAt == nil || !got.DecidedAt.Equal(decided) {
			t.Fatalf("p2 mismatch: %+v", got)
		}
		if _, err := tx.GetPlan("P-99"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("missing: got %v", err)
		}

		all, err := tx.ListPlans("", "")
		if err != nil || len(all) != 3 || all[0].ID != "P-1" || all[2].ID != "P-3" {
			t.Fatalf("list all: %v %v", all, err)
		}
		b1, _ := tx.ListPlans("B-1", "")
		if len(b1) != 2 {
			t.Fatalf("list brief: %d", len(b1))
		}
		prop, _ := tx.ListPlans("", model.PlanProposed)
		if len(prop) != 2 {
			t.Fatalf("list status: %d", len(prop))
		}
		both, _ := tx.ListPlans("B-1", model.PlanApproved)
		if len(both) != 1 || both[0].ID != "P-2" {
			t.Fatalf("list both: %v", both)
		}
		none, err := tx.ListPlans("B-9", "")
		if err != nil || len(none) != 0 {
			t.Fatalf("list none: %v %v", none, err)
		}
		return nil
	})

	if err := s.Atomic(ctx, func(tx store.Tx) error {
		p, err := tx.GetPlan("P-1")
		if err != nil {
			return err
		}
		p.Status = model.PlanRejected
		p.Note = nil
		p.DecidedAt = &decided
		return tx.UpdatePlan(p)
	}); err != nil {
		t.Fatal(err)
	}
	_ = s.View(ctx, func(tx store.Tx) error {
		got, _ := tx.GetPlan("P-1")
		if got.Status != model.PlanRejected || got.Note != nil || got.DecidedAt == nil || !got.DecidedAt.Equal(decided) {
			t.Fatalf("after update: %+v", got)
		}
		return nil
	})

	err = s.Atomic(ctx, func(tx store.Tx) error {
		return tx.UpdatePlan(&model.Plan{ID: "P-404", CreatedAt: created})
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update missing: got %v", err)
	}
}
