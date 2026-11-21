package forecast_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/SigmaUno/subspace/internal/forecast"
	"github.com/SigmaUno/subspace/internal/servertest"
)

func TestForecastOverHTTP(t *testing.T) {
	e := servertest.New(t)
	lead := e.Register("rex", "leader", "other")
	wk := e.Register("claude-1", "worker", "claude-code")
	rev := e.Register("codex-r", "reviewer", "codex")

	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "Demo", "body": "do it", "repos": []string{"o/r"}, "max_cost_eur": 1.0, "max_parallel_agents": 2})
	bid := br["id"].(string)
	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{
		map[string]any{"ref": "a", "title": "First", "spec": "s", "repo": "o/r", "acceptance_criteria": []string{"ok"}, "budget": map[string]any{"max_minutes": 30, "max_cost_eur": 1.0}},
		map[string]any{"ref": "b", "title": "Second", "spec": "s", "repo": "o/r", "depends_on": []string{"a"}, "budget": map[string]any{"max_minutes": 30, "max_cost_eur": 1.0}},
	}})["plan"].(map[string]any)
	tasks := plan["tasks"].([]any)
	t1 := tasks[0].(map[string]any)["id"].(string)
	if code, _ := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatal("approve plan")
	}

	get := func(path, token string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest("GET", e.TS.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	fetch := func() forecast.Forecast {
		t.Helper()
		code, body := get("/api/v1/briefs/"+bid+"/forecast", servertest.OwnerToken)
		if code != 200 {
			t.Fatalf("forecast: %d %s", code, body)
		}
		var f forecast.Forecast
		if err := json.Unmarshal(body, &f); err != nil {
			t.Fatal(err)
		}
		return f
	}
	budgetDecision := func() bool {
		t.Helper()
		code, body := get("/api/v1/decisions", servertest.OwnerToken)
		if code != 200 {
			t.Fatalf("decisions: %d %s", code, body)
		}
		var out struct {
			Decisions []struct {
				ID      string `json:"id"`
				Kind    string `json:"kind"`
				BriefID string `json:"brief_id"`
			}
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		for _, d := range out.Decisions {
			if d.ID == "budget:"+bid {
				if d.Kind != "budget" || d.BriefID != bid {
					t.Fatalf("decision: %+v", d)
				}
				return true
			}
		}
		return false
	}

	f := fetch()
	if f.BriefID != bid || f.Tasks.Total != 2 || f.Tasks.Remaining != 2 || f.Cost.Budget != 1 || f.Schedule.PlannedCriticalPath != 60 {
		t.Fatalf("initial forecast: %+v", f)
	}

	e.Must(lead, "assign", map[string]any{"task_id": t1, "agent_id": "claude-1"})
	e.Must(wk, "claim_task", map[string]any{"task_id": t1})
	e.Must(wk, "update_status", map[string]any{"task_id": t1, "status": "in_progress", "cost_eur": 0.9})
	e.Must(wk, "submit", map[string]any{"task_id": t1, "pr_url": "https://git.example/o/r/pulls/1", "summary": "done"})
	e.Must(rev, "review", map[string]any{"task_id": t1, "verdict": "approve", "comments": "lgtm"})
	if code, _ := e.REST("POST", "/api/v1/tasks/"+t1+"/done", map[string]any{"note": "merged"}); code != 200 {
		t.Fatal("mark done")
	}

	f = fetch()
	if f.Tasks.Complete != 1 || f.Cost.Used != 0.9 || !f.CostOverrun() || f.Cost.Projected.P50 < 1.2 {
		t.Fatalf("forecast after t1: %+v", f)
	}
	if len(f.Risks) == 0 || f.Risks[0].Code != forecast.CodeCostOverrun || f.Risks[0].Severity != forecast.SeverityHigh {
		t.Fatalf("risks: %+v", f.Risks)
	}
	if again := fetch(); again.Seed != f.Seed || again.Cost.Projected != f.Cost.Projected {
		t.Fatalf("forecast not deterministic: %+v vs %+v", again.Cost, f.Cost)
	}
	if !budgetDecision() {
		t.Fatal("expected budget decision in owner feed")
	}

	if code, out := e.REST("PATCH", "/api/v1/briefs/"+bid, map[string]any{"max_cost_eur": 100}); code != 200 {
		t.Fatalf("raise budget: %d %v", code, out)
	}
	if budgetDecision() {
		t.Fatal("budget decision must clear once the forecast fits the budget")
	}
	if f = fetch(); len(f.Risks) != 0 {
		t.Fatalf("risks after raising budget: %+v", f.Risks)
	}

	if code, _ := get("/api/v1/briefs/B-99/forecast", servertest.OwnerToken); code != 404 {
		t.Fatalf("unknown brief: %d", code)
	}
	if code, _ := get("/api/v1/briefs/"+bid+"/forecast", ""); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := get("/api/v1/briefs/"+bid+"/forecast", wk); code != 403 {
		t.Fatalf("agent token: %d", code)
	}
}
