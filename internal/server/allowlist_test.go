package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/servertest"
)

func needLoopbackPeer(t *testing.T, ip string) {
	t.Helper()
	l, err := net.Listen("tcp", ip+":0")
	if err != nil {
		t.Skipf("cannot bind %s: %v", ip, err)
	}
	_ = l.Close()
}

func allowRequest(t *testing.T, base, peer, path, tok, xff string) (int, map[string]any) {
	t.Helper()
	method, body := http.MethodGet, io.Reader(nil)
	if path == "/mcp" {
		method = http.MethodPost
		body = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	}
	req, err := http.NewRequest(method, base+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(peer)}}
	cl := &http.Client{Transport: &http.Transport{DialContext: dialer.DialContext}}
	defer cl.CloseIdleConnections()
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func registerAgentToken(t *testing.T, e *servertest.Env, id string) string {
	t.Helper()
	reg, err := e.Srv.Board.RegisterAgent(context.Background(), board.AgentInput{ID: id, Role: model.RoleWorker, Kind: model.KindClaudeCode, Model: "m", Provider: "anthropic"})
	if err != nil {
		t.Fatal(err)
	}
	return reg.Connect.Token
}

func TestAllowlistMatrix(t *testing.T) {
	needLoopbackPeer(t, "127.0.0.2")
	cases := []struct {
		name, owner, agent, trusted, peer, xff, path string
		want                                         int
	}{
		{name: "unset allows any peer", peer: "127.0.0.2", path: "/api/v1/state", want: http.StatusOK},
		{name: "cidr contains peer", owner: "127.0.0.0/8", peer: "127.0.0.2", path: "/api/v1/state", want: http.StatusOK},
		{name: "bare address matches", owner: "127.0.0.2", peer: "127.0.0.2", path: "/api/v1/state", want: http.StatusOK},
		{name: "peer outside list", owner: "10.0.0.0/8", peer: "127.0.0.1", path: "/api/v1/state", want: http.StatusForbidden},
		{name: "spoofed XFF ignored without trusted proxy", owner: "10.0.0.0/8", peer: "127.0.0.1", xff: "10.1.2.3", path: "/api/v1/state", want: http.StatusForbidden},
		{name: "trusted proxy honours XFF", owner: "10.0.0.0/8", trusted: "127.0.0.1", peer: "127.0.0.1", xff: "10.1.2.3", path: "/api/v1/state", want: http.StatusOK},
		{name: "rightmost untrusted hop wins over spoofed left", owner: "10.0.0.0/8", trusted: "127.0.0.1", peer: "127.0.0.1", xff: "10.1.2.3, 203.0.113.7", path: "/api/v1/state", want: http.StatusForbidden},
		{name: "spoofed leftmost hop ignored", owner: "10.0.0.0/8", trusted: "127.0.0.1", peer: "127.0.0.1", xff: "203.0.113.7, 10.1.2.3", path: "/api/v1/state", want: http.StatusOK},
		{name: "trusted hops skipped right to left", owner: "10.0.0.0/8", trusted: "127.0.0.1", peer: "127.0.0.1", xff: "10.1.2.3, 127.0.0.1", path: "/api/v1/state", want: http.StatusOK},
		{name: "untrusted peer ignores XFF", owner: "10.0.0.0/8", trusted: "127.0.0.2", peer: "127.0.0.1", xff: "10.1.2.3", path: "/api/v1/state", want: http.StatusForbidden},
		{name: "malformed forwarded hop fails closed", owner: "127.0.0.0/8", trusted: "127.0.0.1", peer: "127.0.0.1", xff: "garbage", path: "/api/v1/state", want: http.StatusForbidden},
		{name: "malformed trusted list trusts nobody", owner: "10.0.0.0/8", trusted: "not-an-ip", peer: "127.0.0.1", xff: "10.1.2.3", path: "/api/v1/state", want: http.StatusForbidden},
		{name: "malformed allowlist denies all", owner: "10.0.0.0/8,bogus", peer: "127.0.0.1", path: "/api/v1/state", want: http.StatusForbidden},
		{name: "healthz always open", owner: "10.0.0.0/8", agent: "10.0.0.0/8", peer: "127.0.0.1", path: "/healthz", want: http.StatusOK},
		{name: "owner list does not restrict mcp", owner: "10.0.0.0/8", peer: "127.0.0.1", path: "/mcp", want: http.StatusOK},
		{name: "agent list restricts mcp", agent: "10.0.0.0/8", peer: "127.0.0.1", path: "/mcp", want: http.StatusForbidden},
		{name: "agent list allows listed peer", agent: "127.0.0.0/8", peer: "127.0.0.2", path: "/mcp", want: http.StatusOK},
		{name: "agent list ignores owner REST", agent: "127.0.0.0/8", peer: "127.0.0.2", path: "/api/v1/state", want: http.StatusOK},
		{name: "trusted proxy honours XFF for mcp", agent: "10.0.0.0/8", trusted: "127.0.0.1", peer: "127.0.0.1", xff: "10.1.2.3", path: "/mcp", want: http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("SUBSPACE_OWNER_ALLOW_CIDRS", c.owner)
			t.Setenv("SUBSPACE_AGENT_ALLOW_CIDRS", c.agent)
			t.Setenv("SUBSPACE_TRUSTED_PROXIES", c.trusted)
			e := servertest.New(t)
			tok := ""
			switch c.path {
			case "/mcp":
				tok = registerAgentToken(t, e, "w1")
			case "/healthz":
			default:
				tok = servertest.OwnerToken
			}
			code, _ := allowRequest(t, e.TS.URL, c.peer, c.path, tok, c.xff)
			if code != c.want {
				t.Fatalf("%s from %s (xff %q): status %d, want %d", c.path, c.peer, c.xff, code, c.want)
			}
		})
	}
}

func TestAllowlistDenialIsJSON(t *testing.T) {
	needLoopbackPeer(t, "127.0.0.2")
	t.Setenv("SUBSPACE_OWNER_ALLOW_CIDRS", "127.0.0.1")
	e := servertest.New(t)
	code, out := allowRequest(t, e.TS.URL, "127.0.0.2", "/api/v1/state", servertest.OwnerToken, "")
	if code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", code)
	}
	errObj, _ := out["error"].(map[string]any)
	if errObj["code"] != "forbidden" {
		t.Fatalf("error = %v, want code forbidden", out)
	}
	if code, _ := allowRequest(t, e.TS.URL, "127.0.0.1", "/api/v1/state", servertest.OwnerToken, ""); code != http.StatusOK {
		t.Fatalf("listed peer status %d", code)
	}
}
