package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/ratelimit"
)

func (e *env) raw(method, path, tok string) (*http.Response, map[string]any) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.ts.URL+path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func rateLimitedBody(t *testing.T, resp *http.Response, out map[string]any) {
	t.Helper()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
	if code := out["error"].(map[string]any)["code"]; code != "rate_limited" {
		t.Fatalf("error code = %v, want rate_limited", code)
	}
}

func TestMCPRateLimitIsPerAgent(t *testing.T) {
	t.Setenv(envRateMCP, "1/m:3")
	e := newEnv(t)
	a := e.register("w-a", "worker", "claude-code")
	b := e.register("w-b", "worker", "claude-code")

	for i := 0; i < 3; i++ {
		if code := e.mcpStatus(a); code != http.StatusOK {
			t.Fatalf("call %d status = %d", i, code)
		}
	}
	resp, out := e.raw("POST", "/mcp", a)
	rateLimitedBody(t, resp, out)
	if code := e.mcpStatus(b); code != http.StatusOK {
		t.Fatalf("other agent throttled: %d", code)
	}
	if code, _ := e.rest("GET", "/api/v1/state", nil); code != http.StatusOK {
		t.Fatalf("REST affected by MCP limit: %d", code)
	}
}

func TestAuthFailureLockoutBlocksIP(t *testing.T) {
	t.Setenv(envRateAuthFail, "1/m:3")
	e := newEnv(t)
	agent := e.register("w-a", "worker", "claude-code")

	for i := 0; i < 4; i++ {
		resp, _ := e.raw("GET", "/api/v1/state", "wrong")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401", i, resp.StatusCode)
		}
	}
	resp, out := e.raw("GET", "/api/v1/state", ownerTok)
	rateLimitedBody(t, resp, out)
	resp, out = e.raw("POST", "/mcp", agent)
	rateLimitedBody(t, resp, out)
	if resp, _ := e.raw("GET", "/healthz", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d", resp.StatusCode)
	}
}

func TestAuthFailureLockoutExpires(t *testing.T) {
	t.Setenv(envRateAuthFail, "1/m:2")
	clock := ratelimit.NewFakeClock(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))
	unauthorized := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}
	})
	h := newRateLimited(unauthorized, Deps{}, clock)
	do := func(auth string) int {
		req := httptest.NewRequest("GET", "/api/v1/state", nil)
		req.RemoteAddr = "203.0.113.9:4000"
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := 0; i < 3; i++ {
		if code := do(""); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d", i, code)
		}
	}
	if code := do("Bearer x"); code != http.StatusTooManyRequests {
		t.Fatalf("locked status = %d", code)
	}
	clock.Advance(59 * time.Second)
	if code := do("Bearer x"); code != http.StatusTooManyRequests {
		t.Fatalf("still locked status = %d", code)
	}
	clock.Advance(2 * time.Second)
	if code := do("Bearer x"); code != http.StatusOK {
		t.Fatalf("after lockout status = %d", code)
	}
}
