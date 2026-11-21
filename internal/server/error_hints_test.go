package server_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/servertest"
)

type hintEnv struct {
	*servertest.Env
	lead, wk, other string
	task            string
}

func newHintEnv(t *testing.T) *hintEnv {
	t.Helper()
	e := servertest.New(t)
	h := &hintEnv{Env: e, lead: e.Register("rex", "leader", "other"), wk: e.Register("claude-1", "worker", "claude-code"), other: e.Register("claude-2", "worker", "claude-code")}
	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "Demo", "body": "do it", "repos": []string{"o/r"}, "max_cost_eur": 10, "max_parallel_agents": 5})
	plan := e.Must(h.lead, "propose_plan", map[string]any{"brief_id": br["id"], "tasks": []any{
		map[string]any{"ref": "a", "title": "Add login", "spec": "build it", "repo": "o/r", "acceptance_criteria": []string{"works"}},
	}})["plan"].(map[string]any)
	h.task = plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
	if code, out := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	e.Must(h.lead, "assign", map[string]any{"task_id": h.task, "agent_id": "claude-1"})
	return h
}

func (h *hintEnv) failure(tok, tool string, args any, code string) map[string]any {
	h.T.Helper()
	out, isErr := h.Call(tok, tool, args)
	if !isErr {
		h.T.Fatalf("%s should fail with %s: %v", tool, code, out)
	}
	e, _ := out["error"].(map[string]any)
	if e["code"] != code {
		h.T.Fatalf("%s: %v want code %s", tool, e, code)
	}
	if e["hint"] != mcp.Hint(code, tool) || e["hint"] == "" {
		h.T.Fatalf("%s: hint %v want %q", tool, e["hint"], mcp.Hint(code, tool))
	}
	return e
}

func TestErrorHintNotAssignedAndClaimConflict(t *testing.T) {
	h := newHintEnv(t)
	e := h.failure(h.other, "claim_task", map[string]any{"task_id": h.task}, "not_assigned")
	if !strings.Contains(e["hint"].(string), "list_my_tasks") {
		t.Fatalf("hint: %v", e["hint"])
	}
	h.Must(h.wk, "claim_task", map[string]any{"task_id": h.task})
	e = h.failure(h.wk, "claim_task", map[string]any{"task_id": h.task}, "conflict")
	if !strings.Contains(e["hint"].(string), "wait for the leader") {
		t.Fatalf("hint: %v", e["hint"])
	}
}

func TestErrorHintPausedInTextContent(t *testing.T) {
	h := newHintEnv(t)
	if code, out := h.REST("POST", "/api/v1/pause-all", map[string]any{"reason": "test"}); code != 200 {
		t.Fatalf("pause: %d %v", code, out)
	}
	res := h.RPC(h.wk, "tools/call", map[string]any{"name": "claim_task", "arguments": map[string]any{"task_id": h.task}})["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatalf("claim while paused: %v", res)
	}
	var text struct{ Error struct{ Code, Hint string } }
	if err := json.Unmarshal([]byte(res["content"].([]any)[0].(map[string]any)["text"].(string)), &text); err != nil {
		t.Fatal(err)
	}
	if text.Error.Code != "paused" || !strings.Contains(text.Error.Hint, "heartbeat") {
		t.Fatalf("text content: %+v", text)
	}
	sc := res["structuredContent"].(map[string]any)["error"].(map[string]any)
	if sc["hint"] != text.Error.Hint {
		t.Fatalf("structured hint %v differs from text %q", sc["hint"], text.Error.Hint)
	}
}

func TestErrorHintAgentPausedByLoopProtection(t *testing.T) {
	h := newHintEnv(t)
	h.Must(h.wk, "claim_task", map[string]any{"task_id": h.task})
	args := map[string]any{"task_id": h.task, "body": "same note"}
	for range 5 {
		h.Must(h.wk, "add_note", args)
	}
	e := h.failure(h.wk, "add_note", args, "agent_paused")
	if !strings.Contains(e["hint"].(string), "wait for the owner") {
		t.Fatalf("hint: %v", e["hint"])
	}
	h.failure(h.wk, "add_note", map[string]any{"task_id": h.task, "body": "other"}, "agent_paused")
}
