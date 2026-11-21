package agentstats_test

import (
	"testing"

	"github.com/SigmaUno/subspace/internal/servertest"
)

func TestAgentStatsOverHTTP(t *testing.T) {
	e := servertest.New(t)
	lead := e.Register("rex", "leader", "other")
	w1 := e.Register("claude-1", "worker", "claude-code")
	w2 := e.Register("claude-2", "worker", "claude-code")
	e.Register("claude-3", "worker", "claude-code")
	rev := e.Register("codex-r", "reviewer", "codex")

	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "Demo", "body": "b", "repos": []string{"o/r"},
		"max_cost_eur": 50, "max_parallel_agents": 5})
	bid := br["id"].(string)
	task := func(title string) map[string]any {
		return map[string]any{"title": title, "spec": "s", "repo": "o/r"}
	}
	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": bid,
		"tasks": []any{task("One"), task("Two"), task("Three")}})["plan"].(map[string]any)
	if code, out := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	for task, agent := range map[string]string{"T-1": "claude-1", "T-2": "claude-2", "T-3": "claude-1"} {
		e.Must(lead, "assign", map[string]any{"task_id": task, "agent_id": agent})
	}

	submit := func(tok, id string) {
		e.Must(tok, "claim_task", map[string]any{"task_id": id})
		e.Must(tok, "submit", map[string]any{"task_id": id, "pr_url": "https://git.test/o/r/pulls/1", "summary": "done"})
	}
	review := func(id, verdict string) {
		e.Must(rev, "review", map[string]any{"task_id": id, "verdict": verdict, "comments": "c"})
	}

	e.Must(w1, "claim_task", map[string]any{"task_id": "T-1"})
	e.Must(w1, "update_status", map[string]any{"task_id": "T-1", "status": "in_progress", "cost_eur": 0.5})
	e.Must(w1, "submit", map[string]any{"task_id": "T-1", "pr_url": "https://git.test/o/r/pulls/1"})
	review("T-1", "approve")

	submit(w2, "T-2")
	review("T-2", "changes_requested")
	submit(w2, "T-2")
	review("T-2", "approve")

	e.Must(w1, "claim_task", map[string]any{"task_id": "T-3"})
	e.Must(w1, "update_status", map[string]any{"task_id": "T-3", "status": "blocked", "note": "stuck"})

	code, out := e.REST("GET", "/api/v1/agent-stats", nil)
	if code != 200 {
		t.Fatalf("list: %d %v", code, out)
	}
	rows := out["agents"].([]any)
	if len(rows) != 5 {
		t.Fatalf("agents: %d", len(rows))
	}
	byID := map[string]map[string]any{}
	for _, r := range rows {
		m := r.(map[string]any)
		byID[m["agent_id"].(string)] = m
	}

	s1, s2, s3 := byID["claude-1"], byID["claude-2"], byID["claude-3"]
	if s1["tasks_completed"] != 1.0 || s1["blocked"] != 1.0 || s1["first_pass_approval_rate"] != 1.0 ||
		s1["review_rounds_per_task"] != 0.0 || s1["cost_per_task"] != 0.5 {
		t.Fatalf("claude-1: %v", s1)
	}
	if s2["tasks_completed"] != 1.0 || s2["blocked"] != 0.0 || s2["first_pass_approval_rate"] != 0.0 ||
		s2["review_rounds_per_task"] != 1.0 {
		t.Fatalf("claude-2: %v", s2)
	}
	if s3["tasks_completed"] != 0.0 || s3["first_pass_approval_rate"] != nil || s3["score"] != 0.5 {
		t.Fatalf("claude-3: %v", s3)
	}
	if s1["score"].(float64) <= s3["score"].(float64) || s3["score"].(float64) <= s2["score"].(float64) {
		t.Fatalf("scores not ordered: %v %v %v", s1["score"], s3["score"], s2["score"])
	}
	for _, id := range []string{"claude-1", "claude-2"} {
		trend := byID[id]["trend"].([]any)
		if len(trend) != 7 || trend[6].(map[string]any)["completed"] != 1.0 {
			t.Fatalf("%s trend: %v", id, trend)
		}
	}
	if first := rows[0].(map[string]any)["agent_id"]; first != "claude-1" {
		t.Fatalf("leaderboard order: %v", first)
	}

	code, one := e.REST("GET", "/api/v1/agents/claude-2/stats", nil)
	if code != 200 || one["agent_id"] != "claude-2" || one["tasks_completed"] != 1.0 {
		t.Fatalf("agent stats: %d %v", code, one)
	}
	if code, _ := e.REST("GET", "/api/v1/agents/nobody/stats", nil); code != 404 {
		t.Fatalf("unknown agent: %d", code)
	}
	_, reviewers := e.REST("GET", "/api/v1/agent-stats?role=reviewer", nil)
	if got := reviewers["agents"].([]any); len(got) != 1 {
		t.Fatalf("role filter: %v", got)
	}
}
