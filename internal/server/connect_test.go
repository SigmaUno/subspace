package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConnectSnippetsOverREST(t *testing.T) {
	e := newEnv(t)

	out := e.restWant(201, "POST", "/api/v1/agents", map[string]any{"id": "claude-1", "role": "worker", "kind": "claude-code", "model": "m"})
	reg := out["connect"].(map[string]any)
	tok := reg["token"].(string)
	want := func(c map[string]any, bearer string) {
		t.Helper()
		sn := c["snippets"].(map[string]any)
		if c["agent_id"] != "claude-1" || c["mcp_url"] != "http://sub.test/mcp" {
			t.Fatalf("connect identity: %v", c)
		}
		if c["join_line"] != "Join Subspace at http://sub.test as claude-1. Call get_instructions and follow them." {
			t.Fatalf("join_line: %v", c["join_line"])
		}
		if sn["claude_code"] != `claude mcp add --transport http subspace http://sub.test/mcp --header "Authorization: Bearer `+bearer+`"` {
			t.Fatalf("claude_code: %v", sn["claude_code"])
		}
		if sn["codex_toml"] != "[mcp_servers.subspace]\nurl = \"http://sub.test/mcp\"\nhttp_headers = { Authorization = \"Bearer "+bearer+"\" }\n" {
			t.Fatalf("codex_toml: %q", sn["codex_toml"])
		}
		if sn["opencode_json"] != `{"mcp":{"subspace":{"type":"remote","url":"http://sub.test/mcp","headers":{"Authorization":"Bearer `+bearer+`"}}}}` {
			t.Fatalf("opencode_json: %v", sn["opencode_json"])
		}
	}
	want(reg, tok)

	got := e.restWant(200, "GET", "/api/v1/agents/claude-1/connect", nil)
	if _, ok := got["token"]; ok {
		t.Fatalf("GET connect leaked a token: %v", got)
	}
	want(got, "<token>")
	if raw, _ := json.Marshal(got); strings.Contains(string(raw), tok) {
		t.Fatal("GET connect response contains the agent token")
	}

	rotated := e.restWant(200, "POST", "/api/v1/agents/claude-1/rotate-token", map[string]any{})["connect"].(map[string]any)
	fresh := rotated["token"].(string)
	want(rotated, fresh)

	e.restWant(404, "GET", "/api/v1/agents/nobody/connect", nil)
}
