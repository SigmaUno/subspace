package server

import "testing"

func TestClaimRespectsMaxParallelAgents(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	w1 := e.register("w1", "worker", "claude-code")
	w2 := e.register("w2", "worker", "claude-code")

	_, br := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "Cap", "body": "b", "repos": []string{"o/r"}, "max_parallel_agents": 1})
	bid := br["id"].(string)
	plan := e.must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{
		map[string]any{"ref": "a", "title": "A", "spec": "a", "repo": "o/r"},
		map[string]any{"ref": "b", "title": "B", "spec": "b", "repo": "o/r"},
		map[string]any{"ref": "c", "title": "C", "spec": "c", "repo": "o/r"},
	}})["plan"].(map[string]any)
	if code, _ := e.rest("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatal("approve")
	}
	var ids []string
	for _, x := range plan["tasks"].([]any) {
		ids = append(ids, x.(map[string]any)["id"].(string))
	}
	e.must(lead, "assign", map[string]any{"task_id": ids[0], "agent_id": "w1"})
	e.must(lead, "assign", map[string]any{"task_id": ids[1], "agent_id": "w2"})
	e.must(lead, "assign", map[string]any{"task_id": ids[2], "agent_id": "w1"})

	e.must(w1, "claim_task", map[string]any{"task_id": ids[0]})
	e.mustFail(w2, "claim_task", map[string]any{"task_id": ids[1]}, "capacity")
	e.must(w1, "claim_task", map[string]any{"task_id": ids[2]})

	for _, id := range []string{ids[0], ids[2]} {
		e.must(w1, "submit", map[string]any{"task_id": id, "pr_url": "https://git.example/o/r/pulls/1"})
	}
	if c := e.must(w2, "claim_task", map[string]any{"task_id": ids[1]}); status(c) != "in_progress" {
		t.Fatalf("claim after slots freed: %v", c)
	}
}
