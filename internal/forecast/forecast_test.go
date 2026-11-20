package forecast

import (
	"reflect"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/usage"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func task(id, status string, minutes int, cost float64, deps ...string) *model.Task {
	return &model.Task{ID: id, BriefID: "B-1", Status: status, MinutesUsed: minutes, CostUsed: cost, DependsOn: deps}
}

func withBudget(t *model.Task, minutes int, cost float64) *model.Task {
	t.Budget = model.Budget{MaxMinutes: minutes, MaxCostEUR: cost}
	return t
}

func snapshot(maxCost float64, parallel int, tasks ...*model.Task) Snapshot {
	var used float64
	for _, t := range tasks {
		if t.Status != model.StatusCancelled {
			used += t.CostUsed
		}
	}
	return Snapshot{
		Now:   t0,
		Brief: model.Brief{ID: "B-1", CostUsed: used, Budget: model.BriefBudget{MaxCostEUR: maxCost, MaxParallelAgent: parallel}},
		Tasks: tasks,
	}
}

func codes(f Forecast) []string {
	out := []string{}
	for _, r := range f.Risks {
		out = append(out, r.Code+":"+r.Severity)
	}
	return out
}

func TestBuild(t *testing.T) {
	tests := []struct {
		name  string
		snap  Snapshot
		check func(t *testing.T, f Forecast)
	}{
		{
			name: "finished brief projects exactly what was spent",
			snap: snapshot(10, 3, task("T-1", model.StatusDone, 30, 2), task("T-2", model.StatusApproved, 20, 1), task("T-3", model.StatusCancelled, 5, 9)),
			check: func(t *testing.T, f Forecast) {
				if f.Cost.Projected != (Range{3, 3, 3}) || f.Schedule.RemainingMinutes != (Range{}) || f.Tasks != (Counts{Total: 2, Complete: 2}) {
					t.Fatalf("unexpected: %+v", f)
				}
				if len(f.Risks) != 0 || f.CostOverrun() {
					t.Fatalf("risks: %v", codes(f))
				}
			},
		},
		{
			name: "remaining tasks push cost past the budget",
			snap: snapshot(5, 3, task("T-1", model.StatusDone, 30, 2), task("T-2", model.StatusReady, 0, 0), task("T-3", model.StatusReady, 0, 0)),
			check: func(t *testing.T, f Forecast) {
				if !f.CostOverrun() || f.Cost.ExceedProbability < 0.5 || f.Cost.Projected.P10 > f.Cost.Projected.P50 || f.Cost.Projected.P50 > f.Cost.Projected.P90 {
					t.Fatalf("cost: %+v", f.Cost)
				}
				if !reflect.DeepEqual(codes(f), []string{"cost_overrun:high"}) {
					t.Fatalf("risks: %v", codes(f))
				}
			},
		},
		{
			name: "spent budget is flagged as exceeded",
			snap: snapshot(3, 3, task("T-1", model.StatusInProgress, 30, 4)),
			check: func(t *testing.T, f Forecast) {
				if !reflect.DeepEqual(codes(f), []string{"cost_exceeded:high"}) || f.Cost.ExceedProbability != 1 {
					t.Fatalf("risks: %v cost: %+v", codes(f), f.Cost)
				}
			},
		},
		{
			name: "ample budget raises no flag",
			snap: snapshot(500, 3, task("T-1", model.StatusDone, 30, 2), task("T-2", model.StatusReady, 0, 0)),
			check: func(t *testing.T, f Forecast) {
				if len(f.Risks) != 0 || f.Cost.ExceedProbability != 0 {
					t.Fatalf("risks: %v cost: %+v", codes(f), f.Cost)
				}
			},
		},
		{
			name: "slow history slips the critical path",
			snap: snapshot(0, 3,
				task("T-1", model.StatusDone, 60, 0),
				withBudget(task("T-2", model.StatusReady, 0, 0), 10, 0),
				withBudget(task("T-3", model.StatusBacklog, 0, 0, "T-2"), 10, 0)),
			check: func(t *testing.T, f Forecast) {
				if f.Schedule.PlannedCriticalPath != 20 || f.Schedule.CriticalPathMinutes.P50 < 60 {
					t.Fatalf("schedule: %+v", f.Schedule)
				}
				if !reflect.DeepEqual(codes(f), []string{"critical_path_slip:high"}) {
					t.Fatalf("risks: %v", codes(f))
				}
			},
		},
		{
			name: "fast history stays within plan",
			snap: snapshot(0, 3,
				task("T-1", model.StatusDone, 4, 0),
				withBudget(task("T-2", model.StatusReady, 0, 0), 10, 0),
				withBudget(task("T-3", model.StatusBacklog, 0, 0, "T-2"), 10, 0)),
			check: func(t *testing.T, f Forecast) {
				if len(f.Risks) != 0 || f.Schedule.CriticalPathMinutes.P50 > 20 {
					t.Fatalf("risks: %v schedule: %+v", codes(f), f.Schedule)
				}
			},
		},
		{
			name: "unbudgeted tasks have no plan to slip against",
			snap: snapshot(0, 3, task("T-1", model.StatusReady, 0, 0), task("T-2", model.StatusBacklog, 0, 0, "T-1")),
			check: func(t *testing.T, f Forecast) {
				if len(f.Risks) != 0 || f.Schedule.PlannedCriticalPath != 0 || f.Schedule.CriticalPathMinutes.P50 < 60 {
					t.Fatalf("risks: %v schedule: %+v", codes(f), f.Schedule)
				}
			},
		},
		{
			name: "one agent serialises independent tasks",
			snap: snapshot(0, 1, task("T-1", model.StatusReady, 0, 0), task("T-2", model.StatusReady, 0, 0), task("T-3", model.StatusReady, 0, 0), task("T-4", model.StatusReady, 0, 0)),
			check: func(t *testing.T, f Forecast) {
				wide := Build(snapshot(0, 4, task("T-1", model.StatusReady, 0, 0), task("T-2", model.StatusReady, 0, 0), task("T-3", model.StatusReady, 0, 0), task("T-4", model.StatusReady, 0, 0)), Options{Seed: 42, Runs: 2000})
				if f.Schedule.RemainingMinutes.P50 < 2*wide.Schedule.RemainingMinutes.P50 {
					t.Fatalf("serial %v parallel %v", f.Schedule.RemainingMinutes, wide.Schedule.RemainingMinutes)
				}
				if f.Schedule.CriticalPathMinutes != wide.Schedule.CriticalPathMinutes {
					t.Fatalf("critical path must ignore parallelism: %v vs %v", f.Schedule.CriticalPathMinutes, wide.Schedule.CriticalPathMinutes)
				}
			},
		},
		{
			name: "progress narrows the estimate",
			snap: func() Snapshot {
				a := task("T-1", model.StatusInProgress, 45, 0)
				a.ProgressPct = 90
				return snapshot(0, 3, a)
			}(),
			check: func(t *testing.T, f Forecast) {
				r := f.Schedule.RemainingMinutes
				if r.P50 < 3 || r.P50 > 8 || r.P90-r.P10 > 8 {
					t.Fatalf("remaining: %+v", r)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := Build(tc.snap, Options{Seed: 42, Runs: 2000})
			tc.check(t, f)
		})
	}
}

func TestBuildIsDeterministicPerSeed(t *testing.T) {
	s := snapshot(5, 2, task("T-1", model.StatusDone, 30, 2), task("T-2", model.StatusReady, 0, 0), task("T-3", model.StatusReady, 0, 0, "T-2"))
	a := Build(s, Options{Seed: 1})
	if b := Build(s, Options{Seed: 1}); !reflect.DeepEqual(a, b) {
		t.Fatalf("same seed differs:\n%+v\n%+v", a, b)
	}
	if b := Build(s, Options{Seed: 2}); reflect.DeepEqual(a.Cost.Projected, b.Cost.Projected) {
		t.Fatal("different seeds produced identical projections")
	}
	d1, d2 := Build(s, Options{}), Build(s, Options{})
	if d1.Seed == 0 || !reflect.DeepEqual(d1, d2) {
		t.Fatal("default seed must be derived deterministically from the brief")
	}
}

func TestBurn(t *testing.T) {
	claimed := t0.Add(-2 * time.Hour)
	done := task("T-1", model.StatusDone, 60, 4)
	done.ClaimedAt = &claimed
	s := snapshot(10, 2, done, task("T-2", model.StatusReady, 0, 0))

	b := Build(s, Options{Seed: 1}).Burn
	if b.Source != "tasks" || b.EURPerHour != 2 || b.TasksPerHour != 0.5 || b.RunwayHours == nil || *b.RunwayHours != 3 {
		t.Fatalf("task burn: %+v", b)
	}

	s.Usage = []usage.Record{
		{TS: t0.Add(-30 * time.Minute), CostUSD: 2},
		{TS: t0.Add(-10 * time.Minute), CostUSD: 1},
		{TS: t0.Add(-3 * time.Hour), CostUSD: 100},
	}
	b = Build(s, Options{Seed: 1, EURToUSD: 2}).Burn
	if b.Source != "usage" || b.EURPerHour != 3 || b.RunwayHours == nil || *b.RunwayHours != 2 {
		t.Fatalf("usage burn: %+v", b)
	}
}

func TestDependencyCycleDoesNotHang(t *testing.T) {
	s := snapshot(0, 2, task("T-1", model.StatusReady, 0, 0, "T-2"), task("T-2", model.StatusReady, 0, 0, "T-1"))
	if f := Build(s, Options{Seed: 1, Runs: 50}); f.Tasks.Remaining != 2 || f.Schedule.RemainingMinutes.P50 <= 0 {
		t.Fatalf("unexpected: %+v", f)
	}
}
