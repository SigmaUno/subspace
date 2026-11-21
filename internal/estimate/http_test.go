package estimate_test

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/estimate"
	"github.com/SigmaUno/subspace/internal/forecast"
	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type world struct {
	e     *servertest.Env
	clk   *clock
	lead  string
	wk    string
	rev   string
	brief string
}

func start(t *testing.T) *world {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)}
	e := servertest.Start(t, server.Config{PublicURL: "http://sub.test", Now: clk.Now,
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: 48 * time.Hour})
	w := &world{e: e, clk: clk}
	w.lead = e.Register("rex", "leader", "other")
	w.wk = e.Register("w1", "worker", "claude-code")
	w.rev = e.Register("r1", "reviewer", "codex")
	w.brief = w.newBrief()
	return w
}

func (w *world) newBrief() string {
	w.e.T.Helper()
	code, out := w.e.REST("POST", "/api/v1/briefs", map[string]any{"title": "Demo", "body": "do it", "repos": []string{"o/r"},
		"max_cost_eur": 1000.0, "max_parallel_agents": 2})
	if code != 201 {
		w.e.T.Fatalf("create brief: %d %v", code, out)
	}
	return out["id"].(string)
}

func task(ref string, large bool, extra map[string]any) map[string]any {
	spec, criteria, paths := strings.Repeat("s", 100), []string{"works"}, []string{}
	if large {
		spec = strings.Repeat("s", 2500)
		criteria = []string{"a", "b", "c", "d", "e", "f"}
		paths = []string{"internal/a/**", "internal/b/**", "internal/c/**"}
	}
	t := map[string]any{"ref": ref, "title": "task " + ref, "spec": spec, "repo": "o/r", "acceptance_criteria": criteria, "paths": paths}
	for k, v := range extra {
		t[k] = v
	}
	return t
}

func (w *world) propose(brief string, tasks ...map[string]any) (string, []string) {
	w.e.T.Helper()
	in := make([]any, len(tasks))
	for i, t := range tasks {
		in[i] = t
	}
	plan := w.e.Must(w.lead, "propose_plan", map[string]any{"brief_id": brief, "tasks": in})["plan"].(map[string]any)
	var ids []string
	for _, t := range plan["tasks"].([]any) {
		ids = append(ids, t.(map[string]any)["id"].(string))
	}
	return plan["id"].(string), ids
}

func (w *world) approve(plan string) {
	w.e.T.Helper()
	if code, out := w.e.REST("POST", "/api/v1/plans/"+plan+"/approve", map[string]any{}); code != 200 {
		w.e.T.Fatalf("approve %s: %d %v", plan, code, out)
	}
}

func (w *world) work(id string, minutes int, cost float64) {
	w.e.T.Helper()
	w.e.Must(w.wk, "claim_task", map[string]any{"task_id": id})
	w.clk.Advance(time.Duration(minutes) * time.Minute)
	w.e.Must(w.wk, "update_status", map[string]any{"task_id": id, "status": "in_progress", "cost_eur": cost})
	w.e.Must(w.wk, "submit", map[string]any{"task_id": id, "pr_url": "https://git.example/o/r/pulls/" + id[2:], "summary": "done"})
}

func (w *world) complete(id string, large bool, rng *rand.Rand) {
	w.e.T.Helper()
	w.e.Must(w.lead, "assign", map[string]any{"task_id": id, "agent_id": "w1"})
	if large {
		w.work(id, 90+rng.IntN(31), 4+float64(rng.IntN(20))/10)
		w.e.Must(w.rev, "review", map[string]any{"task_id": id, "verdict": "changes_requested", "comments": "fix"})
		w.work(id, 10, 0.5)
	} else {
		w.work(id, 10+rng.IntN(6), 0.5+float64(rng.IntN(5))/10)
	}
	w.e.Must(w.rev, "review", map[string]any{"task_id": id, "verdict": "approve", "comments": "lgtm"})
	if code, out := w.e.REST("POST", "/api/v1/tasks/"+id+"/done", map[string]any{"note": "merged"}); code != 200 {
		w.e.T.Fatalf("done %s: %d %v", id, code, out)
	}
}

func (w *world) history(seed uint64, n int) {
	w.e.T.Helper()
	rng := rand.New(rand.NewPCG(seed, 1))
	var tasks []map[string]any
	var large []bool
	for i := range n {
		isLarge := i%2 == 1
		tasks = append(tasks, task(fmt.Sprintf("h%d", i), isLarge, nil))
		large = append(large, isLarge)
	}
	plan, ids := w.propose(w.brief, tasks...)
	w.approve(plan)
	for i, id := range ids {
		w.complete(id, large[i], rng)
	}
}

func (w *world) get(path, token string) (int, []byte) {
	w.e.T.Helper()
	req, _ := http.NewRequest("GET", w.e.TS.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.e.T.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if ct := resp.Header.Get("Content-Type"); resp.StatusCode == 200 && ct != "application/json" {
		w.e.T.Fatalf("%s: content type %q", path, ct)
	}
	return resp.StatusCode, b
}

func (w *world) report(path string) (estimate.Report, []byte) {
	w.e.T.Helper()
	code, body := w.get(path, servertest.OwnerToken)
	if code != 200 {
		w.e.T.Fatalf("%s: %d %s", path, code, body)
	}
	var r estimate.Report
	if err := json.Unmarshal(body, &r); err != nil {
		w.e.T.Fatal(err)
	}
	return r, body
}

func TestEstimatesLearnFromCompletedTasksOverHTTP(t *testing.T) {
	w := start(t)
	w.history(42, 12)

	next := w.newBrief()
	plan, ids := w.propose(next,
		task("small", false, map[string]any{"budget": map[string]any{"max_minutes": 120, "max_cost_eur": 100.0}}),
		task("large", true, map[string]any{"depends_on": []string{"small"}, "budget": map[string]any{"max_minutes": 30, "max_cost_eur": 0.5}}),
	)

	rep, body := w.report("/api/v1/plans/" + plan + "/estimates")
	if rep.ID != plan || !rep.Sufficient || rep.History != 12 || len(rep.Tasks) != 2 {
		t.Fatalf("plan estimates: %s", body)
	}
	small, large := rep.Tasks[0], rep.Tasks[1]
	if small.TaskID != ids[0] || large.TaskID != ids[1] {
		t.Fatalf("task order: %s", body)
	}
	if !small.Estimate.Sufficient || small.Estimate.Confidence != estimate.ConfidenceMedium {
		t.Fatalf("small estimate: %+v", small.Estimate)
	}
	if small.Estimate.Minutes.Median < 10 || small.Estimate.Minutes.Median > 45 || large.Estimate.Minutes.Median < 80 {
		t.Fatalf("minutes small %+v large %+v", small.Estimate.Minutes, large.Estimate.Minutes)
	}
	if small.Estimate.CostEUR.Median >= large.Estimate.CostEUR.Median || small.Estimate.ReviewRounds.Median >= large.Estimate.ReviewRounds.Median {
		t.Fatalf("large tasks must cost and cycle more: small %+v large %+v", small.Estimate, large.Estimate)
	}
	if small.Estimate.Minutes.P80 < small.Estimate.Minutes.Median || len(small.Estimate.Basis) != 3 {
		t.Fatalf("small basis: %+v", small.Estimate)
	}
	if len(small.ExceedsBudget) != 0 {
		t.Fatalf("small task is within budget: %v", small.ExceedsBudget)
	}
	if strings.Join(large.ExceedsBudget, ",") != estimate.BudgetMinutes+","+estimate.BudgetCost {
		t.Fatalf("large task must exceed its budget: %v", large.ExceedsBudget)
	}

	tot := rep.Totals
	sum := small.Estimate.Minutes.Median + large.Estimate.Minutes.Median
	if tot.Tasks != 2 || tot.Estimated != 2 || math.Abs(tot.Minutes.Expected-sum) > 0.011 {
		t.Fatalf("totals: %+v want minutes %.2f", tot, sum)
	}
	if tot.CriticalPathMinutes.Expected != tot.Minutes.Expected {
		t.Fatalf("a dependency chain is the critical path: %+v", tot)
	}
	if !(tot.CostEUR.Low < tot.CostEUR.Expected && tot.CostEUR.Expected < tot.CostEUR.High) || tot.Minutes.Low >= tot.Minutes.High {
		t.Fatalf("uncertainty range: %+v", tot)
	}

	if _, again := w.report("/api/v1/plans/" + plan + "/estimates"); string(again) != string(body) {
		t.Fatalf("estimates changed between identical requests:\n%s\n%s", body, again)
	}

	brief, _ := w.report("/api/v1/briefs/" + next + "/estimates")
	if brief.ID != next || len(brief.Tasks) != 2 || brief.Totals != rep.Totals || brief.Tasks[1].Estimate.Minutes != large.Estimate.Minutes {
		t.Fatalf("brief estimates differ from plan estimates: %+v", brief)
	}
	if done, _ := w.report("/api/v1/briefs/" + w.brief + "/estimates"); len(done.Tasks) != 0 || done.Totals.Tasks != 0 {
		t.Fatalf("completed tasks are not estimated: %+v", done.Tasks)
	}
}

func TestEstimatesAreDeterministicAcrossRuns(t *testing.T) {
	run := func(seed uint64) []byte {
		w := start(t)
		w.history(seed, 10)
		plan, _ := w.propose(w.newBrief(), task("a", false, nil), task("b", true, nil))
		_, body := w.report("/api/v1/plans/" + plan + "/estimates")
		return body
	}
	first := run(7)
	if second := run(7); string(first) != string(second) {
		t.Fatalf("same seed, different estimates:\n%s\n%s", first, second)
	}
	if other := run(8); string(other) == string(first) {
		t.Fatalf("a different history must change the estimates: %s", first)
	}
}

func TestForecastFollowsEstimatesOnceHistoryIsSufficient(t *testing.T) {
	w := start(t)
	next := w.newBrief()
	budget := map[string]any{"budget": map[string]any{"max_minutes": 600, "max_cost_eur": 100.0}}
	plan, _ := w.propose(next, task("a", false, budget), task("b", true, budget))
	w.approve(plan)

	fetch := func() forecast.Forecast {
		t.Helper()
		code, body := w.get("/api/v1/briefs/"+next+"/forecast", servertest.OwnerToken)
		if code != 200 {
			t.Fatalf("forecast: %d %s", code, body)
		}
		var f forecast.Forecast
		if err := json.Unmarshal(body, &f); err != nil {
			t.Fatal(err)
		}
		return f
	}
	before := fetch()
	if before.Cost.Projected.P50 < 50 {
		t.Fatalf("without history the forecast follows the task budgets: %+v", before.Cost)
	}

	w.history(3, 12)
	after := fetch()
	if after.Cost.Projected.P50 > 20 || after.Schedule.RemainingMinutes.P50 > 400 {
		t.Fatalf("with history the forecast follows the estimates: before %+v after %+v %+v", before.Cost, after.Cost, after.Schedule)
	}
}

func TestEstimatesWithoutHistory(t *testing.T) {
	w := start(t)
	plan, ids := w.propose(w.brief, task("a", false, nil), task("b", true, nil), task("c", false, nil))
	w.approve(plan)
	w.complete(ids[0], false, rand.New(rand.NewPCG(1, 1)))

	rep, body := w.report("/api/v1/plans/" + plan + "/estimates")
	if rep.Sufficient || rep.History != 1 || len(rep.Tasks) != 2 || rep.Totals.Estimated != 0 || rep.Totals.Tasks != 2 {
		t.Fatalf("report: %s", body)
	}
	for _, te := range rep.Tasks {
		if te.Estimate.Sufficient || te.Estimate.Confidence != estimate.ConfidenceNone || len(te.ExceedsBudget) != 0 {
			t.Fatalf("estimate without history: %+v", te)
		}
	}
	if !strings.Contains(string(body), `"exceeds_budget":[]`) || !strings.Contains(string(body), `"basis":[]`) {
		t.Fatalf("empty lists must encode as []: %s", body)
	}
}

func TestEstimatesAccess(t *testing.T) {
	w := start(t)
	plan, _ := w.propose(w.brief, task("a", false, nil))
	for _, path := range []string{"/api/v1/plans/" + plan + "/estimates", "/api/v1/briefs/" + w.brief + "/estimates"} {
		if code, _ := w.get(path, ""); code != 401 {
			t.Errorf("%s without token: %d", path, code)
		}
		if code, _ := w.get(path, w.wk); code != 403 {
			t.Errorf("%s with agent token: %d", path, code)
		}
	}
	for _, path := range []string{"/api/v1/plans/P-99/estimates", "/api/v1/briefs/B-99/estimates"} {
		if code, _ := w.get(path, servertest.OwnerToken); code != 404 {
			t.Errorf("%s: %d", path, code)
		}
	}
}
