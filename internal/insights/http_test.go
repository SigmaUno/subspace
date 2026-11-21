package insights_test

import (
	"fmt"
	"testing"

	"github.com/SigmaUno/subspace/internal/servertest"
)

func findings(t *testing.T, out map[string]any) map[string]map[string]any {
	t.Helper()
	got := map[string]map[string]any{}
	for _, f := range out["findings"].([]any) {
		m := f.(map[string]any)
		got[m["id"].(string)] = m
	}
	return got
}

func TestInsightsAfterScriptedBadBehavior(t *testing.T) {
	e := servertest.New(t)
	e.Register("rex", "leader", "other")
	w1 := e.Register("claude-1", "worker", "claude-code")
	w2 := e.Register("claude-2", "worker", "claude-code")
	w3 := e.Register("claude-3", "worker", "claude-code")

	for i := range 6 {
		e.MustFail(w1, "claim_task", map[string]any{"task_id": fmt.Sprintf("T-%d", 90+i)}, "not_assigned")
	}
	for range 5 {
		e.MustFail(w2, "claim_task", map[string]any{"task_id": "T-99"}, "not_assigned")
	}
	e.MustFail(w2, "claim_task", map[string]any{"task_id": "T-99"}, "agent_paused")
	e.Must(w3, "whoami", map[string]any{})

	code, out := e.REST("GET", "/api/v1/insights", nil)
	if code != 200 {
		t.Fatalf("insights: %d %v", code, out)
	}
	got := findings(t, out)
	claims := got["claude-1:unassigned_claims"]
	if claims == nil || claims["severity"] != "high" || claims["count"] != 6.0 || len(claims["evidence"].([]any)) != 6 {
		t.Fatalf("claude-1 claims: %v", claims)
	}
	if len(claims["task_ids"].([]any)) != 6 {
		t.Fatalf("claude-1 tasks: %v", claims["task_ids"])
	}
	if got["claude-1:repeated_errors"] != nil {
		t.Fatalf("distinct errors reported as repeated: %v", got["claude-1:repeated_errors"])
	}
	rep := got["claude-2:repeated_errors"]
	if rep == nil || rep["severity"] != "warning" || rep["count"] != 5.0 {
		t.Fatalf("claude-2 repeated: %v", rep)
	}
	if loop := got["claude-2:loop_trips"]; loop == nil || loop["severity"] != "warning" || loop["count"] != 1.0 {
		t.Fatalf("claude-2 loop: %v", loop)
	}
	for id := range got {
		if id[:8] == "claude-3" {
			t.Fatalf("clean agent flagged: %v", got[id])
		}
	}
	if out["high"] != 1.0 || out["warning"] != 3.0 {
		t.Fatalf("counts: high=%v warning=%v", out["high"], out["warning"])
	}
	first := out["findings"].([]any)[0].(map[string]any)
	if first["id"] != "claude-1:unassigned_claims" {
		t.Fatalf("severity order: %v", first["id"])
	}

	_, high := e.REST("GET", "/api/v1/insights?severity=high", nil)
	if n := len(high["findings"].([]any)); n != 1 {
		t.Fatalf("severity filter: %v", high)
	}
	if code, _ := e.REST("GET", "/api/v1/insights?severity=low", nil); code != 400 {
		t.Fatalf("bad severity: %d", code)
	}

	code, mine := e.REST("GET", "/api/v1/agents/claude-2/insights", nil)
	if code != 200 || len(mine["findings"].([]any)) != 3 {
		t.Fatalf("agent insights: %d %v", code, mine)
	}
	for _, f := range mine["findings"].([]any) {
		if f.(map[string]any)["agent_id"] != "claude-2" {
			t.Fatalf("foreign finding: %v", f)
		}
	}
	_, clean := e.REST("GET", "/api/v1/agents/claude-3/insights", nil)
	if len(clean["findings"].([]any)) != 0 || clean["high"] != 0.0 {
		t.Fatalf("clean agent: %v", clean)
	}
	if code, _ := e.REST("GET", "/api/v1/agents/nobody/insights", nil); code != 404 {
		t.Fatalf("unknown agent: %d", code)
	}

	_, dec := e.REST("GET", "/api/v1/decisions", nil)
	var found map[string]any
	for _, d := range dec["decisions"].([]any) {
		if m := d.(map[string]any); m["id"] == "insight:claude-1:unassigned_claims" {
			found = m
		}
	}
	if found == nil || found["agent_id"] != "claude-1" {
		t.Fatalf("high finding missing from decisions: %v", dec)
	}
	for _, d := range dec["decisions"].([]any) {
		if d.(map[string]any)["id"] == "insight:claude-2:repeated_errors" {
			t.Fatalf("warning raised as decision")
		}
	}
}
