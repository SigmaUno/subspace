package server_test

import (
	"net/url"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/servertest"
)

func sumSeries(t *testing.T, e *servertest.Env, q url.Values) (total, peak float64, n int) {
	t.Helper()
	code, out := e.REST("GET", "/api/v1/timeseries?"+q.Encode(), nil)
	if code != 200 {
		t.Fatalf("timeseries %v: %d %v", q, code, out)
	}
	for _, p := range out["points"].([]any) {
		v := p.(map[string]any)["v"].(float64)
		total += v
		peak = max(peak, v)
		n++
	}
	return
}

func TestTimeseriesFromRealFlow(t *testing.T) {
	e := servertest.New(t)
	lead := e.Register("rex", "leader", "other")
	rev := e.Register("codex-r", "reviewer", "codex")
	w1 := e.Register("w1", "worker", "claude-code")

	var tasks [3]string
	var briefs [2]string
	for i, n := range []int{2, 1} {
		code, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B", "body": "b", "repos": []string{"o/r"}, "max_cost_eur": 50, "max_parallel_agents": 10})
		if code != 201 {
			t.Fatalf("brief: %d %v", code, br)
		}
		briefs[i] = br["id"].(string)
		var specs []any
		for j := 0; j < n; j++ {
			specs = append(specs, map[string]any{"title": "t", "spec": "s", "repo": "o/r", "acceptance_criteria": []string{"ok"}})
		}
		plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": briefs[i], "tasks": specs})["plan"].(map[string]any)
		if code, out := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
			t.Fatalf("approve: %d %v", code, out)
		}
		for j, x := range plan["tasks"].([]any) {
			tasks[i*2+j] = x.(map[string]any)["id"].(string)
		}
	}
	first := tasks[0]

	e.Must(lead, "assign", map[string]any{"task_id": first, "agent_id": "w1"})
	e.Must(w1, "claim_task", map[string]any{"task_id": first})
	e.Must(w1, "update_status", map[string]any{"task_id": first, "status": "in_progress", "cost_eur": 1.0})
	e.Must(w1, "update_status", map[string]any{"task_id": first, "status": "in_progress", "cost_eur": 3.5})
	e.Must(w1, "submit", map[string]any{"task_id": first, "pr_url": "https://example.com/pr/1", "summary": "s"})
	e.Must(rev, "review", map[string]any{"task_id": first, "verdict": "changes_requested", "comments": "fix"})
	e.Must(w1, "claim_task", map[string]any{"task_id": first})
	e.Must(w1, "submit", map[string]any{"task_id": first, "pr_url": "https://example.com/pr/1", "summary": "s"})
	e.Must(rev, "review", map[string]any{"task_id": first, "verdict": "approve", "comments": "ok"})
	if code, out := e.REST("POST", "/api/v1/tasks/"+first+"/done", map[string]any{}); code != 200 {
		t.Fatalf("done: %d %v", code, out)
	}

	since := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	q := func(metric, brief string) url.Values {
		v := url.Values{"metric": {metric}, "bucket": {"1h"}, "since": {since}}
		if brief != "" {
			v.Set("brief_id", brief)
		}
		return v
	}
	for _, c := range []struct {
		metric, brief string
		want          float64
	}{
		{"tasks_created", "", 3},
		{"tasks_created", briefs[0], 2},
		{"tasks_created", briefs[1], 1},
		{"tasks_done", "", 1},
		{"tasks_done", briefs[0], 1},
		{"tasks_done", briefs[1], 0},
		{"review_rounds", "", 1},
		{"review_rounds", briefs[1], 0},
		{"cost_eur", "", 3.5},
		{"cost_eur", briefs[0], 3.5},
		{"cost_eur", briefs[1], 0},
		{"tokens", "", 0},
	} {
		if got, _, _ := sumSeries(t, e, q(c.metric, c.brief)); got != c.want {
			t.Errorf("%s brief=%q: got %v want %v", c.metric, c.brief, got, c.want)
		}
	}
	if _, peak, _ := sumSeries(t, e, q("active_agents", briefs[0])); peak < 1 {
		t.Errorf("active_agents peak %v", peak)
	}
	if _, peak, _ := sumSeries(t, e, q("active_agents", briefs[1])); peak != 0 {
		t.Errorf("active_agents on idle brief: %v", peak)
	}
}

func TestTimeseriesParametersAndCap(t *testing.T) {
	e := servertest.New(t)
	for path, want := range map[string]int{
		"/api/v1/timeseries":                                          400,
		"/api/v1/timeseries?metric=nope":                              400,
		"/api/v1/timeseries?metric=tokens&bucket=2h":                  400,
		"/api/v1/timeseries?metric=tokens&since=yesterday":            400,
		"/api/v1/timeseries?metric=tokens&since=2999-01-01T00:00:00Z": 400,
		"/api/v1/timeseries?metric=tokens&brief_id=B-404":             404,
		"/api/v1/timeseries?metric=tokens":                            200,
	} {
		if code, out := e.REST("GET", path, nil); code != want {
			t.Errorf("%s: %d %v want %d", path, code, out, want)
		}
	}
	for bucket, want := range map[string]int{"5m": 288, "1h": 168, "1d": 30} {
		_, _, n := sumSeries(t, e, url.Values{"metric": {"tasks_done"}, "bucket": {bucket}})
		if n != want && n != want+1 {
			t.Errorf("default %s buckets: %d want %d", bucket, n, want)
		}
	}
	if _, _, n := sumSeries(t, e, url.Values{"metric": {"tasks_done"}, "bucket": {"5m"}, "since": {"2020-01-01T00:00:00Z"}}); n != 2000 {
		t.Errorf("capped buckets: %d want 2000", n)
	}
}
