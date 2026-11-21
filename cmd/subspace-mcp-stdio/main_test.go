package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const testToken = "ssp_test-token-should-never-be-logged"

type seenReq struct {
	Auth    string
	Session string
	Method  string
	Path    string
	Accept  string
}

type fakeMCP struct {
	mu   sync.Mutex
	seen []seenReq
}

func (f *fakeMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var m struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	_ = json.Unmarshal(body, &m)
	f.mu.Lock()
	f.seen = append(f.seen, seenReq{
		Auth:    r.Header.Get("Authorization"),
		Session: r.Header.Get("Mcp-Session-Id"),
		Method:  m.Method,
		Path:    r.URL.Path,
		Accept:  r.Header.Get("Accept"),
	})
	f.mu.Unlock()

	if r.Header.Get("Authorization") != "Bearer "+testToken {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch m.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "sess-1")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(m.ID) + `,"result":{"protocolVersion":"2025-06-18"}}`))
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"jsonrpc\":\"2.0\",\n \"id\":" + string(m.ID) + ",\"result\":{\"tools\":[]}}\n"))
	case "tools/call":
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(": ping\n\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n" +
			"event: message\r\ndata: {\"jsonrpc\":\"2.0\",\"id\":" + string(m.ID) + ",\"result\":{}}\r\n\r\n"))
	default:
		http.Error(w, "boom", http.StatusInternalServerError)
	}
}

func runBridge(t *testing.T, url, input string) (stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	env := map[string]string{"SUBSPACE_URL": url, "SUBSPACE_TOKEN": testToken}
	err := run(context.Background(), strings.NewReader(input), &out, &errb, func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return out.String(), errb.String()
}

func TestBridgeFlow(t *testing.T) {
	f := &fakeMCP{}
	srv := httptest.NewServer(f)
	defer srv.Close()

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		``,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"whoami"}}`,
	}, "\n") + "\n"
	stdout, stderr := runBridge(t, srv.URL+"/", input)

	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	want := []string{
		`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`,
		`{"jsonrpc":"2.0","method":"notifications/progress"}`,
		`{"jsonrpc":"2.0","id":3,"result":{}}`,
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(want), stdout)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %s, want %s", i, lines[i], want[i])
		}
	}

	if len(f.seen) != 4 {
		t.Fatalf("server saw %d requests, want 4", len(f.seen))
	}
	for i, s := range f.seen {
		if s.Path != "/mcp" || s.Auth != "Bearer "+testToken {
			t.Errorf("req %d: path=%q auth ok=%v", i, s.Path, s.Auth == "Bearer "+testToken)
		}
		if !strings.Contains(s.Accept, "application/json") || !strings.Contains(s.Accept, "text/event-stream") {
			t.Errorf("req %d: accept = %q", i, s.Accept)
		}
		wantSess := "sess-1"
		if i == 0 {
			wantSess = ""
		}
		if s.Session != wantSess {
			t.Errorf("req %d (%s): session = %q, want %q", i, s.Method, s.Session, wantSess)
		}
	}
	if strings.Contains(stdout, testToken) || strings.Contains(stderr, testToken) {
		t.Error("token leaked to output")
	}
}

func TestBridgeNotificationWritesNothing(t *testing.T) {
	srv := httptest.NewServer(&fakeMCP{})
	defer srv.Close()
	stdout, _ := runBridge(t, srv.URL, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
}

func TestBridgeHTTPErrorBecomesJSONRPCError(t *testing.T) {
	srv := httptest.NewServer(&fakeMCP{})
	defer srv.Close()
	stdout, stderr := runBridge(t, srv.URL, `{"jsonrpc":"2.0","id":"abc","method":"nope"}`+"\n"+`{"jsonrpc":"2.0","method":"nope"}`+"\n")

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 1 {
		t.Fatalf("want exactly one error response, got %q", stdout)
	}
	var resp struct {
		ID    string `json:"id"`
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID != "abc" || resp.Error.Code != -32000 || !strings.Contains(resp.Error.Message, "500") {
		t.Errorf("unexpected response: %s", lines[0])
	}
	if !strings.Contains(stderr, "request failed") {
		t.Errorf("stderr should log the failure: %q", stderr)
	}
}

func TestBridgeWrongTokenNotLogged(t *testing.T) {
	srv := httptest.NewServer(&fakeMCP{})
	defer srv.Close()
	var out, errb bytes.Buffer
	env := map[string]string{"SUBSPACE_URL": srv.URL, "SUBSPACE_TOKEN": "ssp_wrong-secret-token"}
	err := run(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n"), &out, &errb,
		func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "401") {
		t.Errorf("want 401 error response, got %q", out.String())
	}
	if strings.Contains(out.String()+errb.String(), "ssp_wrong-secret-token") {
		t.Error("token leaked")
	}
}

func TestBridgeUnreachableServer(t *testing.T) {
	srv := httptest.NewServer(&fakeMCP{})
	url := srv.URL
	srv.Close()
	stdout, stderr := runBridge(t, url, `{"jsonrpc":"2.0","id":7,"method":"tools/list"}`+"\n")
	if !strings.Contains(stdout, `"id":7`) || !strings.Contains(stdout, `"error"`) {
		t.Errorf("want JSON-RPC error for id 7, got %q", stdout)
	}
	if strings.Contains(stdout+stderr, testToken) {
		t.Error("token leaked")
	}
}

func TestMissingEnv(t *testing.T) {
	var out, errb bytes.Buffer
	err := run(context.Background(), strings.NewReader(""), &out, &errb, func(string) string { return "" })
	if err == nil {
		t.Fatal("expected error when env is missing")
	}
}

func TestLastLineWithoutNewline(t *testing.T) {
	srv := httptest.NewServer(&fakeMCP{})
	defer srv.Close()
	stdout, _ := runBridge(t, srv.URL, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if !strings.Contains(stdout, `"id":1`) {
		t.Errorf("stdout = %q", stdout)
	}
}
