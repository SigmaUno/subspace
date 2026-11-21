package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTokenEnv(t *testing.T) (*servertest.Env, *fakeClock) {
	t.Helper()
	clk := &fakeClock{now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	t.Cleanup(server.SetTokenClock(clk.Now))
	return servertest.New(t), clk
}

func createToken(e *servertest.Env, name, scope string, extra map[string]any) (string, string) {
	e.T.Helper()
	body := map[string]any{"name": name, "scope": scope}
	for k, v := range extra {
		body[k] = v
	}
	code, out := e.REST("POST", "/api/v1/tokens", body)
	if code != 201 {
		e.T.Fatalf("create token: %d %v", code, out)
	}
	secret := out["secret"].(string)
	if !strings.HasPrefix(secret, "ssu_") {
		e.T.Fatalf("secret %q lacks ssu_ prefix", secret)
	}
	return out["token"].(map[string]any)["id"].(string), secret
}

func listedToken(e *servertest.Env, id string) map[string]any {
	e.T.Helper()
	code, out := e.REST("GET", "/api/v1/tokens", nil)
	if code != 200 {
		e.T.Fatalf("list tokens: %d %v", code, out)
	}
	for _, x := range out["tokens"].([]any) {
		if t := x.(map[string]any); t["id"] == id {
			return t
		}
	}
	e.T.Fatalf("token %s not listed in %v", id, out)
	return nil
}

var matrixVar = regexp.MustCompile(`\{[^}]+\}`)

type matrixReq struct{ method, path string }

func matrixRequests(patterns []string) []matrixReq {
	var out []matrixReq
	for _, p := range patterns {
		method, path, ok := strings.Cut(p, " ")
		if !ok {
			method, path = "", p
		}
		path = matrixVar.ReplaceAllString(path, "X-999")
		if strings.HasSuffix(path, "/") {
			path += "matrix"
		}
		methods := []string{method}
		if method == "" {
			methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE"}
		}
		for _, m := range methods {
			out = append(out, matrixReq{m, path})
		}
	}
	return out
}

func matrixDo(t *testing.T, base, tok string, r matrixReq) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var body io.Reader
	if r.method != "GET" && r.method != "DELETE" {
		body = strings.NewReader("{}")
	}
	req, err := http.NewRequestWithContext(ctx, r.method, base+r.path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", r.method, r.path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		return resp.StatusCode, ""
	}
	var e struct{ Error struct{ Code string } }
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e)
	return resp.StatusCode, e.Error.Code
}

func TestTokenScopeMatrixCoversEveryRoute(t *testing.T) {
	e, _ := newTokenEnv(t)
	agent := e.Register("w1", "worker", "claude-code")
	_, viewer := createToken(e, "ro", "viewer", nil)
	_, owner := createToken(e, "rw", "owner", nil)

	reqs := matrixRequests(server.RoutePatterns(e.Srv))
	if len(reqs) < 60 {
		t.Fatalf("only %d requests generated from mux patterns", len(reqs))
	}
	base := e.TS.URL
	for _, r := range reqs {
		read := r.method == "GET" && r.path != "/api/v1/tokens" && !strings.HasPrefix(r.path, "/api/v1/tokens/")
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			if code, _ := matrixDo(t, base, "", r); code != http.StatusUnauthorized {
				t.Errorf("anonymous: %d, want 401", code)
			}
			if code, ec := matrixDo(t, base, agent, r); code != http.StatusForbidden || ec != "forbidden" {
				t.Errorf("agent: %d %q, want 403 forbidden", code, ec)
			}
			code, ec := matrixDo(t, base, viewer, r)
			switch {
			case read && (code == http.StatusUnauthorized || code == http.StatusForbidden):
				t.Errorf("viewer read: %d %q", code, ec)
			case !read && (code != http.StatusForbidden || ec != "forbidden"):
				t.Errorf("viewer write: %d %q, want 403 forbidden", code, ec)
			}
			if code, ec := matrixDo(t, base, owner, r); code == http.StatusUnauthorized || code == http.StatusForbidden {
				t.Errorf("owner: %d %q", code, ec)
			}
		})
	}
}

func TestTokenMeExpiryAndRevocation(t *testing.T) {
	e, clk := newTokenEnv(t)

	if code, out := e.REST("POST", "/api/v1/tokens", map[string]any{"name": "x", "scope": "admin"}); code != 400 {
		t.Fatalf("bad scope: %d %v", code, out)
	}
	if code, out := e.REST("POST", "/api/v1/tokens", map[string]any{"name": "x", "scope": "viewer", "expires_at": clk.Now().Add(-time.Second)}); code != 400 {
		t.Fatalf("past expiry: %d %v", code, out)
	}

	_, viewer := createToken(e, "ro", "viewer", map[string]any{"expires_at": clk.Now().Add(time.Hour)})
	ownerID, owner := createToken(e, "rw", "owner", nil)

	for tok, scope := range map[string]string{viewer: "viewer", owner: "owner", servertest.OwnerToken: "owner"} {
		code, me := e.RESTAs(tok, "GET", "/api/v1/me", nil)
		if code != 200 || me["role"] != "owner" || me["scope"] != scope {
			t.Fatalf("me for %s: %d %v", scope, code, me)
		}
	}
	if code, _ := e.RESTAs(viewer, "GET", "/api/v1/tokens", nil); code != 403 {
		t.Fatalf("viewer listed tokens: %d", code)
	}

	clk.Add(time.Hour)
	if code, _ := e.RESTAs(viewer, "GET", "/api/v1/state", nil); code != 401 {
		t.Fatalf("expired viewer: %d", code)
	}

	if code, _ := e.RESTAs(owner, "POST", "/api/v1/pause-all", map[string]any{"reason": "drill"}); code != 200 {
		t.Fatalf("owner pause-all: %d", code)
	}
	code, out := e.REST("GET", "/api/v1/audit?tool=pause_all", nil)
	evs := out["events"].([]any)
	if code != 200 || len(evs) != 1 || evs[0].(map[string]any)["note"] != "token="+ownerID+"; drill" {
		t.Fatalf("pause_all audit: %d %v", code, out)
	}

	if code, out := e.RESTAs(owner, "POST", "/api/v1/tokens/"+ownerID+"/revoke", nil); code != 200 || out["revoked_at"] == nil {
		t.Fatalf("revoke: %d %v", code, out)
	}
	if code, _ := e.RESTAs(owner, "GET", "/api/v1/state", nil); code != 401 {
		t.Fatalf("revoked owner token: %d", code)
	}
	if code, _ := e.REST("POST", "/api/v1/tokens/"+ownerID+"/revoke", nil); code != 409 {
		t.Fatalf("second revoke: %d", code)
	}
	if code, _ := e.REST("POST", "/api/v1/tokens/K-999/revoke", nil); code != 404 {
		t.Fatalf("unknown revoke: %d", code)
	}

	_, out = e.REST("GET", "/api/v1/audit?tool=revoke_token", nil)
	evs = out["events"].([]any)
	if len(evs) != 1 || evs[0].(map[string]any)["note"] != "token="+ownerID {
		t.Fatalf("revoke audit: %v", out)
	}
	_, out = e.REST("GET", "/api/v1/audit?tool=create_token", nil)
	if evs = out["events"].([]any); len(evs) != 2 || evs[0].(map[string]any)["note"] != "token=root" {
		t.Fatalf("create audit: %v", out)
	}
	for _, x := range listedToken(e, ownerID) {
		if s, ok := x.(string); ok && strings.HasPrefix(s, "ssu_") {
			t.Fatalf("token list leaks a secret: %v", x)
		}
	}
}

func TestTokenLastUsedIsThrottled(t *testing.T) {
	e, clk := newTokenEnv(t)
	id, tok := createToken(e, "ro", "viewer", nil)
	lastUsed := func() any { return listedToken(e, id)["last_used_at"] }
	use := func() {
		if code, _ := e.RESTAs(tok, "GET", "/api/v1/state", nil); code != 200 {
			t.Fatalf("use token: %d", code)
		}
	}
	at := func(ts time.Time) string { return ts.UTC().Format(time.RFC3339Nano) }

	if v := lastUsed(); v != nil {
		t.Fatalf("unused token has last_used_at %v", v)
	}
	clk.Add(time.Second)
	first := clk.Now()
	use()
	if v := lastUsed(); v != at(first) {
		t.Fatalf("last_used_at %v, want %s", v, at(first))
	}
	clk.Add(59 * time.Second)
	use()
	use()
	if v := lastUsed(); v != at(first) {
		t.Fatalf("last_used_at %v moved within the throttle window, want %s", v, at(first))
	}
	clk.Add(time.Second)
	use()
	if v := lastUsed(); v != at(clk.Now()) {
		t.Fatalf("last_used_at %v, want %s", v, at(clk.Now()))
	}
}
