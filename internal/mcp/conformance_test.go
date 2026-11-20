package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/model"
)

const ownerTok = "owner-token-for-conformance-tests"

type fakeLookup map[string]*model.Agent

func (f fakeLookup) LookupToken(tok string) (*model.Agent, error) { return f[tok], nil }

func confServer() (*Server, fakeLookup) {
	l := fakeLookup{
		"tok-leader":   {ID: "rex", Role: model.RoleLeader, Status: model.AgentActive},
		"tok-worker":   {ID: "claude-1", Role: model.RoleWorker, Status: model.AgentActive},
		"tok-tester":   {ID: "tess", Role: model.RoleTester, Status: model.AgentActive},
		"tok-reviewer": {ID: "rev", Role: model.RoleReviewer, Status: model.AgentActive},
		"tok-revoked":  {ID: "gone", Role: model.RoleWorker, Status: model.AgentRevoked},
	}
	s := NewServer(l, DefaultRegistry())
	s.OwnerToken = ownerTok
	return s, l
}

func post(t *testing.T, s *Server, tok, body string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

type rpcOut struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcErr         `json:"error"`
}

func decodeRPC(t *testing.T, w *httptest.ResponseRecorder) rpcOut {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q", ct)
	}
	var o rpcOut
	if err := json.Unmarshal(w.Body.Bytes(), &o); err != nil {
		t.Fatalf("decode: %v: %s", err, w.Body.String())
	}
	if o.JSONRPC != "2.0" {
		t.Fatalf("jsonrpc = %q", o.JSONRPC)
	}
	return o
}

func listTools(t *testing.T, s *Server, tok string) []string {
	t.Helper()
	o := decodeRPC(t, post(t, s, tok, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if o.Error != nil {
		t.Fatalf("tools/list error: %+v", o.Error)
	}
	var res struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(o.Result, &res); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tl := range res.Tools {
		if tl.InputSchema["type"] != "object" {
			t.Errorf("tool %s: inputSchema.type = %v", tl.Name, tl.InputSchema["type"])
		}
		names = append(names, tl.Name)
	}
	return names
}

func TestConformanceInitialize(t *testing.T) {
	s, _ := confServer()
	w := post(t, s, "tok-worker", `{"jsonrpc":"2.0","id":7,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	o := decodeRPC(t, w)
	if o.Error != nil {
		t.Fatalf("error: %+v", o.Error)
	}
	if string(o.ID) != "7" {
		t.Errorf("id = %s", o.ID)
	}
	var res struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Capabilities    map[string]any `json:"capabilities"`
		ServerInfo      map[string]any `json:"serverInfo"`
	}
	if err := json.Unmarshal(o.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.ProtocolVersion != "2025-06-18" {
		t.Errorf("protocolVersion = %q", res.ProtocolVersion)
	}
	if _, ok := res.Capabilities["tools"]; !ok {
		t.Errorf("capabilities lack tools: %v", res.Capabilities)
	}
	if res.ServerInfo["name"] == "" || res.ServerInfo["name"] == nil {
		t.Errorf("serverInfo = %v", res.ServerInfo)
	}
	sid := w.Header().Get("Mcp-Session-Id")
	if sid == "" {
		t.Fatal("no Mcp-Session-Id")
	}

	if w := post(t, s, "tok-worker", `{"jsonrpc":"2.0","id":8,"method":"ping"}`, "Mcp-Session-Id", sid); w.Code != http.StatusOK {
		t.Errorf("ping with session = %d", w.Code)
	}

	if w := post(t, s, "tok-tester", `{"jsonrpc":"2.0","id":9,"method":"ping"}`, "Mcp-Session-Id", sid); w.Code != http.StatusNotFound {
		t.Errorf("foreign session = %d, want 404", w.Code)
	}

	if w := post(t, s, "tok-worker", `{"jsonrpc":"2.0","id":9,"method":"ping"}`, "Mcp-Session-Id", "nope"); w.Code != http.StatusNotFound {
		t.Errorf("unknown session = %d, want 404", w.Code)
	}

	if w := post(t, s, "tok-worker", `{"jsonrpc":"2.0","id":9,"method":"ping"}`); w.Code != http.StatusOK {
		t.Errorf("ping without session = %d", w.Code)
	}
}

func TestConformanceDeleteSession(t *testing.T) {
	s, _ := confServer()
	w := post(t, s, "tok-worker", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	sid := w.Header().Get("Mcp-Session-Id")
	del := func(tok, sid string) int {
		r := httptest.NewRequest(http.MethodDelete, "/mcp", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		if sid != "" {
			r.Header.Set("Mcp-Session-Id", sid)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, r)
		return rec.Code
	}
	if c := del("tok-worker", ""); c != http.StatusNotFound {
		t.Errorf("delete without session = %d", c)
	}
	if c := del("tok-tester", sid); c != http.StatusNotFound {
		t.Errorf("delete foreign session = %d", c)
	}
	if c := del("tok-worker", sid); c != http.StatusNoContent {
		t.Errorf("delete = %d", c)
	}
	if w := post(t, s, "tok-worker", `{"jsonrpc":"2.0","id":2,"method":"ping"}`, "Mcp-Session-Id", sid); w.Code != http.StatusNotFound {
		t.Errorf("ping after delete = %d, want 404", w.Code)
	}
}

func TestConformancePing(t *testing.T) {
	s, _ := confServer()
	o := decodeRPC(t, post(t, s, "tok-leader", `{"jsonrpc":"2.0","id":"abc","method":"ping"}`))
	if o.Error != nil || string(o.Result) != "{}" {
		t.Errorf("ping = result %s err %+v", o.Result, o.Error)
	}
	if string(o.ID) != `"abc"` {
		t.Errorf("string id not echoed: %s", o.ID)
	}
}

func toolMatrix() map[string][]string {
	common := []string{"get_instructions", "whoami", "list_my_tasks", "get_task", "heartbeat", "update_status", "add_note", "ask_owner", "post_message", "read_thread"}
	with := func(extra ...string) []string {
		out := append(append([]string{}, common...), extra...)
		sort.Strings(out)
		return out
	}
	return map[string][]string{
		model.RoleLeader:   with("get_brief", "list_briefs", "propose_plan", "create_task", "update_task", "assign", "reassign", "board", "list_agents", "capacity", "request_spawn"),
		model.RoleWorker:   with("claim_task", "release_task", "submit"),
		model.RoleTester:   with("claim_task", "release_task", "submit", "report_tests"),
		model.RoleReviewer: with("list_reviews", "review"),
	}
}

func TestConformanceToolsListMatchesDocs(t *testing.T) {
	s, _ := confServer()
	for role, tok := range map[string]string{
		model.RoleLeader: "tok-leader", model.RoleWorker: "tok-worker",
		model.RoleTester: "tok-tester", model.RoleReviewer: "tok-reviewer",
	} {
		got := listTools(t, s, tok)
		if !sort.StringsAreSorted(got) {
			t.Errorf("%s: tools not sorted: %v", role, got)
		}
		want := toolMatrix()[role]
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s tools/list mismatch\n got: %v\nwant: %v", role, got, want)
		}
	}
}

func TestConformanceToolsCallRecheckedPerRole(t *testing.T) {
	s, _ := confServer()
	owned := toolMatrix()
	all := map[string]bool{}
	for _, names := range owned {
		for _, n := range names {
			all[n] = true
		}
	}

	for role, tok := range map[string]string{
		model.RoleLeader: "tok-leader", model.RoleWorker: "tok-worker",
		model.RoleTester: "tok-tester", model.RoleReviewer: "tok-reviewer",
	} {
		mine := map[string]bool{}
		for _, n := range owned[role] {
			mine[n] = true
		}
		var forbidden []string
		for n := range all {
			if !mine[n] {
				forbidden = append(forbidden, n)
			}
		}
		forbidden = append(forbidden, "approve_plan", "reject_plan", "answer_question", "set_budget", "register_agent",
			"register_agents_bulk", "revoke_agent", "pause_all", "resume_all", "edit_charter", "create_brief", "set_capacity", "mark_done")
		for _, n := range forbidden {
			body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + n + `","arguments":{}}}`
			o := decodeRPC(t, post(t, s, tok, body))
			if o.Error == nil || o.Error.Code != -32601 || !strings.Contains(o.Error.Message, "not available for role") {
				t.Errorf("%s calling %s: error = %+v result=%s", role, n, o.Error, o.Result)
			}
		}
	}
}

func TestConformanceUnknownToolAndMethod(t *testing.T) {
	s, _ := confServer()
	o := decodeRPC(t, post(t, s, "tok-worker", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"does_not_exist","arguments":{}}}`))
	if o.Error == nil || o.Error.Code != -32601 {
		t.Errorf("unknown tool: %+v", o.Error)
	}
	o = decodeRPC(t, post(t, s, "tok-worker", `{"jsonrpc":"2.0","id":2,"method":"resources/list"}`))
	if o.Error == nil || o.Error.Code != -32601 {
		t.Errorf("unknown method: %+v", o.Error)
	}
	for _, params := range []string{`{}`, `{"name":""}`, `[1]`, `"x"`} {
		o = decodeRPC(t, post(t, s, "tok-worker", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":`+params+`}`))
		if o.Error == nil || o.Error.Code != -32602 {
			t.Errorf("params %s: %+v", params, o.Error)
		}
	}
}

func TestConformanceNotificationsAccepted(t *testing.T) {
	s, _ := confServer()
	for _, body := range []string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`,
		`{"jsonrpc":"2.0","id":5,"result":{}}`,
	} {
		w := post(t, s, "tok-worker", body)
		if w.Code != http.StatusAccepted {
			t.Errorf("%s -> %d, want 202", body, w.Code)
		}
		if w.Body.Len() != 0 {
			t.Errorf("%s -> non-empty body %q", body, w.Body.String())
		}
	}
}

func TestConformanceMethods(t *testing.T) {
	s, _ := confServer()
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodHead} {
		r := httptest.NewRequest(m, "/mcp", nil)
		r.Header.Set("Authorization", "Bearer tok-worker")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want 405", m, w.Code)
		}
		if m == http.MethodGet && w.Header().Get("Allow") == "" {
			t.Error("405 without Allow header")
		}
	}
}

func TestConformanceParseErrors(t *testing.T) {
	s, _ := confServer()
	for _, body := range []string{``, `{`, `not json`, `[1,2`, `{"jsonrpc":"1.0","id":1,"method":"ping"}`, `{"id":1,"method":"ping"}`} {
		o := decodeRPC(t, post(t, s, "tok-worker", body))
		if o.Error == nil || o.Error.Code != -32700 {
			t.Errorf("body %q: error = %+v", body, o.Error)
		}
		if string(o.ID) != "null" {
			t.Errorf("body %q: id = %s, want null", body, o.ID)
		}
	}
}

func TestConformanceAuth(t *testing.T) {
	s, _ := confServer()
	ping := `{"jsonrpc":"2.0","id":1,"method":"ping"}`

	w := post(t, s, "", ping)
	if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("no token: %d %q", w.Code, w.Header().Get("WWW-Authenticate"))
	}
	w = post(t, s, "wrong", ping)
	if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("bad token: %d %q", w.Code, w.Header().Get("WWW-Authenticate"))
	}
	w = post(t, s, "tok-revoked", ping)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("revoked token: %d", w.Code)
	}
	if w = post(t, s, ownerTok, ping); w.Code != http.StatusForbidden {
		t.Errorf("owner token: %d, want 403", w.Code)
	}

	for _, h := range []string{"Basic dXNlcjpwdw==", "Bearer ", "Bearer", "tok-worker"} {
		r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(ping))
		r.Header.Set("Authorization", h)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, r)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: %d, want 401", h, rec.Code)
		}
	}

	if w = post(t, s, "", `garbage`); w.Code != http.StatusUnauthorized {
		t.Errorf("garbage without token: %d", w.Code)
	}
}

func TestConformanceOversizeBody(t *testing.T) {
	s, _ := confServer()
	big := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"pad":"` + strings.Repeat("a", maxBody+1) + `"}}`
	w := post(t, s, "tok-worker", big)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize = %d, want 413", w.Code)
	}

	pad := strings.Repeat("a", maxBody-200)
	ok := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"pad":"` + pad + `"}}`
	if len(ok) >= maxBody {
		t.Fatalf("test body too large: %d", len(ok))
	}
	if w := post(t, s, "tok-worker", ok); w.Code != http.StatusOK {
		t.Errorf("near-limit = %d, want 200", w.Code)
	}
}

func TestConformanceNoBatching(t *testing.T) {
	s, _ := confServer()
	batch := `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`
	o := decodeRPC(t, post(t, s, "tok-worker", batch))
	if o.Error == nil || o.Error.Code != -32700 {
		t.Errorf("batch should be rejected with -32700, got %+v", o.Error)
	}
}
