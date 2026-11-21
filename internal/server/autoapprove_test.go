package server

import "testing"

func TestAutoApprovePlanSkipsOwnerApproval(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	e.register("claude-1", "worker", "claude-code")

	_, manual := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "Manual"})
	if manual["auto_approve_plan"] != false {
		t.Fatalf("auto_approve_plan should default to false: %v", manual)
	}
	_, auto := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "Auto", "auto_approve_plan": true})
	if auto["auto_approve_plan"] != true {
		t.Fatalf("auto_approve_plan should be set: %v", auto)
	}

	propose := func(briefID string) map[string]any {
		t.Helper()
		return e.must(lead, "propose_plan", map[string]any{"brief_id": briefID, "tasks": []any{
			map[string]any{"title": "Work", "spec": "do it", "repo": "o/r"},
		}})["plan"].(map[string]any)
	}
	taskStatus := func(p map[string]any) string {
		return p["tasks"].([]any)[0].(map[string]any)["status"].(string)
	}

	held := propose(manual["id"].(string))
	if held["status"] != "proposed" || taskStatus(held) != "backlog" {
		t.Fatalf("manual brief must wait for the owner: %v", held)
	}

	plan := propose(auto["id"].(string))
	if plan["status"] != "approved" || taskStatus(plan) != "ready" {
		t.Fatalf("auto brief plan should be approved with ready tasks: %v", plan)
	}
	id := plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
	e.must(lead, "assign", map[string]any{"task_id": id, "agent_id": "claude-1"})
	if _, got := e.rest("GET", "/api/v1/briefs/"+auto["id"].(string), nil); got["plan_status"] != "approved" {
		t.Fatalf("brief plan_status: %v", got)
	}
	if code, out := e.rest("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 409 {
		t.Fatalf("an auto-approved plan cannot be approved again: %d %v", code, out)
	}

	_, events := e.rest("GET", "/api/v1/audit?tool=approve_plan", nil)
	found := false
	for _, ev := range events["events"].([]any) {
		m := ev.(map[string]any)
		if m["agent_id"] == "rex" && m["role"] == "leader" {
			found = true
		}
	}
	if !found {
		t.Fatalf("auto approval must be audited as the leader: %v", events)
	}
}

func TestAutoApprovePlanToggledByOwner(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	_, brief := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "Later"})
	id := brief["id"].(string)

	if code, out := e.rest("PATCH", "/api/v1/briefs/"+id, map[string]any{"auto_approve_plan": true}); code != 200 || out["auto_approve_plan"] != true {
		t.Fatalf("enable: %d %v", code, out)
	}
	if code, out := e.rest("PATCH", "/api/v1/briefs/"+id, map[string]any{"max_parallel_agents": 3}); code != 200 || out["auto_approve_plan"] != true {
		t.Fatalf("unrelated patch must keep the setting: %d %v", code, out)
	}
	plan := e.must(lead, "propose_plan", map[string]any{"brief_id": id, "tasks": []any{
		map[string]any{"title": "Work", "spec": "do it", "repo": "o/r"},
	}})["plan"].(map[string]any)
	if plan["status"] != "approved" {
		t.Fatalf("plan should be auto-approved: %v", plan)
	}

	if code, out := e.rest("PATCH", "/api/v1/briefs/"+id, map[string]any{"auto_approve_plan": false}); code != 200 || out["auto_approve_plan"] != false {
		t.Fatalf("disable: %d %v", code, out)
	}
	plan = e.must(lead, "propose_plan", map[string]any{"brief_id": id, "tasks": []any{
		map[string]any{"title": "More", "spec": "do more", "repo": "o/r"},
	}})["plan"].(map[string]any)
	if plan["status"] != "proposed" {
		t.Fatalf("plan should wait for the owner again: %v", plan)
	}
}
