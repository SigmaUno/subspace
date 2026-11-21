package agentout_test

import (
	"net/http"
	"testing"

	"github.com/SigmaUno/subspace/internal/servertest"
)

func TestOutputFollowsLeaderAndAssigneeScope(t *testing.T) {
	e := servertest.New(t)
	rex := e.Register("rex", "leader", "other")
	max := e.Register("max", "leader", "other")
	w1 := e.Register("w1", "worker", "claude-code")
	w2 := e.Register("w2", "worker", "claude-code")

	code, b := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "scoped", "body": "b", "repos": []string{"o/r"},
		"max_cost_eur": 10, "max_parallel_agents": 5, "leader_id": "rex"})
	if code != 201 {
		t.Fatalf("brief: %d %v", code, b)
	}
	plan := e.Must(rex, "propose_plan", map[string]any{"brief_id": b["id"], "tasks": []any{
		map[string]any{"title": "build", "spec": "build it", "repo": "o/r", "acceptance_criteria": []string{"builds"}},
	}})["plan"].(map[string]any)
	if code, out := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	tid := plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
	e.Must(rex, "assign", map[string]any{"task_id": tid, "agent_id": "w1"})

	tagged := map[string]any{"task_id": tid, "stream": "stdout", "text": "tagged"}
	e.Must(rex, "log_output", tagged)
	e.Must(w1, "log_output", tagged)
	e.MustFail(max, "log_output", tagged, "not_found")
	e.MustFail(w2, "log_output", tagged, "not_found")

	if code, out := e.REST("POST", "/api/v1/briefs/"+b["id"].(string)+"/leader", map[string]any{"agent_id": "max"}); code != 200 {
		t.Fatalf("handoff: %d %v", code, out)
	}
	e.MustFail(rex, "log_output", tagged, "not_found")
	e.Must(max, "log_output", tagged)

	e.Must(max, "reassign", map[string]any{"task_id": tid, "agent_id": "w2"})
	e.MustFail(w1, "log_output", tagged, "not_found")
	e.Must(w2, "log_output", tagged)
}

func TestOutputAppendsOnlyToOwnStream(t *testing.T) {
	e := servertest.New(t)
	w1 := e.Register("w1", "worker", "claude-code")
	w2 := e.Register("w2", "worker", "claude-code")
	e.Must(w1, "log_output", map[string]any{"stream": "stdout", "text": "mine"})
	e.Must(w2, "log_output", map[string]any{"agent_id": "w1", "stream": "stdout", "text": "spoof"})

	_, out := e.REST("GET", "/api/v1/agents/w1/output", nil)
	got := lines(t, out)
	if len(got) != 1 || got[0]["text"] != "mine" {
		t.Fatalf("w1 stream: %v", out)
	}
	_, out = e.REST("GET", "/api/v1/agents/w2/output", nil)
	got = lines(t, out)
	if len(got) != 1 || got[0]["text"] != "spoof" || got[0]["agent_id"] != "w2" {
		t.Fatalf("w2 stream: %v", out)
	}
}

func TestOutputEndpointScopes(t *testing.T) {
	e := servertest.New(t)
	w1 := e.Register("w1", "worker", "claude-code")
	e.Must(w1, "log_output", map[string]any{"stream": "stdout", "text": "hello"})
	_, tok := e.REST("POST", "/api/v1/tokens", map[string]any{"name": "ro", "scope": "viewer"})
	viewer := tok["secret"].(string)

	for _, path := range []string{"/api/v1/agents/w1/output", "/api/v1/agent-output"} {
		if code, out := e.RESTAs(viewer, "GET", path, nil); code != 200 {
			t.Fatalf("viewer GET %s: %d %v", path, code, out)
		}
		if code, _ := e.RESTAs(w1, "GET", path, nil); code != 403 {
			t.Fatalf("agent GET %s: %d", path, code)
		}
		if code, _ := e.RESTAs("", "GET", path, nil); code != 401 {
			t.Fatalf("anonymous GET %s: %d", path, code)
		}
		for _, method := range []string{"POST", "PUT", "DELETE"} {
			if code, _ := e.RESTAs(viewer, method, path, map[string]any{"text": "x"}); code != 403 {
				t.Fatalf("viewer %s %s: %d", method, path, code)
			}
			if code, _ := e.REST(method, path, map[string]any{"text": "x"}); code < 400 {
				t.Fatalf("owner %s %s accepted: %d", method, path, code)
			}
		}
	}
	for _, topics := range []string{"output", "output:w1"} {
		s := openSSE(t, e, viewer, topics)
		if s.resp.StatusCode != http.StatusOK {
			t.Fatalf("viewer SSE %s: %d", topics, s.resp.StatusCode)
		}
		s.next(t, "hello")
	}

	call := map[string]any{"name": "log_output", "arguments": map[string]any{"stream": "stdout", "text": "x"}}
	if out := e.RPC(servertest.OwnerToken, "tools/call", call); out["result"] != nil {
		t.Fatalf("owner token accepted on MCP: %v", out)
	}
	if code, _ := e.REST("POST", "/api/v1/agents/w1/revoke", map[string]any{}); code != 200 {
		t.Fatal("revoke failed")
	}
	if out := e.RPC(w1, "tools/call", call); out["result"] != nil {
		t.Fatalf("revoked agent appended: %v", out)
	}
}
