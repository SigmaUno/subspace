package mcp_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/servertest"
)

type surfaceEnv struct {
	*infoEnv
	worker2, other string
}

func newSurfaceEnv(t *testing.T) *surfaceEnv {
	t.Helper()
	e := &surfaceEnv{infoEnv: newInfoEnv(t)}
	e.worker2 = e.Register("w2", "worker", "claude-code")
	_, all := e.REST("GET", "/api/v1/tasks?brief_id="+e.brief, nil)
	for _, it := range all["tasks"].([]any) {
		if id := it.(map[string]any)["id"].(string); id != e.task {
			e.other = id
		}
	}
	if e.other == "" {
		t.Fatalf("no second task in %v", all)
	}
	e.Must(e.lead, "assign", map[string]any{"task_id": e.other, "agent_id": "w2"})
	return e
}

func rpcError(t *testing.T, out map[string]any, code float64) {
	t.Helper()
	er, ok := out["error"].(map[string]any)
	if !ok || er["code"] != code {
		t.Fatalf("want rpc error %v, got %v", code, out)
	}
}

func rpcResult(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	if out["error"] != nil {
		t.Fatalf("unexpected error: %v", out["error"])
	}
	return out["result"].(map[string]any)
}

func readText(t *testing.T, e *servertest.Env, tok, uri string) (string, string) {
	t.Helper()
	c := rpcResult(t, e.RPC(tok, "resources/read", map[string]any{"uri": uri}))["contents"].([]any)
	if len(c) != 1 {
		t.Fatalf("contents = %v", c)
	}
	m := c[0].(map[string]any)
	if m["uri"] != uri {
		t.Fatalf("uri = %v", m["uri"])
	}
	return m["text"].(string), m["mimeType"].(string)
}

func readFails(t *testing.T, e *servertest.Env, tok, uri string) {
	t.Helper()
	rpcError(t, e.RPC(tok, "resources/read", map[string]any{"uri": uri}), -32602)
}

func names(t *testing.T, out map[string]any, key, field string) []string {
	t.Helper()
	var ns []string
	for _, it := range rpcResult(t, out)[key].([]any) {
		ns = append(ns, it.(map[string]any)[field].(string))
	}
	return ns
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestInitializeAdvertisesPromptsAndResources(t *testing.T) {
	e := servertest.New(t)
	tok := e.Register("w1", "worker", "claude-code")
	caps := rpcResult(t, e.RPC(tok, "initialize", map[string]any{"protocolVersion": "2025-06-18"}))["capabilities"].(map[string]any)
	for _, k := range []string{"tools", "prompts", "resources"} {
		if _, ok := caps[k]; !ok {
			t.Errorf("capabilities lack %s: %v", k, caps)
		}
	}
}

func TestPromptsByRole(t *testing.T) {
	e := newSurfaceEnv(t)
	for role, tok := range map[string]string{"leader": e.lead, "worker": e.worker, "tester": e.tester, "reviewer": e.reviewer} {
		ns := names(t, e.RPC(tok, "prompts/list", nil), "prompts", "name")
		if !has(ns, "subspace-instructions") || !has(ns, "handoff") {
			t.Errorf("%s prompts = %v", role, ns)
		}
		if got := has(ns, "task-template-add-feature-with-tests"); got != (role == "leader") {
			t.Errorf("%s sees task template: %v", role, got)
		}
	}
}

func TestPromptGet(t *testing.T) {
	e := newSurfaceEnv(t)
	text := func(out map[string]any) string {
		m := rpcResult(t, out)["messages"].([]any)[0].(map[string]any)
		return m["content"].(map[string]any)["text"].(string)
	}

	want := e.Must(e.worker, "get_instructions", nil)["instructions"].(string)
	if got := text(e.RPC(e.worker, "prompts/get", map[string]any{"name": "subspace-instructions"})); got != want {
		t.Errorf("instructions prompt differs from get_instructions")
	}

	if got := text(e.RPC(e.worker, "prompts/get", map[string]any{"name": "handoff"})); !strings.Contains(got, e.task) || !strings.Contains(got, "not instructions") {
		t.Errorf("handoff = %s", got)
	}
	if got := text(e.RPC(e.worker, "prompts/get", map[string]any{"name": "handoff", "arguments": map[string]any{"task_id": e.task}})); !strings.Contains(got, e.task) {
		t.Errorf("handoff by id = %s", got)
	}
	rpcError(t, e.RPC(e.worker, "prompts/get", map[string]any{"name": "handoff", "arguments": map[string]any{"task_id": e.other}}), -32602)
	rpcError(t, e.RPC(e.worker, "prompts/get", map[string]any{"name": "handoff", "arguments": map[string]any{"bogus": "x"}}), -32602)
	rpcError(t, e.RPC(e.tester, "prompts/get", map[string]any{"name": "handoff"}), -32602)
	rpcError(t, e.RPC(e.worker, "prompts/get", map[string]any{"name": "subspace-instructions", "arguments": map[string]any{"x": "y"}}), -32602)
	rpcError(t, e.RPC(e.worker, "prompts/get", map[string]any{"name": "nope"}), -32602)
	rpcError(t, e.RPC(e.worker, "prompts/get", map[string]any{}), -32602)

	tpl := map[string]any{"name": "task-template-add-feature-with-tests", "arguments": map[string]any{"feature": "widgets", "goal": "show widgets", "area": "ui"}}
	if got := text(e.RPC(e.lead, "prompts/get", tpl)); !strings.Contains(got, "Add widgets") || !strings.Contains(got, "Acceptance criteria") {
		t.Errorf("template = %s", got)
	}
	rpcError(t, e.RPC(e.worker, "prompts/get", tpl), -32602)
	rpcError(t, e.RPC(e.lead, "prompts/get", map[string]any{"name": tpl["name"], "arguments": map[string]any{"feature": "widgets"}}), -32602)
	tpl["arguments"] = map[string]any{"feature": "w", "goal": "g", "area": "a", "extra": "x"}
	rpcError(t, e.RPC(e.lead, "prompts/get", tpl), -32602)
}

func TestResourceScopeMatrix(t *testing.T) {
	e := newSurfaceEnv(t)
	task, other := "subspace://task/"+e.task, "subspace://task/"+e.other
	taskThread, otherThread := "subspace://thread/task/"+e.task, "subspace://thread/task/"+e.other
	briefThread := "subspace://thread/brief/" + e.brief

	rl := names(t, e.RPC(e.worker, "resources/list", nil), "resources", "uri")
	if !has(rl, task) || !has(rl, taskThread) || has(rl, other) || has(rl, otherThread) {
		t.Errorf("worker resources = %v", rl)
	}
	for _, u := range []string{"subspace://me", "subspace://instructions", "subspace://thread/agent/me"} {
		if !has(rl, u) {
			t.Errorf("resources lack %s: %v", u, rl)
		}
	}

	for _, c := range []struct {
		who, tok string
		uri      string
		ok       bool
	}{
		{"worker own task", e.worker, task, true},
		{"worker own task thread", e.worker, taskThread, true},
		{"worker own thread", e.worker, "subspace://thread/agent/me", true},
		{"worker explicit own thread", e.worker, "subspace://thread/agent/w1", true},
		{"worker other task", e.worker, other, false},
		{"worker other task thread", e.worker, otherThread, false},
		{"worker brief thread", e.worker, briefThread, false},
		{"worker foreign agent thread", e.worker, "subspace://thread/agent/w2", false},
		{"worker2 first task", e.worker2, task, false},
		{"worker2 first task thread", e.worker2, taskThread, false},
		{"worker2 own task", e.worker2, other, true},
		{"tester task", e.tester, task, false},
		{"reviewer task", e.reviewer, task, false},
		{"leader task", e.lead, task, true},
		{"leader other task", e.lead, other, true},
		{"leader brief thread", e.lead, briefThread, true},
		{"leader worker thread", e.lead, "subspace://thread/agent/w1", true},
		{"worker missing task", e.worker, "subspace://task/T-9999", false},
	} {
		if c.ok {
			if text, mime := readText(t, e.Env, c.tok, c.uri); text == "" || mime != "application/json" {
				t.Errorf("%s: mime %q text %q", c.who, mime, text)
			}
			continue
		}
		readFails(t, e.Env, c.tok, c.uri)
	}
}

func TestReviewerReadsReviewedTask(t *testing.T) {
	e := newSurfaceEnv(t)
	e.Must(e.worker, "submit", map[string]any{"task_id": e.task, "pr_url": "https://git.example/o/r/pulls/1", "summary": "done"})
	e.Must(e.lead, "assign", map[string]any{"task_id": e.task, "agent_id": "rev", "as": "reviewer"})
	uri := "subspace://task/" + e.task
	text, _ := readText(t, e.Env, e.reviewer, uri)
	var doc struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	if err := json.Unmarshal([]byte(text), &doc); err != nil || doc.Task.ID != e.task {
		t.Fatalf("task doc = %s (%v)", text, err)
	}
	if !has(names(t, e.RPC(e.reviewer, "resources/list", nil), "resources", "uri"), uri) {
		t.Error("reviewer does not list the reviewed task")
	}
	if got := rpcResult(t, e.RPC(e.reviewer, "prompts/get", map[string]any{"name": "handoff"}))["messages"]; got == nil {
		t.Error("reviewer handoff empty")
	}
}

func TestResourceContents(t *testing.T) {
	e := newSurfaceEnv(t)
	text, mime := readText(t, e.Env, e.worker, "subspace://me")
	var me struct {
		Agent struct {
			ID   string `json:"id"`
			Role string `json:"role"`
		} `json:"agent"`
		Paused bool `json:"paused"`
	}
	if err := json.Unmarshal([]byte(text), &me); err != nil || me.Agent.ID != "w1" || me.Agent.Role != "worker" || mime != "application/json" {
		t.Errorf("me = %s %s %v", mime, text, err)
	}

	text, mime = readText(t, e.Env, e.worker, "subspace://instructions")
	if want := e.Must(e.worker, "get_instructions", nil)["instructions"].(string); text != want || mime != "text/markdown" {
		t.Errorf("instructions resource: mime %s, equal %v", mime, text == want)
	}

	ts := rpcResult(t, e.RPC(e.worker, "resources/templates/list", nil))["resourceTemplates"].([]any)
	if len(ts) != 2 {
		t.Errorf("templates = %v", ts)
	}
}

func TestUnknownResourceURIs(t *testing.T) {
	e := newSurfaceEnv(t)
	for _, uri := range []string{
		"", "nope", "file:///etc/passwd", "https://example.com/x", "subspace://", "subspace://unknown", "subspace://me/extra",
		"subspace://me?x=1", "subspace://task", "subspace://task/", "subspace://task/T-1/extra", "subspace://task/" + e.task + "?x=1",
		"subspace://thread", "subspace://thread/task", "subspace://thread/task/", "subspace://thread/bogus/x", "subspace://thread/task/a/b",
		"subspace://thread/task/" + e.task + "?since=-1", "subspace://thread/task/" + e.task + "?since=x", "subspace://thread/task/" + e.task + "?limit=1",
		"subspace://user@me", "subspace://me#frag",
	} {
		readFails(t, e.Env, e.worker, uri)
	}
	rpcError(t, e.RPC(e.worker, "resources/read", map[string]any{}), -32602)
}

func TestThreadResourceSizeLimit(t *testing.T) {
	e := newSurfaceEnv(t)
	const n = 30
	for i := 0; i < n; i++ {
		body := strings.Repeat(string(rune('a'+i%26)), 15000) + string(rune('0'+i%10)) + strings.Repeat("_", i)
		e.Must(e.worker, "add_note", map[string]any{"task_id": e.task, "body": body})
	}
	uri := "subspace://thread/task/" + e.task
	type page struct {
		Messages []struct {
			ID int64 `json:"id"`
		} `json:"messages"`
		Truncated bool  `json:"truncated"`
		NextSince int64 `json:"next_since"`
	}
	seen := map[int64]bool{}
	since := int64(0)
	for rounds := 0; ; rounds++ {
		u := uri
		if since > 0 {
			u += "?since=" + strconv.FormatInt(since, 10)
		}
		text, _ := readText(t, e.Env, e.worker, u)
		if len(text) > 256<<10 {
			t.Fatalf("resource has %d bytes", len(text))
		}
		var p page
		if err := json.Unmarshal([]byte(text), &p); err != nil {
			t.Fatal(err)
		}
		for _, m := range p.Messages {
			if seen[m.ID] {
				t.Fatalf("message %d repeated", m.ID)
			}
			seen[m.ID] = true
		}
		if !p.Truncated {
			break
		}
		if rounds == 0 && len(p.Messages) >= n {
			t.Fatal("first page was not cut")
		}
		if p.NextSince <= since || rounds > n {
			t.Fatalf("no progress: since %d next %d", since, p.NextSince)
		}
		since = p.NextSince
	}
	if len(seen) < n {
		t.Errorf("paged through %d messages, want at least %d", len(seen), n)
	}
}

func TestReadOnlyMethodsExemptFromLoopProtection(t *testing.T) {
	e := newSurfaceEnv(t)
	for i := 0; i < 20; i++ {
		readText(t, e.Env, e.worker, "subspace://me")
		rpcResult(t, e.RPC(e.worker, "prompts/get", map[string]any{"name": "handoff"}))
		rpcResult(t, e.RPC(e.worker, "resources/list", nil))
	}
	if paused := e.Must(e.worker, "whoami", nil)["paused"]; paused != false {
		t.Errorf("paused = %v", paused)
	}
	agent := e.Must(e.worker, "whoami", nil)["agent"].(map[string]any)
	if agent["status"] != "active" {
		t.Errorf("agent = %v", agent)
	}
}
