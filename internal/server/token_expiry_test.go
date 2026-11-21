package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func mcpStatus(t *testing.T, e *servertest.Env, token string) (int, map[string]any, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "whoami", "arguments": map[string]any{}}})
	req, _ := http.NewRequest("POST", e.TS.URL+"/mcp", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, resp.Header.Get("WWW-Authenticate")
}

func expiresAt(t *testing.T, e *servertest.Env, id string) any {
	t.Helper()
	code, a := e.REST("GET", "/api/v1/agents/"+id, nil)
	if code != 200 {
		t.Fatalf("get agent %s: %d %v", id, code, a)
	}
	return a["token_expires_at"]
}

func connectToken(t *testing.T, out map[string]any) string {
	t.Helper()
	return out["connect"].(map[string]any)["token"].(string)
}

func decisionIDs(t *testing.T, e *servertest.Env) map[string]map[string]any {
	t.Helper()
	code, out := e.REST("GET", "/api/v1/decisions", nil)
	if code != 200 {
		t.Fatalf("decisions: %d %v", code, out)
	}
	ids := map[string]map[string]any{}
	for _, d := range out["decisions"].([]any) {
		m := d.(map[string]any)
		ids[m["id"].(string)] = m
	}
	return ids
}

func TestTokenExpiryLifecycle(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	clk := &fakeClock{t: t0}
	e := servertest.Start(t, server.Config{PublicURL: "http://sub.test", Now: clk.Now,
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})

	for _, bad := range []string{"soon", "-1h", "0s"} {
		code, out := e.REST("POST", "/api/v1/agents", map[string]any{"id": "bad", "role": "worker", "expires_in": bad})
		if code != 400 {
			t.Fatalf("expires_in %q: %d %v", bad, code, out)
		}
	}

	code, reg := e.REST("POST", "/api/v1/agents", map[string]any{"id": "w1", "role": "worker", "kind": "claude-code", "expires_in": "48h"})
	if code != 201 {
		t.Fatalf("register: %d %v", code, reg)
	}
	tok := connectToken(t, reg)
	if got := expiresAt(t, e, "w1"); got != t0.Add(48*time.Hour).Format(time.RFC3339) {
		t.Fatalf("token_expires_at = %v", got)
	}
	forever := e.Register("w-forever", "worker", "claude-code")
	if got := expiresAt(t, e, "w-forever"); got != nil {
		t.Fatalf("token_expires_at should be null: %v", got)
	}

	if st, _, _ := mcpStatus(t, e, tok); st != 200 {
		t.Fatalf("fresh token: %d", st)
	}
	if _, ok := decisionIDs(t, e)["token:w1"]; ok {
		t.Fatal("no decision expected before the warning window")
	}

	clk.Advance(25 * time.Hour)
	d, ok := decisionIDs(t, e)["token:w1"]
	if !ok {
		t.Fatal("expected a token decision within 24h of expiry")
	}
	if d["kind"] != "token_expiry" || d["agent_id"] != "w1" || len(d["actions"].([]any)) != 2 || d["actions"].([]any)[0] != "rotate" {
		t.Fatalf("decision: %v", d)
	}
	if _, ok := decisionIDs(t, e)["token:w-forever"]; ok {
		t.Fatal("agent without expiry must not raise a decision")
	}
	if st, _, _ := mcpStatus(t, e, tok); st != 200 {
		t.Fatalf("token still valid inside the window: %d", st)
	}

	code, out := e.REST("POST", "/api/v1/decisions/token:w1", map[string]any{"action": "rotate", "expires_in": "soon"})
	if code != 400 {
		t.Fatalf("bad rotate payload: %d %v", code, out)
	}
	code, out = e.REST("POST", "/api/v1/decisions/token:w1", map[string]any{"action": "rotate", "expires_in": "72h"})
	if code != 200 {
		t.Fatalf("rotate decision: %d %v", code, out)
	}
	res := out["result"].(map[string]any)
	newTok := connectToken(t, res)
	if got := res["agent"].(map[string]any)["token_expires_at"]; got != clk.Now().Add(72*time.Hour).Format(time.RFC3339) {
		t.Fatalf("rotated expiry = %v", got)
	}
	if _, ok := decisionIDs(t, e)["token:w1"]; ok {
		t.Fatal("decision should be gone after rotating")
	}
	if st, _, _ := mcpStatus(t, e, tok); st != 401 {
		t.Fatalf("old token after rotate: %d", st)
	}
	if st, _, _ := mcpStatus(t, e, newTok); st != 200 {
		t.Fatalf("new token: %d", st)
	}

	clk.Advance(72*time.Hour + time.Second)
	st, body, challenge := mcpStatus(t, e, newTok)
	if st != 401 || body["error"].(map[string]any)["code"] != "token_expired" || challenge == "" {
		t.Fatalf("expired token: %d %v %q", st, body, challenge)
	}
	if st, _, _ := mcpStatus(t, e, "ssp_unknown"); st != 401 {
		t.Fatalf("unknown token: %d", st)
	}
	if _, ok := decisionIDs(t, e)["token:w1"]; !ok {
		t.Fatal("expired token should raise a decision")
	}
	if st, _, _ := mcpStatus(t, e, forever); st != 200 {
		t.Fatalf("token without expiry: %d", st)
	}

	if code, out := e.REST("POST", "/api/v1/agents/w1/rotate-token", map[string]any{}); code != 400 {
		t.Fatalf("keeping an expired expiry: %d %v", code, out)
	}
	code, out = e.REST("POST", "/api/v1/agents/w1/rotate-token", map[string]any{"expires_in": "1h"})
	if code != 200 {
		t.Fatalf("rotate expired: %d %v", code, out)
	}
	if st, _, _ := mcpStatus(t, e, connectToken(t, out)); st != 200 {
		t.Fatalf("token after rotate: %d", st)
	}
}

func TestTokenExpiryRotateKeepsAndBulk(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	clk := &fakeClock{t: t0}
	e := servertest.Start(t, server.Config{PublicURL: "http://sub.test", Now: clk.Now,
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})

	code, out := e.REST("POST", "/api/v1/agents/bulk", map[string]any{"prefix": "bulk", "count": 3, "role": "worker", "kind": "codex", "expires_in": "10h"})
	if code != 201 {
		t.Fatalf("bulk: %d %v", code, out)
	}
	want := t0.Add(10 * time.Hour).Format(time.RFC3339)
	for _, id := range []string{"bulk-001", "bulk-002", "bulk-003"} {
		if got := expiresAt(t, e, id); got != want {
			t.Fatalf("%s token_expires_at = %v", id, got)
		}
	}

	clk.Advance(time.Hour)
	if code, out := e.REST("POST", "/api/v1/agents/bulk-001/rotate-token", map[string]any{}); code != 200 {
		t.Fatalf("rotate: %d %v", code, out)
	}
	if got := expiresAt(t, e, "bulk-001"); got != want {
		t.Fatalf("rotate without expires_in must keep the expiry, got %v", got)
	}

	_, list := e.REST("GET", "/api/v1/agents", nil)
	for _, a := range list["agents"].([]any) {
		if a.(map[string]any)["token_expires_at"] != want {
			t.Fatalf("agent list: %v", a)
		}
	}

	e.Register("plain", "worker", "claude-code")
	if code, _ := e.REST("POST", "/api/v1/agents/plain/rotate-token", map[string]any{}); code != 200 {
		t.Fatal("rotate plain")
	}
	if got := expiresAt(t, e, "plain"); got != nil {
		t.Fatalf("rotate must not add an expiry: %v", got)
	}
}
