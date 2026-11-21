package idempotency

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

type agents map[string]*model.Agent

func (a agents) LookupToken(tok string) (*model.Agent, error) {
	if tok == "boom" {
		return nil, errors.New("db down")
	}
	if ag, ok := a[tok]; ok {
		return ag, nil
	}
	return &model.Agent{ID: "a1", Status: model.AgentActive}, nil
}

type failStore struct {
	memStore
	getErr, putErr error
}

func (s *failStore) GetIdempotency(ctx context.Context, agentID, key string, now time.Time) (*Record, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.memStore.GetIdempotency(ctx, agentID, key, now)
}

func (s *failStore) PutIdempotency(ctx context.Context, agentID, key string, r *Record, now time.Time) error {
	if s.putErr != nil {
		return s.putErr
	}
	return s.memStore.PutIdempotency(ctx, agentID, key, r, now)
}

type backend struct {
	calls  int
	status int
	result string
}

func (b *backend) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	b.calls++
	w.Header().Set("Content-Type", "application/json")
	if b.status != 0 {
		w.WriteHeader(b.status)
	}
	res := b.result
	if res == "" {
		res = `{"content":[],"isError":false}`
	}
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + res + "}\n"))
}

func rpcBody(t *testing.T, id any, method, tool, args, key string) []byte {
	t.Helper()
	params := map[string]any{"name": tool, "arguments": json.RawMessage(args)}
	if key != "" {
		params["_meta"] = map[string]any{"idempotencyKey": key}
	}
	msg := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if id != nil {
		msg["id"] = id
	}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func post(h http.Handler, path string, body []byte, bearer string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	if bearer != "" {
		r.Header.Set("Authorization", bearer)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

type rpcReply struct {
	ID     json.RawMessage `json:"id"`
	Result struct {
		IsError           bool `json:"isError"`
		StructuredContent struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"structuredContent"`
	} `json:"result"`
}

func parseReply(t *testing.T, w *httptest.ResponseRecorder) rpcReply {
	t.Helper()
	var r rpcReply
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatalf("reply %q: %v", w.Body.String(), err)
	}
	return r
}

func TestReplayWithinTTL(t *testing.T) {
	st := &memStore{m: map[string]*Record{}}
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	now := t0
	be := &backend{}
	h := Wrap(be, Options{Store: st, Agents: agents{}, Now: func() time.Time { return now }})

	first := post(h, "/mcp", rpcBody(t, 1, "tools/call", "claim_task", `{"task_id":"T-1"}`, "k1"), "Bearer ssp_x")
	if be.calls != 1 || first.Header().Get(Replay) != "" {
		t.Fatalf("first call: calls=%d replay=%q", be.calls, first.Header().Get(Replay))
	}
	rec := st.m["a1\x00k1"]
	if rec == nil || rec.Tool != "claim_task" || !rec.ExpiresAt.Equal(t0.Add(24*time.Hour)) {
		t.Fatalf("stored record %+v", rec)
	}
	if n := len(h.(*handler).locks); n != 0 {
		t.Fatalf("%d key locks left after the call", n)
	}

	now = t0.Add(24*time.Hour - time.Second)
	again := post(h, "/mcp", rpcBody(t, 7, "tools/call", "claim_task", `{"task_id":"T-1"}`, "k1"), "Bearer ssp_x")
	if be.calls != 1 || again.Header().Get(Replay) != "true" || again.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("replay: calls=%d headers=%v", be.calls, again.Header())
	}
	if id := string(parseReply(t, again).ID); id != "7" {
		t.Fatalf("replay id %s, want 7", id)
	}

	now = t0.Add(24 * time.Hour)
	post(h, "/mcp", rpcBody(t, 8, "tools/call", "claim_task", `{"task_id":"T-1"}`, "k1"), "Bearer ssp_x")
	if be.calls != 2 {
		t.Fatalf("expired key replayed: calls=%d", be.calls)
	}
}

func TestKeyLengthLimit(t *testing.T) {
	be := &backend{}
	h := Wrap(be, Options{Store: &memStore{m: map[string]*Record{}}, Agents: agents{}})
	ok := strings.Repeat("k", 255)
	for range 2 {
		post(h, "/mcp", rpcBody(t, 1, "tools/call", "submit", `{}`, ok), "Bearer ssp_x")
	}
	if be.calls != 1 {
		t.Fatalf("255-byte key not honoured: calls=%d", be.calls)
	}
	w := post(h, "/mcp", rpcBody(t, 3, "tools/call", "submit", `{}`, ok+"k"), "Bearer ssp_x")
	r := parseReply(t, w)
	if be.calls != 1 || string(r.ID) != "3" || !r.Result.IsError || r.Result.StructuredContent.Error.Code != "bad_request" {
		t.Fatalf("256-byte key: calls=%d reply=%s", be.calls, w.Body.String())
	}
}

func TestBodyLimit(t *testing.T) {
	sized := func(n int) []byte {
		base := rpcBody(t, 1, "tools/call", "add_note", `{"note":""}`, "k")
		pad := n - len(base)
		return bytes.Replace(base, []byte(`"note":""`), []byte(`"note":"`+strings.Repeat("x", pad)+`"`), 1)
	}
	limit := sized(1 << 20)
	if len(limit) != 1<<20 {
		t.Fatalf("body is %d bytes", len(limit))
	}
	be := &backend{}
	h := Wrap(be, Options{Store: &memStore{m: map[string]*Record{}}, Agents: agents{}})
	post(h, "/mcp", limit, "Bearer ssp_x")
	if w := post(h, "/mcp", limit, "Bearer ssp_x"); be.calls != 1 || w.Header().Get(Replay) != "true" {
		t.Fatalf("1 MiB body not deduplicated: calls=%d", be.calls)
	}

	over := append(sized(1<<20), '\n')
	be2 := &backend{}
	h2 := Wrap(be2, Options{Store: &memStore{m: map[string]*Record{}}, Agents: agents{}})
	post(h2, "/mcp", over, "Bearer ssp_x")
	if w := post(h2, "/mcp", over, "Bearer ssp_x"); be2.calls != 2 || w.Header().Get(Replay) != "" {
		t.Fatalf("oversized body deduplicated: calls=%d", be2.calls)
	}
}

func TestRequestsOutsideTheContractPassThrough(t *testing.T) {
	call := func(t *testing.T) []byte { return rpcBody(t, 1, "tools/call", "claim_task", `{}`, "k") }
	cases := []struct {
		name   string
		method string
		path   string
		body   func(*testing.T) []byte
		bearer string
		opts   func(*Options)
	}{
		{name: "other path", path: "/api/v1/x", body: call},
		{name: "GET", method: http.MethodGet, body: call},
		{name: "not tools/call", body: func(t *testing.T) []byte { return rpcBody(t, 1, "tools/list", "claim_task", `{}`, "k") }},
		{name: "notification without id", body: func(t *testing.T) []byte { return rpcBody(t, nil, "tools/call", "claim_task", `{}`, "k") }},
		{name: "read-only tool", body: func(t *testing.T) []byte { return rpcBody(t, 1, "tools/call", "board", `{}`, "k") }},
		{name: "invalid json", body: func(*testing.T) []byte { return []byte(`{"method":"tools/call"`) }},
		{name: "no key", body: func(t *testing.T) []byte { return rpcBody(t, 1, "tools/call", "claim_task", `{}`, "") }},
		{name: "no authorization", body: call, bearer: "-"},
		{name: "basic auth", body: call, bearer: "Basic abc"},
		{name: "empty bearer", body: call, bearer: "Bearer "},
		{name: "lookup error", body: call, bearer: "Bearer boom"},
		{name: "unknown token", body: call, bearer: "Bearer nobody"},
		{name: "revoked agent", body: call, bearer: "Bearer revoked"},
		{name: "no agent lookup", body: call, opts: func(o *Options) { o.Agents = nil }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			be := &backend{}
			o := Options{Store: &memStore{m: map[string]*Record{}}, Agents: agents{
				"nobody":  nil,
				"revoked": {ID: "a1", Status: model.AgentRevoked},
			}}
			if c.opts != nil {
				c.opts(&o)
			}
			h := Wrap(be, o)
			method, path, bearer := c.method, c.path, c.bearer
			if method == "" {
				method = http.MethodPost
			}
			if path == "" {
				path = "/mcp"
			}
			switch bearer {
			case "":
				bearer = "Bearer ssp_x"
			case "-":
				bearer = ""
			}
			for range 2 {
				r := httptest.NewRequest(method, path, bytes.NewReader(c.body(t)))
				if bearer != "" {
					r.Header.Set("Authorization", bearer)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Header().Get(Replay) != "" {
					t.Fatal("request was replayed")
				}
			}
			if be.calls != 2 {
				t.Fatalf("calls=%d, want 2", be.calls)
			}
		})
	}
}

func TestKeyReuseForDifferentCallConflicts(t *testing.T) {
	be := &backend{}
	h := Wrap(be, Options{Store: &memStore{m: map[string]*Record{}}, Agents: agents{}})
	post(h, "/mcp", rpcBody(t, 1, "tools/call", "claim_task", `{"task_id":"T-1"}`, "k"), "Bearer ssp_x")
	for _, c := range []struct{ tool, args string }{{"claim_task", `{"task_id":"T-2"}`}, {"release_task", `{"task_id":"T-1"}`}} {
		w := post(h, "/mcp", rpcBody(t, 5, "tools/call", c.tool, c.args, "k"), "Bearer ssp_x")
		r := parseReply(t, w)
		if string(r.ID) != "5" || r.Result.StructuredContent.Error.Code != "idempotency_conflict" {
			t.Fatalf("%s %s: %s", c.tool, c.args, w.Body.String())
		}
	}
	if be.calls != 1 {
		t.Fatalf("conflicting calls reached the backend: calls=%d", be.calls)
	}
	post(h, "/mcp", rpcBody(t, 1, "tools/call", "submit", `null`, "k2"), "Bearer ssp_x")
	if w := post(h, "/mcp", rpcBody(t, 1, "tools/call", "submit", `{}`, "k2"), "Bearer ssp_x"); w.Header().Get(Replay) != "true" {
		t.Fatalf("null and {} arguments differ: %s", w.Body.String())
	}
}

func TestStoreFailures(t *testing.T) {
	be := &backend{}
	st := &failStore{memStore: memStore{m: map[string]*Record{}}, getErr: errors.New("read failed")}
	h := Wrap(be, Options{Store: st, Agents: agents{}})
	w := post(h, "/mcp", rpcBody(t, 4, "tools/call", "submit", `{}`, "k"), "Bearer ssp_x")
	r := parseReply(t, w)
	if be.calls != 0 || string(r.ID) != "4" || r.Result.StructuredContent.Error.Code != "internal" {
		t.Fatalf("lookup failure: calls=%d reply=%s", be.calls, w.Body.String())
	}

	st.getErr, st.putErr = nil, errors.New("write failed")
	for range 2 {
		if w := post(h, "/mcp", rpcBody(t, 4, "tools/call", "submit", `{}`, "k"), "Bearer ssp_x"); w.Code != http.StatusOK {
			t.Fatalf("status %d", w.Code)
		}
	}
	if be.calls != 2 {
		t.Fatalf("unstored result was replayed: calls=%d", be.calls)
	}
}

func TestOnlyFinalResultsAreStored(t *testing.T) {
	toolErr := func(code string) string {
		return `{"isError":true,"structuredContent":{"error":{"code":"` + code + `","message":"x"}}}`
	}
	cases := []struct {
		name   string
		status int
		result string
		stored bool
	}{
		{name: "success", result: `{"content":[],"isError":false}`, stored: true},
		{name: "conflict tool error", result: toolErr("conflict"), stored: true},
		{name: "internal", result: toolErr("internal")},
		{name: "agent_paused", result: toolErr("agent_paused")},
		{name: "rate_limited", result: toolErr("rate_limited")},
		{name: "http 500", status: http.StatusInternalServerError, result: `{"content":[],"isError":false}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			be := &backend{status: c.status, result: c.result}
			h := Wrap(be, Options{Store: &memStore{m: map[string]*Record{}}, Agents: agents{}})
			first := post(h, "/mcp", rpcBody(t, 1, "tools/call", "update_status", `{}`, "k"), "Bearer ssp_x")
			if want := max(c.status, http.StatusOK); first.Code != want {
				t.Fatalf("status %d, want %d", first.Code, want)
			}
			post(h, "/mcp", rpcBody(t, 1, "tools/call", "update_status", `{}`, "k"), "Bearer ssp_x")
			if want := map[bool]int{true: 1, false: 2}[c.stored]; be.calls != want {
				t.Fatalf("calls=%d, want %d", be.calls, want)
			}
		})
	}
}
