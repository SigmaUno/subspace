package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SigmaUno/subspace/internal/model"
)

type fuzzLookup map[string]*model.Agent

func (l fuzzLookup) LookupToken(tok string) (*model.Agent, error) { return l[tok], nil }

var fuzzRoles = []string{model.RoleLeader, model.RoleWorker, model.RoleTester, model.RoleReviewer}

// newFuzzServer builds a transport over the default tool table (same names, roles and schemas)
// with stub handlers, so fuzzed tools/call requests never need a board.
func newFuzzServer() (*Server, []string) {
	reg := NewRegistry0()
	for _, role := range fuzzRoles {
		for _, t := range DefaultRegistry().List(role) {
			if _, ok := reg.tools[t.Name]; ok {
				continue
			}
			cp := *t
			cp.Handler = func(_ context.Context, c *Call) (any, error) {
				var v any
				if err := decode(c, &v); err != nil {
					return nil, err
				}
				if _, ok := v.(map[string]any); !ok && v != nil {
					return nil, model.BadRequest("arguments must be an object")
				}
				return map[string]any{"ok": true, "args": v}, nil
			}
			reg.Register(&cp)
		}
	}
	lookup := fuzzLookup{}
	tokens := make([]string, len(fuzzRoles))
	for i, role := range fuzzRoles {
		tokens[i] = "ssp_fuzz_" + role
		lookup[tokens[i]] = &model.Agent{ID: "fuzz-" + role, Role: role, Status: model.AgentActive}
	}
	return NewServer(lookup, reg), tokens
}

var fuzzSeeds = []string{
	``,
	`{`,
	`null`,
	`[]`,
	`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`,
	`"jsonrpc"`,
	`{"jsonrpc":"1.0","id":1,"method":"ping"}`,
	`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`,
	`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
	`{"jsonrpc":"2.0","id":"abc","method":"ping"}`,
	`{"jsonrpc":"2.0","id":null,"method":"ping"}`,
	`{"jsonrpc":"2.0","id":{"a":[1,2]},"method":"ping"}`,
	`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
	`{"jsonrpc":"2.0","id":4,"method":"nope"}`,
	`{"jsonrpc":"2.0","id":5,"method":""}`,
	`{"jsonrpc":"2.0","id":6,"method":"tools/call"}`,
	`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":null}`,
	`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":[]}`,
	`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":""}}`,
	`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"no_such_tool","arguments":{}}}`,
	`{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"get_instructions"}}`,
	`{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"claim_task","arguments":{"task_id":"T-1"}}}`,
	`{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{"name":"assign","arguments":"x"}}`,
	`{"jsonrpc":"2.0","id":14,"method":"tools/call","params":{"name":"review","arguments":[1,2,3]}}`,
	`{"jsonrpc":"2.0","id":15,"method":"tools/call","params":{"name":"heartbeat","arguments":null}}`,
	`{"jsonrpc":"2.0","id":1e400,"method":"ping"}`,
	"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"p\\u0000ing\"}",
	"\xff\xfe\x00{}",
}

// FuzzJSONRPC POSTs arbitrary bodies to the handler. Under plain `go test` only the seeds run.
func FuzzJSONRPC(f *testing.F) {
	srv, tokens := newFuzzServer()
	for i, s := range fuzzSeeds {
		f.Add([]byte(s), uint8(i), i%3 == 0)
	}
	f.Fuzz(func(t *testing.T, body []byte, role uint8, withSession bool) {
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokens[int(role)%len(tokens)])
		req.Header.Set("Content-Type", "application/json")
		if withSession {
			req.Header.Set("Mcp-Session-Id", "unknown-session")
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		checkRPCResponse(t, rec, withSession)
	})
}

func checkRPCResponse(t *testing.T, rec *httptest.ResponseRecorder, withSession bool) {
	t.Helper()
	switch rec.Code {
	case http.StatusAccepted:
		if rec.Body.Len() != 0 {
			t.Fatalf("202 with body %q", rec.Body.String())
		}
		return
	case http.StatusNotFound:
		if !withSession {
			t.Fatalf("404 without a session header")
		}
		return
	case http.StatusOK:
	default:
		t.Fatalf("unexpected status %d: %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type %q", ct)
	}
	var resp struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *struct {
			Code    *int   `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	dec := json.NewDecoder(rec.Body)
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if resp.JSONRPC != "2.0" {
		t.Fatalf("jsonrpc = %q", resp.JSONRPC)
	}
	if len(resp.ID) == 0 || !json.Valid(resp.ID) {
		t.Fatalf("missing or invalid id %q", resp.ID)
	}
	if (resp.Error == nil) == (len(resp.Result) == 0) {
		t.Fatalf("need exactly one of result/error: %s", rec.Body.String())
	}
	if resp.Error == nil {
		var res map[string]json.RawMessage
		if err := json.Unmarshal(resp.Result, &res); err != nil {
			t.Fatalf("result is not an object: %s", resp.Result)
		}
		if raw, ok := res["isError"]; ok {
			var b bool
			if json.Unmarshal(raw, &b) != nil || len(res["content"]) == 0 {
				t.Fatalf("malformed tool result: %s", resp.Result)
			}
		}
		return
	}
	if resp.Error.Code == nil || resp.Error.Message == "" {
		t.Fatalf("malformed error: %s", rec.Body.String())
	}
	switch *resp.Error.Code {
	case -32700, -32600, -32601, -32602, -32603:
	default:
		t.Fatalf("unexpected error code %d", *resp.Error.Code)
	}
	if *resp.Error.Code == -32700 && string(resp.ID) != "null" {
		t.Fatalf("parse error must carry id null, got %s", resp.ID)
	}
}
