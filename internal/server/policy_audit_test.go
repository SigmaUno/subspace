package server_test

import (
	"fmt"
	"strings"
	"testing"
)

func TestPolicyStepLimitFailsClosed(t *testing.T) {
	e := newPolicyEnv(t)

	var paths []string
	for i := range 40 {
		paths = append(paths, fmt.Sprintf("pad/%02d/%s", i, strings.Repeat("x", 480)))
	}
	paths = append(paths, "deploy/prod")
	task := e.Must(e.tok["rex"], "create_task", map[string]any{"brief_id": e.brief, "title": "padded", "spec": "s",
		"repo": "o/r", "paths": paths})["task"].(map[string]any)
	e.tasks["padded"] = task["id"].(string)

	e.assign("padded", "codex-1", "policy_hold")
	if got := e.status("padded"); got != "ready" {
		t.Fatalf("padded deploy task is %s", got)
	}
}

func TestPolicyHoldApprovalCoversOnlyTheHeldCall(t *testing.T) {
	e := newPolicyEnv(t)
	plain := e.tasks["plain"]
	e.ok("POST", "/api/v1/policies", 201, map[string]any{"name": "gated", "enabled": true, "rules": []any{
		map[string]any{"id": "edit", "when": map[string]any{"actions": []string{"update_task"}, "match": `task.id == "` + plain + `"`},
			"then": map[string]any{"effect": "require_owner_approval", "timeout": "1h"}},
		map[string]any{"id": "create", "when": map[string]any{"actions": []string{"create_task"}, "match": `"gated" in task.labels`},
			"then": map[string]any{"effect": "require_owner_approval", "timeout": "1h"}},
	}})
	approve := func(action string) {
		t.Helper()
		for _, h := range e.holds("pending") {
			if h["action"] == action {
				e.ok("POST", "/api/v1/policy-holds/"+h["id"].(string)+"/approve", 200, map[string]any{})
				return
			}
		}
		t.Fatalf("no pending %s hold", action)
	}

	block := map[string]any{"task_id": plain, "status": "blocked"}
	e.MustFail(e.tok["rex"], "update_task", block, "policy_hold")
	approve("update_task")
	e.MustFail(e.tok["rex"], "update_task", map[string]any{"task_id": plain, "status": "cancelled"}, "policy_hold")
	if got := e.status("plain"); got != "ready" {
		t.Fatalf("approval of a block was spent on another update: task is %s", got)
	}
	e.Must(e.tok["rex"], "update_task", block)
	if got := e.status("plain"); got != "blocked" {
		t.Fatalf("approved update not applied: task is %s", got)
	}

	held := map[string]any{"brief_id": e.brief, "title": "rotate keys #gated", "spec": "rotate the signing keys", "repo": "o/r"}
	e.MustFail(e.tok["rex"], "create_task", held, "policy_hold")
	approve("create_task")
	e.MustFail(e.tok["rex"], "create_task", map[string]any{"brief_id": e.brief, "title": "rotate keys #gated",
		"spec": "delete the audit log", "repo": "o/r", "paths": []string{"deploy/**"}}, "policy_hold")
	e.Must(e.tok["rex"], "create_task", held)
}

func TestPolicyMergeReadyHeldSubmitIsNotPromoted(t *testing.T) {
	e := newPolicyEnv(t)
	br := e.ok("POST", "/api/v1/briefs", 201, map[string]any{"title": "Solo", "body": "b", "repos": []string{"o/r"},
		"cross_review": false, "quality_gates": map[string]any{"tests": true}})
	plan := e.Must(e.tok["rex"], "propose_plan", map[string]any{"brief_id": br["id"], "tasks": []any{
		map[string]any{"title": "ship", "spec": "s", "repo": "o/r", "paths": []string{"release/prod"}},
	}})["plan"].(map[string]any)
	e.ok("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", 200, map[string]any{})
	id := plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
	e.ok("POST", "/api/v1/policies", 201, map[string]any{"name": "deploy merges", "enabled": true, "rules": []any{map[string]any{"id": "merge",
		"when": map[string]any{"actions": []string{"merge_ready"}, "match": `overlaps(task.paths, "release/**")`},
		"then": map[string]any{"effect": "deny", "message": "release changes are merged by the owner"}}}})

	e.Must(e.tok["rex"], "assign", map[string]any{"task_id": id, "agent_id": "claude-1"})
	e.Must(e.tok["claude-1"], "claim_task", map[string]any{"task_id": id})
	e.MustFail(e.tok["claude-1"], "submit", map[string]any{"task_id": id, "pr_url": "https://git.test/o/r/pulls/1", "summary": "s"}, "policy_denied")

	e.REST("POST", "/api/v1/tasks/"+id+"/gates/override", map[string]any{"reason": "ci is down"})
	if got := e.ok("GET", "/api/v1/tasks/"+id, 200, nil)["status"]; got == "approved" {
		t.Fatalf("merge_ready deny bypassed through quality gate promotion")
	}
}
