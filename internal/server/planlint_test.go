package server

import (
	"strings"
	"testing"
)

func TestProposePlanLint(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	bid := e.restWant(201, "POST", "/api/v1/briefs", map[string]any{
		"title": "B", "repos": []string{"o/r"}, "max_cost_eur": 10, "max_parallel_agents": 1,
	})["id"].(string)

	task := func(ref, title string, extra map[string]any) map[string]any {
		t := map[string]any{"ref": ref, "title": title, "spec": "Implement it and run the tests", "repo": "o/r",
			"acceptance_criteria": []string{"endpoint returns 200", "tests pass in CI"}, "budget": map[string]any{"max_minutes": 30}}
		for k, v := range extra {
			t[k] = v
		}
		return t
	}

	out, isErr := e.call(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{
		task("a", "A", map[string]any{"depends_on": []string{"b"}}),
		task("b", "B", map[string]any{"depends_on": []string{"a"}}),
		task("c", "C", map[string]any{"repo": "o/elsewhere"}),
	}})
	if !isErr {
		t.Fatalf("plan with errors accepted: %v", out)
	}
	errObj := out["error"].(map[string]any)
	msg, _ := errObj["message"].(string)
	if errObj["code"] != "bad_request" || !strings.Contains(msg, "dependency_cycle") || !strings.Contains(msg, "repo_outside_brief") {
		t.Fatalf("unexpected error: %v", errObj)
	}
	if plans := e.restWant(200, "GET", "/api/v1/plans?brief_id="+bid, nil)["plans"].([]any); len(plans) != 0 {
		t.Fatalf("rejected plan was stored: %v", plans)
	}

	plan := e.must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{
		task("a", "Add password reset", map[string]any{"acceptance_criteria": []string{"works"}, "budget": map[string]any{}}),
		task("b", "B", nil),
		task("c", "C", nil),
	}})["plan"].(map[string]any)
	warnings := plan["warnings"].([]any)
	got := map[string]bool{}
	for _, w := range warnings {
		f := w.(map[string]any)
		if f["severity"] != "warning" {
			t.Fatalf("non-warning attached: %v", f)
		}
		got[f["rule"].(string)] = true
	}
	for _, rule := range []string{"acceptance_criteria", "budget_missing", "no_tester", "parallelism"} {
		if !got[rule] {
			t.Fatalf("missing warning %s in %v", rule, warnings)
		}
	}

	stored := e.restWant(200, "GET", "/api/v1/plans/"+plan["id"].(string), nil)
	if len(stored["warnings"].([]any)) != len(warnings) {
		t.Fatalf("stored warnings differ: %v vs %v", stored["warnings"], warnings)
	}

	feed := e.restWant(200, "GET", "/api/v1/decisions", nil)["decisions"].([]any)
	var summary string
	for _, d := range feed {
		if d := d.(map[string]any); d["id"] == "plan:"+plan["id"].(string) {
			summary, _ = d["summary"].(string)
		}
	}
	if !strings.Contains(summary, "lint warnings") || !strings.Contains(summary, "no_tester") {
		t.Fatalf("decision summary lacks warnings: %q", summary)
	}

	approved := e.restWant(200, "POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{})
	if len(approved["warnings"].([]any)) != len(warnings) {
		t.Fatalf("warnings lost on approve: %v", approved["warnings"])
	}

	clean := e.must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{task("a", "A", nil)}})["plan"].(map[string]any)
	if w, ok := clean["warnings"].([]any); !ok || len(w) != 0 {
		t.Fatalf("clean plan warnings should be an empty list: %v", clean["warnings"])
	}
}
