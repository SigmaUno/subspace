package forecast

import (
	"reflect"
	"testing"

	"github.com/SigmaUno/subspace/internal/estimate"
	"github.com/SigmaUno/subspace/internal/model"
)

func estimated(minutes, p80Minutes, cost, p80Cost float64) estimate.Estimate {
	return estimate.Estimate{
		Sufficient: true,
		Minutes:    estimate.Stat{Median: minutes, P80: p80Minutes},
		CostEUR:    estimate.Stat{Median: cost, P80: p80Cost},
	}
}

func TestBuildUsesEstimatesWhenHistoryIsSufficient(t *testing.T) {
	open := func() []*model.Task {
		return []*model.Task{
			withBudget(task("T-1", model.StatusReady, 0, 0), 120, 40),
			withBudget(task("T-2", model.StatusReady, 0, 0, "T-1"), 120, 40),
		}
	}
	base := Build(snapshot(100, 2, open()...), Options{})
	if base.Cost.Projected.P50 < 30 {
		t.Fatalf("fallback projection should follow the task budgets: %+v", base.Cost)
	}

	snap := snapshot(100, 2, open()...)
	snap.Estimates = map[string]estimate.Estimate{"T-1": estimated(10, 14, 1, 2), "T-2": estimated(20, 26, 2, 3)}
	got := Build(snap, Options{})
	if got.Cost.Projected.P50 < 2.5 || got.Cost.Projected.P50 > 4 {
		t.Fatalf("cost should follow the estimates: %+v", got.Cost)
	}
	if got.Schedule.CriticalPathMinutes.P50 < 25 || got.Schedule.CriticalPathMinutes.P50 > 40 {
		t.Fatalf("schedule should follow the estimates: %+v", got.Schedule)
	}
	if got.Schedule.CriticalPathMinutes.P10 >= got.Schedule.CriticalPathMinutes.P90 {
		t.Fatalf("estimates must keep an uncertainty range: %+v", got.Schedule)
	}
	if again := Build(snap, Options{}); !reflect.DeepEqual(again, got) {
		t.Fatal("forecast with estimates is not deterministic")
	}
}

func TestBuildIgnoresInsufficientEstimates(t *testing.T) {
	tasks := func() []*model.Task {
		return []*model.Task{withBudget(task("T-1", model.StatusReady, 0, 0), 120, 40)}
	}
	want := Build(snapshot(100, 2, tasks()...), Options{})
	snap := snapshot(100, 2, tasks()...)
	snap.Estimates = map[string]estimate.Estimate{"T-1": {Minutes: estimate.Stat{Median: 1, P80: 2}}}
	if got := Build(snap, Options{}); !reflect.DeepEqual(got, want) {
		t.Fatalf("insufficient history changed the forecast:\n got %+v\nwant %+v", got, want)
	}
}

func TestBuildKeepsProgressExtrapolationOverEstimates(t *testing.T) {
	running := func() *model.Task {
		r := task("T-1", model.StatusInProgress, 20, 2)
		r.ProgressPct = 50
		return r
	}
	want := Build(snapshot(100, 2, running()), Options{})
	snap := snapshot(100, 2, running())
	snap.Estimates = map[string]estimate.Estimate{"T-1": estimated(500, 600, 50, 60)}
	if got := Build(snap, Options{}); !reflect.DeepEqual(got, want) {
		t.Fatalf("estimates overrode observed progress:\n got %+v\nwant %+v", got, want)
	}
}
