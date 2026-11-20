package server

import "testing"

func TestCrossReviewModes(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	wk := e.register("claude-1", "worker", "claude-code")
	rev := e.register("codex-r", "reviewer", "codex")

	_, def := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "Default"})
	if def["cross_review"] != true {
		t.Fatalf("cross_review should default to true: %v", def)
	}
	_, off := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "Off", "cross_review": false})
	if off["cross_review"] != false {
		t.Fatalf("cross_review should be false: %v", off)
	}
	if got := e.must(lead, "get_brief", map[string]any{"brief_id": off["id"]})["brief"].(map[string]any); got["cross_review"] != false {
		t.Fatalf("get_brief: %v", got)
	}

	submit := func(briefID string) (string, map[string]any) {
		t.Helper()
		plan := e.must(lead, "propose_plan", map[string]any{"brief_id": briefID, "tasks": []any{
			map[string]any{"title": "Work", "spec": "do it", "repo": "o/r"},
		}})["plan"].(map[string]any)
		id := plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
		if code, out := e.rest("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
			t.Fatalf("approve: %d %v", code, out)
		}
		e.must(lead, "assign", map[string]any{"task_id": id, "agent_id": "claude-1"})
		e.must(wk, "claim_task", map[string]any{"task_id": id})
		return id, e.must(wk, "submit", map[string]any{"task_id": id, "pr_url": "https://git.example/o/r/pulls/1"})
	}

	id, sub := submit(def["id"].(string))
	if status(sub) != "submitted" {
		t.Fatalf("cross_review=true should submit: %v", sub)
	}
	if lr := e.must(rev, "list_reviews", nil)["tasks"].([]any); len(lr) != 1 {
		t.Fatalf("list_reviews: %v", lr)
	}
	if r := e.must(rev, "review", map[string]any{"task_id": id, "verdict": "approve", "comments": "ok"}); status(r) != "approved" {
		t.Fatal(r)
	}
	if code, _ := e.rest("POST", "/api/v1/tasks/"+id+"/done", map[string]any{}); code != 200 {
		t.Fatal("done")
	}

	id, sub = submit(off["id"].(string))
	if status(sub) != "approved" {
		t.Fatalf("cross_review=false should approve on submit: %v", sub)
	}
	if lr := e.must(rev, "list_reviews", nil)["tasks"].([]any); len(lr) != 0 {
		t.Fatalf("no review expected: %v", lr)
	}
	e.mustFail(rev, "review", map[string]any{"task_id": id, "verdict": "approve", "comments": "ok"}, "conflict")
	if code, out := e.rest("POST", "/api/v1/tasks/"+id+"/done", map[string]any{}); code != 200 || out["status"] != "done" {
		t.Fatalf("done: %d %v", code, out)
	}

	code, patched := e.rest("PATCH", "/api/v1/briefs/"+def["id"].(string), map[string]any{"cross_review": false})
	if code != 200 || patched["cross_review"] != false {
		t.Fatalf("patch: %d %v", code, patched)
	}
	_, got := e.rest("GET", "/api/v1/briefs/"+def["id"].(string), nil)
	if got["cross_review"] != false {
		t.Fatalf("patch not persisted: %v", got)
	}
	_, same := e.rest("PATCH", "/api/v1/briefs/"+def["id"].(string), map[string]any{"max_parallel_agents": 3})
	if same["cross_review"] != false {
		t.Fatalf("unrelated patch must keep cross_review: %v", same)
	}
}
