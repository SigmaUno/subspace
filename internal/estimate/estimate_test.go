package estimate

import (
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

func near(a, b float64) bool { return math.Abs(a-b) < 0.006 }

func fixture() []Sample {
	var out []Sample
	add := func(role, size, agent, repo string, minutes, cost, rounds float64) {
		out = append(out, Sample{TaskID: "T-" + strconv.Itoa(len(out)+1), Role: role, Size: size, Agent: agent, Repo: repo,
			Minutes: minutes, Cost: cost, Rounds: rounds})
	}
	for i, m := range []float64{10, 20, 30, 40} {
		add("worker", SizeSmall, "claude-code/m"+strconv.Itoa(1+i/2*2), "o/a", m, float64(i+1), 0)
	}
	for i, m := range []float64{100, 120, 140, 160} {
		add("worker", SizeLarge, "codex/m2", "o/b", m, float64(10+i), 1)
	}
	return out
}

func TestEstimateShrinksGroupsTowardTheirParent(t *testing.T) {
	m := Fit(fixture(), Options{})
	tests := []struct {
		name       string
		key        Key
		minutes    Stat
		cost       Stat
		rounds     Stat
		confidence string
		basis      []string
	}{
		{"global only for an unseen role", Key{Role: "tester", Size: SizeSmall},
			Stat{70, 132}, Stat{7, 11.6}, Stat{0.5, 1}, ConfidenceLow, []string{}},
		{"small tasks lean toward the small group", Key{Role: "worker", Size: SizeSmall},
			Stat{44.29, 76}, Stat{4.43, 6.91}, Stat{0.21, 0.43}, ConfidenceMedium, []string{"role", "size"}},
		{"a group as large as its parent changes nothing", Key{Role: "worker", Size: SizeLarge, Agent: "codex/m2"},
			Stat{104.29, 141.14}, Stat{9.57, 12.06}, Stat{0.79, 1}, ConfidenceMedium, []string{"role", "size", "agent"}},
		{"an agent with two samples refines its size group", Key{Role: "worker", Size: SizeSmall, Agent: "claude-code/m1"},
			Stat{32.57, 52.8}, Stat{3.26, 4.87}, Stat{0.13, 0.26}, ConfidenceLow, []string{"role", "size", "agent"}},
		{"an agent without samples in the size group adds no level", Key{Role: "worker", Size: SizeSmall, Agent: "codex/m2"},
			Stat{44.29, 76}, Stat{4.43, 6.91}, Stat{0.21, 0.43}, ConfidenceMedium, []string{"role", "size"}},
		{"an unseen repo adds no level", Key{Role: "worker", Size: SizeLarge, Repo: "o/none"},
			Stat{104.29, 141.14}, Stat{9.57, 12.06}, Stat{0.79, 1}, ConfidenceMedium, []string{"role", "size"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := m.Estimate(tc.key)
			if !e.Sufficient || e.Confidence != tc.confidence {
				t.Fatalf("estimate: %+v", e)
			}
			if !near(e.Minutes.Median, tc.minutes.Median) || !near(e.Minutes.P80, tc.minutes.P80) {
				t.Errorf("minutes %+v want %+v", e.Minutes, tc.minutes)
			}
			if !near(e.CostEUR.Median, tc.cost.Median) || !near(e.CostEUR.P80, tc.cost.P80) {
				t.Errorf("cost %+v want %+v", e.CostEUR, tc.cost)
			}
			if !near(e.ReviewRounds.Median, tc.rounds.Median) || !near(e.ReviewRounds.P80, tc.rounds.P80) {
				t.Errorf("rounds %+v want %+v", e.ReviewRounds, tc.rounds)
			}
			var groups []string
			for _, l := range e.Basis {
				groups = append(groups, l.Group)
			}
			if !slices.Equal(groups, tc.basis) {
				t.Errorf("basis %v want %v", groups, tc.basis)
			}
			if e.Tokens != (Stat{}) {
				t.Errorf("tokens without usage: %+v", e.Tokens)
			}
		})
	}
}

func TestEstimateNeedsEnoughHistory(t *testing.T) {
	m := Fit(fixture()[:4], Options{})
	e := m.Estimate(Key{Role: "worker", Size: SizeSmall})
	if e.Sufficient || e.Confidence != ConfidenceNone || e.Minutes != (Stat{}) || len(e.Basis) != 0 {
		t.Fatalf("estimate from 4 samples: %+v", e)
	}
	if !Fit(fixture()[:4], Options{MinSamples: 2}).Estimate(Key{Role: "worker", Size: SizeSmall}).Sufficient {
		t.Fatal("MinSamples must be configurable")
	}
}

func TestEstimateIgnoresUnknownMetricsAndKeepsZeroRounds(t *testing.T) {
	var samples []Sample
	for i := range 6 {
		s := Sample{TaskID: "T-" + strconv.Itoa(i+1), Role: "worker", Size: SizeSmall, Minutes: float64(10 * (i + 1))}
		if i >= 3 {
			s.Cost = 2
		}
		samples = append(samples, s)
	}
	e := Fit(samples, Options{}).Estimate(Key{Role: "worker", Size: SizeSmall})
	if e.CostEUR.Median != 2 || e.CostEUR.P80 != 2 {
		t.Fatalf("zero costs must not count: %+v", e.CostEUR)
	}
	if e.ReviewRounds != (Stat{}) {
		t.Fatalf("zero review rounds are real: %+v", e.ReviewRounds)
	}
}

func TestEstimateIsIndependentOfSampleOrder(t *testing.T) {
	base := fixture()
	want := Fit(base, Options{}).Estimate(Key{Role: "worker", Size: SizeLarge, Agent: "codex/m2", Repo: "o/b"})
	rng := rand.New(rand.NewPCG(7, 11))
	for range 20 {
		shuffled := slices.Clone(base)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		got := Fit(shuffled, Options{}).Estimate(Key{Role: "worker", Size: SizeLarge, Agent: "codex/m2", Repo: "o/b"})
		if !slices.Equal(got.Basis, want.Basis) || got.Minutes != want.Minutes || got.CostEUR != want.CostEUR {
			t.Fatalf("order changed the estimate: %+v vs %+v", got, want)
		}
	}
}

func TestFeaturesSize(t *testing.T) {
	task := func(spec int, criteria int, paths int) *model.Task {
		t := &model.Task{Spec: strings.Repeat("x", spec), Paths: make(model.Paths, paths)}
		for range criteria {
			t.AcceptanceCriteria = append(t.AcceptanceCriteria, "ok")
		}
		return t
	}
	tests := []struct {
		name string
		task *model.Task
		want string
	}{
		{"short spec, one criterion, no paths", task(100, 1, 0), SizeSmall},
		{"blank criteria do not count", &model.Task{Spec: "s", AcceptanceCriteria: []string{" ", ""}}, SizeSmall},
		{"mid length, few criteria and paths", task(700, 2, 1), SizeMedium},
		{"long spec alone", task(1600, 0, 0), SizeMedium},
		{"long spec with many criteria", task(1600, 7, 0), SizeLarge},
		{"everything large", task(5000, 8, 12), SizeLarge},
	}
	for _, tc := range tests {
		if got := FeaturesOf(tc.task).Size(); got != tc.want {
			t.Errorf("%s: %s want %s (%+v)", tc.name, got, tc.want, FeaturesOf(tc.task))
		}
	}
}

func TestWorkMinutesSumsInProgressSpans(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	ev := func(id int64, min int, status string) *model.AuditEvent {
		return &model.AuditEvent{ID: id, TS: t0.Add(time.Duration(min) * time.Minute), After: map[string]any{"status": status}}
	}
	tests := []struct {
		name   string
		events []*model.AuditEvent
		want   float64
	}{
		{"none", nil, 0},
		{"claim to submit", []*model.AuditEvent{ev(1, 0, "in_progress"), ev(2, 30, "submitted")}, 30},
		{"rework counts every claim", []*model.AuditEvent{
			ev(1, 0, "in_progress"), ev(2, 30, "submitted"), ev(3, 40, "changes_requested"),
			ev(4, 50, "in_progress"), ev(5, 65, "submitted")}, 45},
		{"blocked time is not work", []*model.AuditEvent{
			ev(1, 0, "in_progress"), ev(2, 10, "blocked"), ev(3, 100, "in_progress"), ev(4, 105, "submitted")}, 15},
		{"events out of order", []*model.AuditEvent{ev(2, 30, "submitted"), ev(1, 0, "in_progress")}, 30},
		{"unfinished span is ignored", []*model.AuditEvent{ev(1, 0, "in_progress")}, 0},
	}
	for _, tc := range tests {
		if got := WorkMinutes(tc.events); !near(got, tc.want) {
			t.Errorf("%s: %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestSummarizeTotalsAndBudgetWarnings(t *testing.T) {
	tasks := []*model.Task{
		{ID: "T-1", Title: "a", Role: "worker", Status: "backlog"},
		{ID: "T-2", Title: "b", Role: "worker", Status: "backlog", DependsOn: []string{"T-1"}, Budget: model.Budget{MaxMinutes: 15, MaxCostEUR: 5}},
		{ID: "T-3", Title: "c", Role: "tester", Status: "backlog"},
		{ID: "T-4", Title: "dropped", Role: "worker", Status: "cancelled"},
		{ID: "T-5", Title: "no estimate", Role: "worker", Status: "backlog"},
	}
	est := func(min, minP80, cost, costP80 float64) Estimate {
		return Estimate{Sufficient: true, Confidence: ConfidenceMedium,
			Minutes: Stat{min, minP80}, CostEUR: Stat{cost, costP80}}
	}
	got := Summarize("P-1", tasks, map[string]Estimate{
		"T-1": est(10, 14, 1, 2),
		"T-2": est(20, 26, 6, 9),
		"T-3": est(5, 5, 0.5, 0.5),
	}, 42)
	if got.ID != "P-1" || got.History != 42 || !got.Sufficient || len(got.Tasks) != 4 {
		t.Fatalf("report: %+v", got)
	}
	tot := got.Totals
	if tot.Tasks != 4 || tot.Estimated != 3 {
		t.Fatalf("counts: %+v", tot)
	}
	if want := (Range{27.79, 35, 42.21}); !near(tot.Minutes.Low, want.Low) || tot.Minutes.Expected != want.Expected || !near(tot.Minutes.High, want.High) {
		t.Errorf("minutes %+v want %+v", tot.Minutes, want)
	}
	if want := (Range{22.79, 30, 37.21}); !near(tot.CriticalPathMinutes.Low, want.Low) || tot.CriticalPathMinutes.Expected != want.Expected || !near(tot.CriticalPathMinutes.High, want.High) {
		t.Errorf("critical path %+v want %+v", tot.CriticalPathMinutes, want)
	}
	if want := (Range{4.34, 7.5, 10.66}); !near(tot.CostEUR.Low, want.Low) || tot.CostEUR.Expected != want.Expected || !near(tot.CostEUR.High, want.High) {
		t.Errorf("cost %+v want %+v", tot.CostEUR, want)
	}
	if tot.Tokens != (Range{}) {
		t.Errorf("tokens: %+v", tot.Tokens)
	}
	over := map[string][]string{}
	for _, te := range got.Tasks {
		over[te.TaskID] = te.ExceedsBudget
	}
	if !slices.Equal(over["T-2"], []string{BudgetMinutes, BudgetCost}) || len(over["T-1"]) != 0 || over["T-5"] == nil {
		t.Errorf("budget warnings: %v", over)
	}
	if te := got.Tasks[3]; te.TaskID != "T-5" || te.Estimate.Confidence != ConfidenceNone || te.Estimate.Basis == nil {
		t.Errorf("task without estimate: %+v", te)
	}
}

func TestSummarizeLowIsNeverNegative(t *testing.T) {
	tasks := []*model.Task{{ID: "T-1", Status: "backlog"}}
	got := Summarize("B-1", tasks, map[string]Estimate{"T-1": {Sufficient: true, Minutes: Stat{1, 50}}}, 9)
	if got.Totals.Minutes.Low != 0 || got.Totals.Minutes.High < 50 {
		t.Fatalf("range: %+v", got.Totals.Minutes)
	}
}

func TestSummarizeWithoutHistory(t *testing.T) {
	got := Summarize("P-1", []*model.Task{{ID: "T-1", Status: "backlog"}}, nil, 0)
	if got.Sufficient || got.Totals.Estimated != 0 || got.Totals.Tasks != 1 || got.Tasks[0].Estimate.Sufficient {
		t.Fatalf("report: %+v", got)
	}
}

func TestSizeClassFollowsDocumentedEdges(t *testing.T) {
	tests := []struct {
		f    Features
		want string
	}{
		{Features{SpecLength: 200, Criteria: 4}, SizeSmall},
		{Features{SpecLength: 201, Criteria: 4}, SizeMedium},
		{Features{SpecLength: 600, Criteria: 2}, SizeSmall},
		{Features{SpecLength: 601, Criteria: 2}, SizeMedium},
		{Features{SpecLength: 1500}, SizeSmall},
		{Features{SpecLength: 1501}, SizeMedium},
		{Features{SpecLength: 4000, Criteria: 4}, SizeMedium},
		{Features{SpecLength: 4001, Criteria: 4}, SizeLarge},
		{Features{SpecLength: 1500, Criteria: 1}, SizeSmall},
		{Features{SpecLength: 1500, Criteria: 2}, SizeMedium},
		{Features{SpecLength: 600, Criteria: 3}, SizeSmall},
		{Features{SpecLength: 600, Criteria: 4}, SizeMedium},
		{Features{SpecLength: 4000, Criteria: 6}, SizeMedium},
		{Features{SpecLength: 4000, Criteria: 7}, SizeLarge},
		{Features{SpecLength: 1500, Paths: 0}, SizeSmall},
		{Features{SpecLength: 1500, Paths: 1}, SizeMedium},
		{Features{SpecLength: 600, Paths: 2}, SizeSmall},
		{Features{SpecLength: 600, Paths: 3}, SizeMedium},
		{Features{SpecLength: 4000, Paths: 9}, SizeMedium},
		{Features{SpecLength: 4000, Paths: 10}, SizeLarge},
	}
	for _, tc := range tests {
		if got := tc.f.Size(); got != tc.want {
			t.Errorf("%+v: %s want %s", tc.f, got, tc.want)
		}
	}
	got := FeaturesOf(&model.Task{Spec: "  héllo ", AcceptanceCriteria: []string{" ab ", "  ", "ç"}, Paths: model.Paths{"a", "b"}})
	if got != (Features{SpecLength: 8, Criteria: 2, Paths: 2}) {
		t.Fatalf("features %+v", got)
	}
}

func TestConfidenceFollowsTheDeepestGroup(t *testing.T) {
	samples := func(workers int) []Sample {
		var out []Sample
		for i := range 10 + workers {
			role := "tester"
			if i >= 10 {
				role = "worker"
			}
			out = append(out, Sample{TaskID: "T-" + strconv.Itoa(i+1), Role: role, Size: SizeSmall, Minutes: 10})
		}
		return out
	}
	for workers, want := range map[int]string{2: ConfidenceLow, 3: ConfidenceMedium, 9: ConfidenceMedium, 10: ConfidenceHigh} {
		if got := Fit(samples(workers), Options{}).Estimate(Key{Role: "worker", Size: SizeSmall}).Confidence; got != want {
			t.Errorf("%d samples in the deepest group: %s want %s", workers, got, want)
		}
	}
	if e := Fit(samples(2), Options{}).Estimate(Key{Role: "nobody", Size: SizeSmall}); e.Confidence != ConfidenceLow || len(e.Basis) != 0 {
		t.Errorf("no matching group: %+v", e)
	}
}

func TestEstimateOptionsAndPercentiles(t *testing.T) {
	var five []Sample
	for i := range 5 {
		five = append(five, Sample{TaskID: "T-" + strconv.Itoa(i+1), Role: "worker", Minutes: float64(10 * (i + 1))})
	}
	if !Fit(five, Options{}).Sufficient() || Fit(five[:4], Options{}).Sufficient() {
		t.Fatal("default minimum is 5 samples")
	}
	if !Fit(five[:1], Options{MinSamples: 1}).Sufficient() {
		t.Fatal("MinSamples 1 ignored")
	}
	if e := Fit(five, Options{}).Estimate(Key{Role: "other"}); e.Minutes != (Stat{Median: 30, P80: 42}) {
		t.Fatalf("root percentiles %+v", e.Minutes)
	}

	var mixed []Sample
	for i := range 12 {
		s := Sample{TaskID: "T-" + strconv.Itoa(i+1), Role: "tester", Minutes: 100}
		if i >= 10 {
			s.Role, s.Minutes = "worker", 10
		}
		mixed = append(mixed, s)
	}
	if got := Fit(mixed, Options{}).Estimate(Key{Role: "worker"}).Minutes.Median; got != 64 {
		t.Errorf("default prior weight: median %v want 64", got)
	}
	if got := Fit(mixed, Options{PriorWeight: 1}).Estimate(Key{Role: "worker"}).Minutes.Median; got != 40 {
		t.Errorf("prior weight 1: median %v want 40", got)
	}
}

func TestWorkMinutesOrderingAndRepeatedClaims(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	ev := func(id int64, min int, status string) *model.AuditEvent {
		return &model.AuditEvent{ID: id, TS: t0.Add(time.Duration(min) * time.Minute), After: map[string]any{"status": status}}
	}
	if got := WorkMinutes([]*model.AuditEvent{ev(1, 30, "submitted"), ev(2, 0, "in_progress")}); got != 30 {
		t.Errorf("time order beats id order: %v", got)
	}
	if got := WorkMinutes([]*model.AuditEvent{ev(2, 0, "submitted"), ev(1, 0, "in_progress"), ev(3, 20, "submitted")}); got != 0 {
		t.Errorf("same instant falls back to id order: %v", got)
	}
	if got := WorkMinutes([]*model.AuditEvent{ev(1, 0, "in_progress"), ev(2, 10, "in_progress"), ev(3, 30, "submitted")}); got != 30 {
		t.Errorf("repeated in_progress restarted the span: %v", got)
	}
}

func TestBudgetAndCriticalPathEdges(t *testing.T) {
	e := Estimate{Sufficient: true, Minutes: Stat{10, 10}, CostEUR: Stat{2, 2}}
	if got := Exceeds(&model.Task{Budget: model.Budget{MaxMinutes: 10, MaxCostEUR: 2}}, e); len(got) != 0 {
		t.Errorf("estimate equal to the budget exceeds it: %v", got)
	}
	if got := Exceeds(&model.Task{Budget: model.Budget{MaxMinutes: 1, MaxCostEUR: 0.5}}, Estimate{Sufficient: true, Minutes: Stat{2, 2}, CostEUR: Stat{1, 1}}); !slices.Equal(got, []string{BudgetMinutes, BudgetCost}) {
		t.Errorf("small budgets: %v", got)
	}
	tasks := []*model.Task{{ID: "T-1", Status: "backlog", DependsOn: []string{"T-1", "T-9"}}, {ID: "T-2", Status: "backlog"}}
	r := Summarize("B-1", tasks, map[string]Estimate{"T-1": e, "T-2": {Sufficient: true, Minutes: Stat{4, 4}}}, 7)
	if !r.Sufficient || r.Totals.CriticalPathMinutes != (Range{10, 10, 10}) {
		t.Fatalf("critical path %+v sufficient %v", r.Totals.CriticalPathMinutes, r.Sufficient)
	}
	one := Summarize("B-1", tasks[1:], map[string]Estimate{"T-2": {Sufficient: true, Minutes: Stat{4, 4}}}, 7)
	if !one.Sufficient || one.Totals.Estimated != 1 {
		t.Fatalf("one estimated task: %+v", one.Totals)
	}
}

func TestSummarizeOrdersTasksByNumericID(t *testing.T) {
	var tasks []*model.Task
	for _, id := range []string{"T-10", "T-2", "T-1", "T-11", "T-3"} {
		tasks = append(tasks, &model.Task{ID: id, Status: "backlog"})
	}
	var got []string
	for _, te := range Summarize("B-1", tasks, nil, 0).Tasks {
		got = append(got, te.TaskID)
	}
	if want := []string{"T-1", "T-2", "T-3", "T-10", "T-11"}; !slices.Equal(got, want) {
		t.Fatalf("order %v want %v", got, want)
	}
}
