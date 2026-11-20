package server_test

import (
	"testing"

	"github.com/SigmaUno/subspace/internal/servertest"
)

func TestFleetEndpointsMounted(t *testing.T) {
	e := servertest.New(t)
	lead := e.Register("rex", "leader", "other")
	wk := e.Register("w1", "worker", "claude-code")
	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B", "repos": []string{"o/r"}})
	bid := br["id"].(string)
	e.Must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{map[string]any{"title": "t", "spec": "s", "repo": "o/r"}}})

	code, out := e.REST("GET", "/api/v1/decisions", nil)
	if code != 200 || len(out["decisions"].([]any)) == 0 {
		t.Fatalf("decisions: %d %v", code, out)
	}
	d := out["decisions"].([]any)[0].(map[string]any)
	if code, _ := e.REST("POST", "/api/v1/decisions/"+d["id"].(string), map[string]any{"action": "approve"}); code != 200 {
		t.Fatalf("act: %d", code)
	}
	if _, p := e.REST("GET", "/api/v1/plans/P-1", nil); p["status"] != "approved" {
		t.Fatalf("plan not approved: %v", p["status"])
	}

	if code, out := e.REST("POST", "/api/v1/messages", map[string]any{"to": "agent:w1", "body": "ping"}); code != 200 && code != 201 {
		t.Fatalf("chat send: %d %v", code, out)
	}
	ms := e.Must(wk, "read_thread", map[string]any{"thread": "agent:me"})["messages"].([]any)
	if len(ms) == 0 {
		t.Fatal("agent did not receive the owner chat message")
	}

	if code, _ := e.REST("GET", "/api/v1/briefs/"+bid+"/architecture", nil); code != 200 {
		t.Fatalf("architecture: %d", code)
	}
	if code, out := e.REST("GET", "/api/v1/prompts", nil); code != 200 || out == nil {
		t.Fatalf("prompts: %d", code)
	}
	if code, _ := e.REST("GET", "/api/v1/hillclimb/jobs", nil); code != 200 {
		t.Fatalf("hillclimb: %d", code)
	}
}
