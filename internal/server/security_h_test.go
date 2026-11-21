package server_test

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

func peerDo(t *testing.T, peer string, req *http.Request) (int, []byte) {
	t.Helper()
	dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(peer)}}
	cl := &http.Client{Transport: &http.Transport{DialContext: dialer.DialContext}}
	defer cl.CloseIdleConnections()
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestAgentAllowlistCoversIdempotentReplay(t *testing.T) {
	needLoopbackPeer(t, "127.0.0.2")
	t.Setenv("SUBSPACE_AGENT_ALLOW_CIDRS", "127.0.0.1")
	e := servertest.New(t)
	tok := registerAgentToken(t, e, "w1")
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"add_note","arguments":{"body":"secret plan"},"_meta":{"idempotencyKey":"k1"}}}`)
	send := func(peer string) (int, []byte) {
		req, _ := http.NewRequest(http.MethodPost, e.TS.URL+"/mcp", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		return peerDo(t, peer, req)
	}
	if code, out := send("127.0.0.1"); code != http.StatusOK {
		t.Fatalf("allowed peer: %d %s", code, out)
	}
	if code, out := send("127.0.0.2"); code != http.StatusForbidden {
		t.Fatalf("replay from unlisted peer: %d %s", code, out)
	}
}

func TestOwnerAllowlistCoversSpawnListing(t *testing.T) {
	needLoopbackPeer(t, "127.0.0.2")
	t.Setenv("SUBSPACE_OWNER_ALLOW_CIDRS", "127.0.0.1")
	e := servertest.New(t)
	req, _ := http.NewRequest(http.MethodGet, e.TS.URL+"/api/v1/spawns", nil)
	req.Header.Set("Authorization", "Bearer "+servertest.OwnerToken)
	if code, out := peerDo(t, "127.0.0.2", req); code != http.StatusForbidden {
		t.Fatalf("spawn listing from unlisted peer: %d %s", code, out)
	}
}

func TestBotTokensHonourExpiresIn(t *testing.T) {
	e := servertest.New(t)
	expiry := func(label string, code int, out map[string]any, key string) {
		t.Helper()
		if code/100 != 2 {
			t.Fatalf("%s: %d %v", label, code, out)
		}
		if out[key].(map[string]any)["token_expires_at"] == nil {
			t.Fatalf("%s: token without expiry: %v", label, out)
		}
	}
	code, out := e.REST("POST", "/api/v1/bots", map[string]any{"name": "b1", "kind": "codex", "model": "m", "expires_in": "1h"})
	expiry("create", code, out, "bot")
	code, out = e.REST("POST", "/api/v1/bots/bulk", map[string]any{"prefix": "bb", "count": 1, "kind": "codex", "model": "m", "expires_in": "1h"})
	if code/100 != 2 {
		t.Fatalf("bulk: %d %v", code, out)
	}
	expiry("bulk", code, out["bots"].([]any)[0].(map[string]any), "bot")
	if code, out := e.REST("POST", "/api/v1/bots", map[string]any{"name": "b2", "kind": "codex", "model": "m"}); code != 201 {
		t.Fatalf("create b2: %d %v", code, out)
	}
	code, out = e.REST("POST", "/api/v1/bots/b2/rotate-token", map[string]any{"expires_in": "1h"})
	expiry("rotate", code, out, "bot")
	if code, out := e.REST("POST", "/api/v1/bots", map[string]any{"name": "b3", "kind": "codex", "model": "m", "expires_in": "-1h"}); code != 400 {
		t.Fatalf("negative expires_in: %d %v", code, out)
	}
}

func TestRequestSpawnRejectsUnsafeModel(t *testing.T) {
	e := servertest.New(t)
	lead := e.Register("rex", "leader", "other")
	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B"})
	bid := br["id"].(string)
	for _, m := range []string{"--dangerously-skip-permissions", "m; rm -rf /", "m\nx", strings.Repeat("m", 300)} {
		e.MustFail(lead, "request_spawn", map[string]any{"brief_id": bid, "worker_kind": "claude-code", "model": m, "n": 1}, "bad_request")
	}
	e.Must(lead, "request_spawn", map[string]any{"brief_id": bid, "worker_kind": "claude-code", "model": "anthropic/claude-sonnet-5.5@2026", "n": 1})
}

type logSink struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logSink) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logSink) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestSpendGuardIgnoresUnauthenticatedCostReports(t *testing.T) {
	logs := &logSink{}
	e := servertest.Start(t, server.Config{PublicURL: "http://sub.test", Lease: time.Minute,
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Log: slog.New(slog.NewTextHandler(logs, nil))})
	wk := e.Register("w1", "worker", "claude-code")
	e.RPC("not-a-token", "tools/call", map[string]any{"name": "update_status", "arguments": map[string]any{"task_id": "T-anon", "status": "in_progress"}})
	e.RPC(wk, "tools/call", map[string]any{"name": "update_status", "arguments": map[string]any{"task_id": "T-agent", "status": "in_progress"}})
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), "T-agent") {
		if time.Now().After(deadline) {
			t.Fatalf("authenticated cost report never checked: %s", logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Contains(logs.String(), "T-anon") {
		t.Fatalf("unauthenticated request reached the spend guard: %s", logs.String())
	}
}
