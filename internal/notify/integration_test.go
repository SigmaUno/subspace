package notify_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/server"
)

const ownerTok = "owner-token-for-tests-0123456789"

func TestNewQuestionProducesExactlyOneNotification(t *testing.T) {
	var mu sync.Mutex
	var got []notify.Notification
	sink := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var n notify.Notification
		_ = json.NewDecoder(r.Body).Decode(&n)
		mu.Lock()
		got = append(got, n)
		mu.Unlock()
	}))
	defer sink.Close()
	t.Setenv(notify.EnvWebhookURL, sink.URL)
	t.Setenv(notify.EnvAllowPrivate, "true")

	ctx, cancel := context.WithCancel(context.Background())
	s, err := server.New(ctx, server.Config{
		DBPath: filepath.Join(t.TempDir(), "t.db"), OwnerToken: ownerTok, PublicURL: "http://sub.test",
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	go s.Board.Run(ctx)
	api := httptest.NewServer(s.Handler())
	defer func() { api.Close(); cancel(); _ = s.Close() }()

	do := func(method, path, token string, body any) map[string]any {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, api.URL+path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}

	reg := do("POST", "/api/v1/agents", ownerTok, map[string]any{"id": "w1", "role": "worker", "kind": "claude-code", "model": "m"})
	agentTok := reg["connect"].(map[string]any)["token"].(string)
	res := do("POST", "/mcp", agentTok, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "ask_owner", "arguments": map[string]any{"question": "which db?", "options": []string{"a", "b"}}},
	})
	if r, _ := res["result"].(map[string]any); r == nil || r["isError"] == true {
		t.Fatalf("ask_owner failed: %v", res)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no notification delivered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(2500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].Kind != "question" || got[0].ID == "" {
		t.Fatalf("got %+v", got)
	}

	status := do("GET", "/api/v1/notify/status", ownerTok, nil)
	if status["enabled"] != true || status["sent"] != float64(1) {
		t.Fatalf("status %v", status)
	}
}
