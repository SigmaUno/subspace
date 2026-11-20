package server

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func (e *env) waitAgent(id, want string) map[string]any {
	e.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		a := e.restWant(200, "GET", "/api/v1/agents/"+id, nil)
		if a["status"] == want {
			return a
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("agent %s: status %v, want %s", id, a["status"], want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPoisonTaskBlocksAndSurfacesAsOwnerDecision(t *testing.T) {
	e := newFlowEnv(t, func(c *Config) { c.Lease = 100 * time.Millisecond; c.PoisonExpiries = 2 })
	lead := e.register("rex", "leader", "other")
	workers := []string{"w1", "w2"}
	tokens := map[string]string{}
	for _, w := range workers {
		tokens[w] = e.register(w, "worker", "claude-code")
	}
	id := e.approvedPlan(lead, e.brief(map[string]any{}), planTask("crash loop", nil))[0]
	e.must(lead, "assign", map[string]any{"task_id": id, "agent_id": "w1"})

	e.must(tokens["w1"], "claim_task", map[string]any{"task_id": id})
	if tk := e.waitStatus(id, "assigned"); tk["lease_expiries"] != float64(1) {
		t.Fatalf("lease_expiries after first expiry: %v", tk["lease_expiries"])
	}
	e.must(lead, "reassign", map[string]any{"task_id": id, "agent_id": "w2"})
	e.must(tokens["w2"], "claim_task", map[string]any{"task_id": id})

	tk := e.waitStatus(id, "blocked")
	if reason, _ := tk["blocked_reason"].(string); !strings.HasPrefix(reason, "poison_task") {
		t.Fatalf("blocked_reason %v", tk["blocked_reason"])
	}
	e.mustFail(tokens["w2"], "claim_task", map[string]any{"task_id": id}, "conflict")

	msgs := e.restWant(200, "GET", "/api/v1/threads/task/"+id+"/messages", nil)["messages"].([]any)
	var system bool
	for _, m := range msgs {
		if m.(map[string]any)["kind"] == "system" {
			system = true
		}
	}
	if !system {
		t.Fatalf("no system message in task thread: %v", msgs)
	}

	var found map[string]any
	for _, d := range e.restWant(200, "GET", "/api/v1/decisions", nil)["decisions"].([]any) {
		if d.(map[string]any)["id"] == "unblock:"+id {
			found = d.(map[string]any)
		}
	}
	if found == nil || !strings.Contains(fmt.Sprint(found["summary"]), "poison_task") {
		t.Fatalf("no poison_task decision for the owner: %v", found)
	}

	e.restWant(200, "POST", "/api/v1/tasks/"+id+"/reopen", map[string]any{"status": "assigned"})
	if tk := e.task(id); tk["status"] != "assigned" || tk["lease_expiries"] != float64(0) {
		t.Fatalf("after reopen: %v expiries %v", tk["status"], tk["lease_expiries"])
	}
}

func TestFlappingAgentPausedUntilOwnerResumes(t *testing.T) {
	e := newFlowEnv(t, func(c *Config) { c.Lease = 100 * time.Millisecond })
	lead := e.register("rex", "leader", "other")
	wk := e.register("w1", "worker", "claude-code")
	ids := e.approvedPlan(lead, e.brief(map[string]any{}), planTask("a", nil), planTask("b", nil), planTask("c", nil))
	for _, id := range ids {
		e.must(lead, "assign", map[string]any{"task_id": id, "agent_id": "w1"})
	}
	for _, id := range ids {
		e.must(wk, "claim_task", map[string]any{"task_id": id})
		e.waitStatus(id, "assigned")
	}

	a := e.waitAgent("w1", "paused")
	if a["pause_reason"] != "flapping" {
		t.Fatalf("pause_reason %v", a["pause_reason"])
	}
	e.mustFail(wk, "claim_task", map[string]any{"task_id": ids[0]}, "agent_paused")
	for _, id := range ids {
		if tk := e.task(id); tk["status"] != "assigned" {
			t.Fatalf("%s is %v", id, tk["status"])
		}
	}

	e.restWant(200, "POST", "/api/v1/agents/w1/resume", map[string]any{})
	e.must(wk, "claim_task", map[string]any{"task_id": ids[0]})
}
