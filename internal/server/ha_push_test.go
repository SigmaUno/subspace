package server_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/servertest"
)

func mcpPost(t *testing.T, e *servertest.Env, tok, sid string, body map[string]any) *http.Response {
	t.Helper()
	body["jsonrpc"] = "2.0"
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, e.TS.URL+"/mcp", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestHAResourceSubscriptionFollowsTheStream(t *testing.T) {
	c := startCluster(t, 2, time.Minute)
	tok := c.node(0).Register("w1", "worker", "claude-code")

	sid := mcpPost(t, c.nodes[0], tok, "", map[string]any{"id": 1, "method": "initialize"}).Header.Get("Mcp-Session-Id")
	if sid == "" {
		t.Fatal("no session id")
	}
	sub := mcpPost(t, c.nodes[0], tok, sid, map[string]any{"id": 2, "method": "resources/subscribe",
		"params": map[string]any{"uri": "subspace://thread/agent/w1"}})
	if sub.StatusCode != http.StatusOK {
		t.Fatalf("subscribe: %d", sub.StatusCode)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.nodes[1].TS.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", sid)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream: %d", resp.StatusCode)
	}
	updated := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if line := sc.Text(); strings.Contains(line, "notifications/resources/updated") {
				updated <- line
				return
			}
		}
	}()

	if code, out := c.node(0).REST("POST", "/api/v1/threads/agent/w1/messages", map[string]any{"body": "hello"}); code != 201 && code != 200 {
		t.Fatalf("post: %d %v", code, out)
	}
	select {
	case line := <-updated:
		if !strings.Contains(line, "subspace://thread/agent/w1") {
			t.Fatalf("unexpected notification %s", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a subscription made on one instance produced no update on the stream held by another")
	}
}
