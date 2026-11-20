package sqlite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

func taskTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func taskTestPtr[T any](v T) *T { return &v }

func taskTestMin(id, brief string) *model.Task {
	now := time.UnixMilli(time.Now().UnixMilli()).UTC()
	return &model.Task{
		ID: id, BriefID: brief, PlanID: "P-1", Title: "t " + id, Spec: "spec", Repo: "o/r",
		Branch: "sub/" + id, Role: model.RoleWorker, Status: model.StatusReady,
		AcceptanceCriteria: []string{}, DependsOn: []string{},
		CreatedAt: now, UpdatedAt: now,
	}
}

func taskTestFull(id string) *model.Task {
	x := taskTestMin(id, "B-1")
	hb := time.UnixMilli(1_700_000_000_123).UTC()
	lease := hb.Add(15 * time.Minute)
	claimed := hb.Add(-time.Minute)
	x.AcceptanceCriteria = []string{"a", "b"}
	x.DependsOn = []string{"T-1", "T-2"}
	x.Assignee = taskTestPtr("claude-1")
	x.Reviewer = taskTestPtr("codex-1")
	x.Status = model.StatusInProgress
	x.ProgressPct = 40
	x.LastHeartbeatAt = &hb
	x.LeaseExpiresAt = &lease
	x.ClaimedAt = &claimed
	x.PRURL = taskTestPtr("https://example.com/pr/1")
	x.Budget = model.Budget{MaxMinutes: 120, MaxCostEUR: 5.5}
	x.CostUsed = 0.4
	x.MinutesUsed = 12
	x.ReviewRounds = 2
	x.Tests = &model.TestResult{Passed: 10, Failed: 1, ReportedAt: hb}
	x.BlockedReason = taskTestPtr("budget_exceeded")
	return x
}

func taskTestEqual(t *testing.T, want, got *model.Task) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("task mismatch\nwant %+v\ngot  %+v", want, got)
	}
}

func TestTaskRoundTrip(t *testing.T) {
	s := taskTestStore(t)
	ctx := context.Background()
	full, minimal := taskTestFull("T-1"), taskTestMin("T-2", "B-1")

	zeroTests := taskTestMin("T-3", "B-1")
	zeroTests.Tests = &model.TestResult{Passed: 0, Failed: 0, ReportedAt: time.UnixMilli(5).UTC()}
	nilSlices := taskTestMin("T-4", "B-1")
	nilSlices.AcceptanceCriteria, nilSlices.DependsOn = nil, nil

	err := s.Atomic(ctx, func(tx store.Tx) error {
		for _, x := range []*model.Task{full, minimal, zeroTests, nilSlices} {
			if err := tx.InsertTask(x); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	nilSlices.AcceptanceCriteria, nilSlices.DependsOn = []string{}, []string{}

	err = s.View(ctx, func(tx store.Tx) error {
		for _, want := range []*model.Task{full, minimal, zeroTests, nilSlices} {
			got, err := tx.GetTask(want.ID)
			if err != nil {
				return err
			}
			taskTestEqual(t, want, got)
		}
		g, err := tx.GetTask("T-2")
		if err != nil {
			return err
		}
		if g.Assignee != nil || g.Reviewer != nil || g.PRURL != nil || g.Tests != nil || g.BlockedReason != nil ||
			g.LastHeartbeatAt != nil || g.LeaseExpiresAt != nil || g.ClaimedAt != nil {
			t.Fatalf("expected nil pointers, got %+v", g)
		}
		if _, err := tx.GetTask("T-99"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	err = s.Atomic(ctx, func(tx store.Tx) error { return tx.InsertTask(taskTestMin("T-1", "B-1")) })
	if !errors.Is(err, store.ErrDuplicate) {
		t.Fatalf("want ErrDuplicate, got %v", err)
	}
}

func TestTaskUpdate(t *testing.T) {
	s := taskTestStore(t)
	ctx := context.Background()
	x := taskTestMin("T-1", "B-1")
	if err := s.Atomic(ctx, func(tx store.Tx) error { return tx.InsertTask(x) }); err != nil {
		t.Fatal(err)
	}

	full := taskTestFull("T-1")
	full.CreatedAt = x.CreatedAt
	if err := s.Atomic(ctx, func(tx store.Tx) error { return tx.UpdateTask(full) }); err != nil {
		t.Fatal(err)
	}
	var got *model.Task
	view := func() {
		t.Helper()
		if err := s.View(ctx, func(tx store.Tx) (err error) { got, err = tx.GetTask("T-1"); return }); err != nil {
			t.Fatal(err)
		}
	}
	view()
	taskTestEqual(t, full, got)

	if err := s.Atomic(ctx, func(tx store.Tx) error { return tx.UpdateTask(x) }); err != nil {
		t.Fatal(err)
	}
	view()
	taskTestEqual(t, x, got)

	err := s.Atomic(ctx, func(tx store.Tx) error { return tx.UpdateTask(taskTestMin("T-404", "B-1")) })
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestTaskListCountCost(t *testing.T) {
	s := taskTestStore(t)
	ctx := context.Background()
	err := s.Atomic(ctx, func(tx store.Tx) error {

		for _, n := range []int{10, 2, 1, 11, 3} {
			x := taskTestMin(fmt.Sprintf("T-%d", n), "B-1")
			x.CostUsed = float64(n)
			switch n {
			case 1:
				x.Status = model.StatusInProgress
				x.Assignee = taskTestPtr("a1")
			case 2:
				x.Status = model.StatusAssigned
				x.Assignee = taskTestPtr("a1")
				x.Role = model.RoleTester
			case 3:
				x.Status = model.StatusCancelled
			case 11:
				x.BriefID = "B-2"
				x.PlanID = "P-2"
				x.Reviewer = taskTestPtr("r1")
				x.Status = model.StatusInReview
			}
			if err := tx.InsertTask(x); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	ids := func(ts []*model.Task) []string {
		out := []string{}
		for _, x := range ts {
			out = append(out, x.ID)
		}
		return out
	}
	cases := []struct {
		name string
		f    store.TaskFilter
		want []string
	}{
		{"all", store.TaskFilter{}, []string{"T-1", "T-2", "T-3", "T-10", "T-11"}},
		{"brief", store.TaskFilter{BriefID: "B-1"}, []string{"T-1", "T-2", "T-3", "T-10"}},
		{"plan", store.TaskFilter{PlanID: "P-2"}, []string{"T-11"}},
		{"status in", store.TaskFilter{Status: []string{model.StatusInProgress, model.StatusAssigned}}, []string{"T-1", "T-2"}},
		{"assignee", store.TaskFilter{Assignee: "a1"}, []string{"T-1", "T-2"}},
		{"assignee+role", store.TaskFilter{Assignee: "a1", Role: model.RoleTester}, []string{"T-2"}},
		{"reviewer", store.TaskFilter{Reviewer: "r1"}, []string{"T-11"}},
		{"limit", store.TaskFilter{Limit: 2}, []string{"T-1", "T-2"}},
		{"none", store.TaskFilter{BriefID: "B-9"}, []string{}},
	}
	err = s.View(ctx, func(tx store.Tx) error {
		for _, c := range cases {
			got, err := tx.ListTasks(c.f)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(ids(got), c.want) {
				t.Errorf("%s: want %v got %v", c.name, c.want, ids(got))
			}
		}
		counts, err := tx.CountTasksByStatus()
		if err != nil {
			return err
		}
		wantCounts := map[string]int{
			model.StatusInProgress: 1, model.StatusAssigned: 1, model.StatusCancelled: 1,
			model.StatusReady: 1, model.StatusInReview: 1,
		}
		if !reflect.DeepEqual(counts, wantCounts) {
			t.Errorf("counts: want %v got %v", wantCounts, counts)
		}

		if c, err := tx.BriefCost("B-1"); err != nil || c != 13 {
			t.Errorf("brief cost B-1: %v %v", c, err)
		}
		if c, err := tx.BriefCost("B-404"); err != nil || c != 0 {
			t.Errorf("brief cost empty: %v %v", c, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTaskExtendLeases(t *testing.T) {
	s := taskTestStore(t)
	ctx := context.Background()
	base := time.UnixMilli(1_700_000_000_000).UTC()
	lease := base.Add(10 * time.Minute)
	mk := func(id, status, assignee string) *model.Task {
		x := taskTestMin(id, "B-1")
		x.Status = status
		x.Assignee = taskTestPtr(assignee)
		x.LastHeartbeatAt = &base
		x.LeaseExpiresAt = &lease
		return x
	}
	err := s.Atomic(ctx, func(tx store.Tx) error {
		for _, x := range []*model.Task{
			mk("T-1", model.StatusInProgress, "a1"),
			mk("T-2", model.StatusInProgress, "a1"),
			mk("T-3", model.StatusAssigned, "a1"),
			mk("T-4", model.StatusInProgress, "a2"),
		} {
			if err := tx.InsertTask(x); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	hb := base.Add(time.Minute).UnixMilli()
	later := lease.Add(5 * time.Minute).UnixMilli()
	earlier := lease.Add(-5 * time.Minute).UnixMilli()
	err = s.Atomic(ctx, func(tx store.Tx) error {
		return tx.ExtendLeases([]store.LeaseRow{
			{TaskID: "T-1", Assignee: "a1", Heartbeat: hb, Lease: later},
			{TaskID: "T-2", Assignee: "a1", Heartbeat: hb, Lease: earlier},
			{TaskID: "T-3", Assignee: "a1", Heartbeat: hb, Lease: later},
			{TaskID: "T-4", Assignee: "a1", Heartbeat: hb, Lease: later},
			{TaskID: "T-404", Assignee: "a1", Heartbeat: hb, Lease: later},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Atomic(ctx, func(tx store.Tx) error { return tx.ExtendLeases(nil) }); err != nil {
		t.Fatal(err)
	}

	err = s.View(ctx, func(tx store.Tx) error {
		for id, wantLease := range map[string]int64{
			"T-1": later, "T-2": lease.UnixMilli(), "T-3": lease.UnixMilli(), "T-4": lease.UnixMilli(),
		} {
			x, err := tx.GetTask(id)
			if err != nil {
				return err
			}
			wantHB := base.UnixMilli()
			if id == "T-1" {
				wantHB = hb
			}
			if x.LeaseExpiresAt.UnixMilli() != wantLease || x.LastHeartbeatAt.UnixMilli() != wantHB {
				t.Errorf("%s: lease %v hb %v", id, x.LeaseExpiresAt, x.LastHeartbeatAt)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
