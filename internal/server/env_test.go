package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/testdb"
)

const ownerTok = "owner-token-for-tests-0123456789"

type env struct {
	t   *testing.T
	ts  *httptest.Server
	srv *Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s, err := New(ctx, Config{DBPath: testdb.Path(t, "t.db"), DataDir: testdb.DataDir(t), OwnerToken: ownerTok, PublicURL: "http://sub.test",
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	go s.Board.Run(ctx)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); cancel(); _ = s.Close() })
	return &env{t: t, ts: ts, srv: s}
}

func (e *env) rest(method, path string, body any) (int, map[string]any) {
	e.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+ownerTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (e *env) register(id, role, kind string) string {
	e.t.Helper()
	code, out := e.rest("POST", "/api/v1/agents", map[string]any{"id": id, "role": role, "kind": kind, "model": "m"})
	if code != 201 {
		e.t.Fatalf("register %s: %d %v", id, code, out)
	}
	return out["connect"].(map[string]any)["token"].(string)
}

func (e *env) call(tok, tool string, args any) (map[string]any, bool) {
	e.t.Helper()
	res := e.rpc(tok, "tools/call", map[string]any{"name": tool, "arguments": args})
	if res["error"] != nil {
		return map[string]any{"rpc_error": res["error"]}, true
	}
	r := res["result"].(map[string]any)
	sc, _ := r["structuredContent"].(map[string]any)
	return sc, r["isError"] == true
}

func (e *env) rpc(tok, method string, params any) map[string]any {
	e.t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, _ := http.NewRequest("POST", e.ts.URL+"/mcp", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func (e *env) must(tok, tool string, args any) map[string]any {
	e.t.Helper()
	out, isErr := e.call(tok, tool, args)
	if isErr {
		e.t.Fatalf("%s failed: %v", tool, out)
	}
	return out
}

func (e *env) mustFail(tok, tool string, args any, code string) {
	e.t.Helper()
	out, isErr := e.call(tok, tool, args)
	if !isErr {
		e.t.Fatalf("%s should fail with %s, got %v", tool, code, out)
	}
	if _, rpcErr := out["rpc_error"]; rpcErr && code == "" {
		return
	}
	if got, _ := out["error"].(map[string]any)["code"].(string); got != code {
		e.t.Fatalf("%s: code %q want %q (%v)", tool, got, code, out)
	}
}

func status(m map[string]any) string { return m["task"].(map[string]any)["status"].(string) }
