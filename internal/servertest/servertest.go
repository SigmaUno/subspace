package servertest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/testdb"
)

const OwnerToken = "owner-token-for-tests-0123456789"

type Env struct {
	T   *testing.T
	TS  *httptest.Server
	Srv *server.Server
}

func New(t *testing.T) *Env {
	t.Helper()
	return Start(t, server.Config{PublicURL: "http://sub.test",
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})
}

func Start(t *testing.T, cfg server.Config) *Env {
	t.Helper()
	for _, k := range []string{"SUBSPACE_RATE_MCP", "SUBSPACE_RATE_AUTHFAIL"} {
		if _, ok := os.LookupEnv(k); !ok {
			_ = os.Setenv(k, "off")
		}
	}
	if cfg.DBPath == "" {
		cfg.DBPath = DB(t)
	}
	if strings.HasPrefix(cfg.DBPath, "postgres") {
		if cfg.DataDir == "" {
			cfg.DataDir = t.TempDir()
		}
		if cfg.BackupDir == "" {
			cfg.BackupDir = filepath.Join(cfg.DataDir, "backups")
		}
	}
	if cfg.OwnerToken == "" {
		cfg.OwnerToken = OwnerToken
	}
	ctx, cancel := context.WithCancel(context.Background())
	s, err := server.New(ctx, cfg)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	go s.Board.Run(ctx)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); cancel(); _ = s.Close() })
	return &Env{T: t, TS: ts, Srv: s}
}

func DB(t *testing.T) string {
	t.Helper()
	return testdb.Path(t, "t.db")
}

func (e *Env) REST(method, path string, body any) (int, map[string]any) {
	e.T.Helper()
	return e.RESTAs(OwnerToken, method, path, body)
}

func (e *Env) RESTAs(tok, method, path string, body any) (int, map[string]any) {
	e.T.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, e.TS.URL+path, rd)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.T.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (e *Env) Register(id, role, kind string) string {
	e.T.Helper()
	code, out := e.REST("POST", "/api/v1/agents", map[string]any{"id": id, "role": role, "kind": kind, "model": "m"})
	if code != 201 {
		e.T.Fatalf("register %s: %d %v", id, code, out)
	}
	return out["connect"].(map[string]any)["token"].(string)
}

func (e *Env) Call(tok, tool string, args any) (map[string]any, bool) {
	e.T.Helper()
	res := e.RPC(tok, "tools/call", map[string]any{"name": tool, "arguments": args})
	if res["error"] != nil {
		return map[string]any{"rpc_error": res["error"]}, true
	}
	r := res["result"].(map[string]any)
	sc, _ := r["structuredContent"].(map[string]any)
	return sc, r["isError"] == true
}

func (e *Env) RPC(tok, method string, params any) map[string]any {
	e.T.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, _ := http.NewRequest("POST", e.TS.URL+"/mcp", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.T.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func (e *Env) Must(tok, tool string, args any) map[string]any {
	e.T.Helper()
	out, isErr := e.Call(tok, tool, args)
	if isErr {
		e.T.Fatalf("%s failed: %v", tool, out)
	}
	return out
}

func (e *Env) MustFail(tok, tool string, args any, code string) {
	e.T.Helper()
	out, isErr := e.Call(tok, tool, args)
	if !isErr {
		e.T.Fatalf("%s should fail with %s, got %v", tool, code, out)
	}
	if _, rpcErr := out["rpc_error"]; rpcErr && code == "" {
		return
	}
	if got, _ := out["error"].(map[string]any)["code"].(string); got != code {
		e.T.Fatalf("%s: code %q want %q (%v)", tool, got, code, out)
	}
}
