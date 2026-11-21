package forecast

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/estimate"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/usage"
)

func finishing(id string, deps ...string) *model.Task {
	t := task(id, model.StatusInProgress, 0, 0, deps...)
	t.ProgressPct = 100
	return t
}

func closeTo(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func flat(f Range, v float64) bool {
	return closeTo(f.P10, v) && closeTo(f.P50, v) && closeTo(f.P90, v)
}

func TestPriorsFromHistory(t *testing.T) {
	f := Build(snapshot(0, 0, task("T-1", model.StatusDone, 30, 3), task("T-2", model.StatusApproved, 90, 9), finishing("T-3")), Options{})
	if !flat(f.Schedule.RemainingMinutes, 60) || !flat(f.Cost.Projected, 18) {
		t.Fatalf("history means: minutes %+v cost %+v", f.Schedule.RemainingMinutes, f.Cost.Projected)
	}

	used := finishing("T-3")
	used.MinutesUsed, used.CostUsed = 20, 4
	f = Build(snapshot(0, 0, task("T-1", model.StatusDone, 40, 0), used, finishing("T-4")), Options{})
	if !flat(f.Schedule.RemainingMinutes, 40) || !flat(f.Cost.Projected, 4+40*4.0/60) {
		t.Fatalf("cost per minute: minutes %+v cost %+v", f.Schedule.RemainingMinutes, f.Cost.Projected)
	}

	f = Build(snapshot(0, 0, withBudget(finishing("T-1"), 30, 8)), Options{})
	if !flat(f.Schedule.RemainingMinutes, 15) || !flat(f.Cost.Projected, 4) {
		t.Fatalf("budget fallback: minutes %+v cost %+v", f.Schedule.RemainingMinutes, f.Cost.Projected)
	}
	f = Build(snapshot(0, 0, finishing("T-1")), Options{})
	if !flat(f.Schedule.RemainingMinutes, 60) || !flat(f.Cost.Projected, 0) {
		t.Fatalf("defaults: minutes %+v cost %+v", f.Schedule.RemainingMinutes, f.Cost.Projected)
	}
	f = Build(snapshot(0, 0, withBudget(finishing("T-1"), 1, 0.5)), Options{})
	if !flat(f.Schedule.RemainingMinutes, 0.5) || !flat(f.Cost.Projected, 0.25) {
		t.Fatalf("tiny budget: minutes %+v cost %+v", f.Schedule.RemainingMinutes, f.Cost.Projected)
	}
	f = Build(snapshot(0, 0, task("T-1", model.StatusDone, 1, 0.5), finishing("T-2")), Options{})
	if !flat(f.Schedule.RemainingMinutes, 1) || !flat(f.Cost.Projected, 1) {
		t.Fatalf("tiny history: minutes %+v cost %+v", f.Schedule.RemainingMinutes, f.Cost.Projected)
	}
	f = Build(snapshot(0, 0, task("T-1", model.StatusDone, 1, 0.5), task("T-3", model.StatusDone, 3, 0), finishing("T-2")), Options{})
	if !flat(f.Schedule.RemainingMinutes, 2) || !flat(f.Cost.Projected, 1) {
		t.Fatalf("single cost sample: minutes %+v cost %+v", f.Schedule.RemainingMinutes, f.Cost.Projected)
	}
	paid := finishing("T-1")
	paid.CostUsed = 2
	f = Build(snapshot(0, 0, paid, finishing("T-2")), Options{})
	if !flat(f.Cost.Projected, 2) {
		t.Fatalf("cost without minutes: %+v", f.Cost.Projected)
	}
	almost := finishing("T-2")
	almost.MinutesUsed, almost.CostUsed = 20, 4
	f = Build(snapshot(0, 0, task("T-1", model.StatusDone, 60, 9), almost), Options{})
	if !flat(f.Schedule.RemainingMinutes, 0) || !flat(f.Cost.Projected, 13) {
		t.Fatalf("finished by progress: minutes %+v cost %+v", f.Schedule.RemainingMinutes, f.Cost.Projected)
	}
	stalled := task("T-2", model.StatusInProgress, 10, 1)
	f = Build(snapshot(0, 0, task("T-1", model.StatusDone, 60, 9), stalled), Options{Runs: 200})
	if math.IsInf(f.Schedule.RemainingMinutes.P90, 0) || f.Schedule.RemainingMinutes.P90 > 1000 || math.IsInf(f.Cost.Projected.P90, 0) || f.Cost.Projected.P90 > 1000 {
		t.Fatalf("zero progress with usage: minutes %+v cost %+v", f.Schedule.RemainingMinutes, f.Cost.Projected)
	}
}

func TestSpreadComesFromHistory(t *testing.T) {
	median := func(tasks ...*model.Task) float64 {
		return Build(snapshot(0, 0, append(tasks, task("T-9", model.StatusReady, 0, 0))...), Options{Runs: 20000, Seed: 7}).Schedule.RemainingMinutes.P50
	}
	within := func(got, want float64) bool { return math.Abs(got-want) < want*0.02 }
	if got, want := median(), 60*math.Exp(-0.125); !within(got, want) {
		t.Errorf("default spread: median %v want %v", got, want)
	}
	two := median(task("T-1", model.StatusDone, 30, 0), task("T-2", model.StatusDone, 120, 0))
	if want := 75 * math.Exp(-math.Pow(math.Log(4)/math.Sqrt2, 2)/2); !within(two, want) {
		t.Errorf("two samples: median %v want %v", two, want)
	}
	three := median(task("T-1", model.StatusDone, 30, 0), task("T-2", model.StatusDone, 60, 0), task("T-3", model.StatusDone, 120, 0))
	if want := 70 * math.Exp(-math.Pow(math.Log(2), 2)/2); !within(three, want) {
		t.Errorf("three samples: median %v want %v", three, want)
	}
}

func TestParallelismAndDependencies(t *testing.T) {
	three := func(parallel int) Forecast {
		return Build(snapshot(0, parallel, finishing("T-1"), finishing("T-2"), finishing("T-3")), Options{})
	}
	for parallel, span := range map[int]float64{0: 60, 1: 180, 2: 120, 3: 60, 9: 60} {
		f := three(parallel)
		if !flat(f.Schedule.RemainingMinutes, span) || !flat(f.Schedule.CriticalPathMinutes, 60) {
			t.Errorf("parallel %d: span %+v path %+v", parallel, f.Schedule.RemainingMinutes, f.Schedule.CriticalPathMinutes)
		}
	}

	f := Build(snapshot(0, 3, finishing("T-1"), finishing("T-2", "T-1"), finishing("T-10", "T-10", "T-99")), Options{})
	if !flat(f.Schedule.RemainingMinutes, 120) || !flat(f.Schedule.CriticalPathMinutes, 120) {
		t.Fatalf("chain: span %+v path %+v", f.Schedule.RemainingMinutes, f.Schedule.CriticalPathMinutes)
	}

	f = Build(snapshot(0, 0, finishing("T-1", "T-2"), finishing("T-2", "T-1"), finishing("T-3")), Options{})
	if !flat(f.Schedule.RemainingMinutes, 60) || f.Tasks.Remaining != 3 {
		t.Fatalf("cycle: span %+v tasks %+v", f.Schedule.RemainingMinutes, f.Tasks)
	}
	f = Build(snapshot(0, 0, finishing("T-1"), finishing("T-2", "T-99")), Options{})
	if !flat(f.Schedule.RemainingMinutes, 60) {
		t.Fatalf("unknown and self dependencies: span %+v", f.Schedule.RemainingMinutes)
	}

	f = Build(snapshot(0, 3, task("T-1", model.StatusSubmitted, 0, 0), finishing("T-2", "T-1"), task("T-3", model.StatusDone, 0, 0)), Options{})
	if !flat(f.Schedule.CriticalPathMinutes, 70) || f.Tasks != (Counts{Total: 3, Complete: 2, Remaining: 1}) {
		t.Fatalf("review then work: path %+v tasks %+v", f.Schedule.CriticalPathMinutes, f.Tasks)
	}
	if want := t0.Add(70 * time.Minute); !f.Schedule.CompletesAt.P10.Equal(want) || !f.Schedule.CompletesAt.P90.Equal(want) {
		t.Fatalf("completes at %+v", f.Schedule.CompletesAt)
	}
}

func TestCostRiskBoundaries(t *testing.T) {
	f := Build(snapshot(10, 0, task("T-1", model.StatusDone, 30, 10)), Options{})
	if len(f.Risks) != 0 || f.CostOverrun() || f.Cost.ExceedProbability != 0 {
		t.Fatalf("spend equal to the budget: %v %+v", codes(f), f.Cost)
	}
	f = Build(snapshot(10, 0, task("T-1", model.StatusDone, 30, 4), task("T-2", model.StatusDone, 30, 8), finishing("T-3")), Options{})
	if !reflect.DeepEqual(codes(f), []string{"cost_exceeded:high"}) {
		t.Fatalf("over budget: %v", codes(f))
	}
	f = Build(snapshot(10, 0, task("T-1", model.StatusDone, 30, 6), finishing("T-2")), Options{})
	if !reflect.DeepEqual(codes(f), []string{"cost_overrun:high"}) || f.Cost.ExceedProbability != 1 || !f.CostOverrun() {
		t.Fatalf("certain overrun: %v %+v", codes(f), f.Cost)
	}
}

func TestPlannedCriticalPath(t *testing.T) {
	half := withBudget(finishing("T-2", "T-1"), 30, 0)
	half.MinutesUsed = 10
	half.ProgressPct = 0
	f := Build(snapshot(0, 0, withBudget(finishing("T-1"), 40, 0), half), Options{Runs: 50})
	if f.Schedule.PlannedCriticalPath != 60 {
		t.Fatalf("planned path %v", f.Schedule.PlannedCriticalPath)
	}

	f = Build(snapshot(0, 0, task("T-1", model.StatusDone, 120, 0), withBudget(finishing("T-2"), 60, 0)), Options{})
	if !reflect.DeepEqual(codes(f), []string{"critical_path_slip:high"}) {
		t.Fatalf("slip: %v path %+v planned %v", codes(f), f.Schedule.CriticalPathMinutes, f.Schedule.PlannedCriticalPath)
	}
	f = Build(snapshot(0, 0, task("T-1", model.StatusDone, 60, 0), withBudget(finishing("T-2"), 60, 0)), Options{})
	if len(f.Risks) != 0 {
		t.Fatalf("on plan: %v", codes(f))
	}
	over := withBudget(finishing("T-2", "T-1"), 30, 0)
	over.MinutesUsed, over.ProgressPct = 40, 0
	if f = Build(snapshot(0, 0, withBudget(finishing("T-1"), 1, 0), over), Options{Runs: 20}); f.Schedule.PlannedCriticalPath != 1 {
		t.Fatalf("planned path with an overrun task: %v", f.Schedule.PlannedCriticalPath)
	}
	f = Build(snapshot(0, 0, task("T-1", model.StatusDone, 120, 0), withBudget(finishing("T-2"), 60, 0), finishing("T-3")), Options{})
	if len(f.Risks) != 0 || f.Schedule.PlannedCriticalPath != 0 {
		t.Fatalf("unplanned task: %v planned %v", codes(f), f.Schedule.PlannedCriticalPath)
	}
}

func TestRunsAndSeed(t *testing.T) {
	s := snapshot(0, 0, finishing("T-1"))
	for runs, want := range map[int]int{0: 1000, -3: 1000, 1: 1, 7: 7, 20000: 20000, 50000: 20000} {
		if got := Build(s, Options{Runs: runs}).Runs; got != want {
			t.Errorf("runs %d: %d want %d", runs, got, want)
		}
	}
	a, b := Build(s, Options{}), Build(s, Options{Seed: 42})
	if a.Seed == 0 || a.Seed != Build(s, Options{}).Seed || b.Seed != 42 {
		t.Fatalf("seeds %d %d", a.Seed, b.Seed)
	}
}

func TestEstimateShapesExpectation(t *testing.T) {
	s := snapshot(0, 0, finishing("T-1"))
	at := func(e estimate.Estimate) Forecast {
		s.Estimates = map[string]estimate.Estimate{"T-1": e}
		return Build(s, Options{})
	}
	f := at(estimated(20, 20, 4, 3))
	if !flat(f.Schedule.RemainingMinutes, 20*math.Exp(0.005)) || !flat(f.Cost.Projected, 4*math.Exp(0.005)) {
		t.Fatalf("narrow estimate: %+v %+v", f.Schedule.RemainingMinutes, f.Cost.Projected)
	}
	f = at(estimated(20, 20*math.Exp(0.3*z80), 4, 4*math.Exp(0.3*z80)))
	if !flat(f.Schedule.RemainingMinutes, 20*math.Exp(0.045)) || !flat(f.Cost.Projected, 4*math.Exp(0.045)) {
		t.Fatalf("sigma 0.3: %+v %+v", f.Schedule.RemainingMinutes, f.Cost.Projected)
	}
	f = at(estimated(20, 2000, 4, 400))
	if !flat(f.Schedule.RemainingMinutes, 20*math.Exp(0.5)) || !flat(f.Cost.Projected, 4*math.Exp(0.5)) {
		t.Fatalf("capped sigma: %+v %+v", f.Schedule.RemainingMinutes, f.Cost.Projected)
	}
	f = at(estimated(20, 21*math.Exp(0.01), 4, 4.01*math.Exp(0.01)))
	if !flat(f.Schedule.RemainingMinutes, 20*math.Exp(0.005)) {
		t.Fatalf("sigma floor: %+v", f.Schedule.RemainingMinutes)
	}
	f = at(estimated(0.5, 50, 0, 0))
	if !flat(f.Schedule.RemainingMinutes, 0.5*math.Exp(0.5)) {
		t.Fatalf("sub-minute estimate: %+v", f.Schedule.RemainingMinutes)
	}
	used := finishing("T-1")
	used.MinutesUsed, used.CostUsed = 1, 0.5
	s = snapshot(0, 0, used)
	if f = at(estimated(20, 20, 4, 4)); !flat(f.Schedule.RemainingMinutes, 0) || !flat(f.Cost.Projected, 0.5) {
		t.Fatalf("progress beats estimate: %+v %+v", f.Schedule.RemainingMinutes, f.Cost.Projected)
	}
	s = snapshot(0, 0, task("T-0", model.StatusDone, 60, 9), finishing("T-1"))
	if f = at(estimated(20, 20, 0, 0)); !flat(f.Cost.Projected, 18) {
		t.Fatalf("estimate without cost keeps the cost prior: %+v", f.Cost.Projected)
	}
	s = snapshot(0, 0, finishing("T-1"))
	f = at(estimated(0, 0, 0, 0))
	if !flat(f.Schedule.RemainingMinutes, 60) || !flat(f.Cost.Projected, 0) {
		t.Fatalf("empty estimate: %+v %+v", f.Schedule.RemainingMinutes, f.Cost.Projected)
	}
}

func TestBurnRates(t *testing.T) {
	claimed := func(tk *model.Task, ago time.Duration) *model.Task {
		c := t0.Add(-ago)
		tk.ClaimedAt = &c
		return tk
	}
	s := snapshot(20, 0, claimed(task("T-1", model.StatusDone, 30, 2), 2*time.Hour), claimed(task("T-2", model.StatusApproved, 30, 2), time.Hour), claimed(task("T-3", model.StatusInProgress, 30, 4), 3*time.Hour))
	b := Build(s, Options{}).Burn
	if b.Source != "tasks" || !closeTo(b.TasksPerHour, 2.0/3) || !closeTo(b.EURPerHour, 8.0/3) || b.RunwayHours == nil || !closeTo(*b.RunwayHours, 12/(8.0/3)) {
		t.Fatalf("task burn %+v", b)
	}
	s.Usage = []usage.Record{
		{TS: t0.Add(-30 * time.Minute), CostUSD: 5.5},
		{TS: t0.Add(-15 * time.Minute), CostUSD: 5.5},
		{TS: t0.Add(-2 * time.Hour), CostUSD: 100},
		{TS: t0.Add(time.Minute), CostUSD: 100},
	}
	b = Build(s, Options{EURToUSD: 1.1}).Burn
	if b.Source != "usage" || !closeTo(b.EURPerHour, 20) || !closeTo(*b.RunwayHours, 12.0/20) {
		t.Fatalf("usage burn %+v", b)
	}
	if b = Build(s, Options{}).Burn; !closeTo(b.EURPerHour, 22) {
		t.Fatalf("default rate burn %+v", b)
	}
	s.Brief.Budget.MaxCostEUR = 5
	if b = Build(s, Options{}).Burn; b.RunwayHours == nil || *b.RunwayHours != 0 {
		t.Fatalf("runway past the budget %+v", b)
	}
	if b = Build(s, Options{EURToUSD: 0.5}).Burn; !closeTo(b.EURPerHour, 44) {
		t.Fatalf("half rate burn %+v", b)
	}
	s.Brief.Budget.MaxCostEUR = 0.5
	if b = Build(s, Options{}).Burn; b.RunwayHours == nil {
		t.Fatalf("no runway under a small budget %+v", b)
	}
	s.Brief.Budget.MaxCostEUR = 0
	f := Build(s, Options{})
	if b = f.Burn; b.RunwayHours != nil {
		t.Fatalf("runway without a budget %+v", b)
	}
	if len(f.Risks) != 0 || f.CostOverrun() || f.Cost.ExceedProbability != 0 {
		t.Fatalf("cost risk without a budget: %v %+v", codes(f), f.Cost)
	}
	s.Usage = []usage.Record{{TS: t0.Add(-30 * time.Minute), CostUSD: 0.25}}
	s.Brief.Budget.MaxCostEUR = 100
	if b = Build(s, Options{}).Burn; b.Source != "usage" || !closeTo(b.EURPerHour, 0.5) || b.RunwayHours == nil {
		t.Fatalf("small usage burn %+v", b)
	}

	now := snapshot(0, 0, claimed(task("T-1", model.StatusDone, 30, 1), 0))
	now.Usage = []usage.Record{{TS: t0, CostUSD: 1}}
	if b := Build(now, Options{}).Burn; !closeTo(b.TasksPerHour, 60) || !closeTo(b.EURPerHour, 60) {
		t.Fatalf("burn at the first instant %+v", b)
	}
}
