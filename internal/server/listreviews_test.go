package server

import "testing"

func TestListReviewsHidesSameProviderWorkAcrossAssignees(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	claude := e.register("claude-1", "worker", "claude-code")
	codex := e.register("codex-1", "worker", "codex")
	rev := e.register("codex-r", "reviewer", "codex")

	_, br := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "Mixed"})
	briefID := br["id"].(string)
	plan := e.must(lead, "propose_plan", map[string]any{"brief_id": briefID, "tasks": []any{
		map[string]any{"title": "A", "spec": "a", "repo": "o/r"},
		map[string]any{"title": "B", "spec": "b", "repo": "o/r"},
	}})["plan"].(map[string]any)
	if code, out := e.rest("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	tasks := plan["tasks"].([]any)
	anthropicTask := tasks[0].(map[string]any)["id"].(string)
	for i, w := range []struct{ agent, tok string }{{"claude-1", claude}, {"codex-1", codex}} {
		id := tasks[i].(map[string]any)["id"].(string)
		e.must(lead, "assign", map[string]any{"task_id": id, "agent_id": w.agent})
		e.must(w.tok, "claim_task", map[string]any{"task_id": id})
		e.must(w.tok, "submit", map[string]any{"task_id": id, "pr_url": "https://git.example/o/r/pulls/" + id})
	}

	lr := e.must(rev, "list_reviews", nil)["tasks"].([]any)
	if len(lr) != 1 || lr[0].(map[string]any)["id"] != anthropicTask {
		t.Fatalf("list_reviews should show only the anthropic worker's task %s: %v", anthropicTask, lr)
	}
}
