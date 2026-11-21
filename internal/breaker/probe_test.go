package breaker_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestProbeOpensBreakerOfFailingProvider(t *testing.T) {
	var badHits, goodHits atomic.Int64
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHits.Add(1)
		http.Error(w, `{"error":{"message":"overloaded"}}`, http.StatusInternalServerError)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodHits.Add(1)
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer good-key" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		var in struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.MaxTokens != 1 {
			http.Error(w, "max_tokens must be 1", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"id":"x","model":"d","choices":[{"message":{"content":"p"},"finish_reason":"length"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer good.Close()

	t.Setenv("PROBE_TEST_ANTHROPIC_KEY", "bad-key")
	t.Setenv("PROBE_TEST_DEEPSEEK_KEY", "good-key")
	t.Setenv("SUBSPACE_PROVIDER_PROBE_INTERVAL", "20ms")
	e, lead, _, brief := setup(t, "1h")

	code, out := e.REST("PUT", "/api/v1/capacity", map[string]any{"global_max_agents": 0, "limits": []map[string]any{
		{"provider": "anthropic", "model": "m", "max_concurrency": 0, "key_env": "PROBE_TEST_ANTHROPIC_KEY", "base_url": bad.URL},
		{"provider": "deepseek", "model": "d", "max_concurrency": 0, "key_env": "PROBE_TEST_DEEPSEEK_KEY", "base_url": good.URL},
		{"provider": "openai", "model": "*", "max_concurrency": 5},
	}})
	if code != 200 {
		t.Fatalf("set capacity: %d %v", code, out)
	}

	byProvider := func() map[string]map[string]any {
		code, body := e.REST("GET", "/api/v1/providers", nil)
		if code != 200 {
			t.Fatalf("providers: %d %v", code, body)
		}
		m := map[string]map[string]any{}
		for _, p := range body["providers"].([]any) {
			p := p.(map[string]any)
			m[p["provider"].(string)] = p
		}
		return m
	}
	deadline := time.Now().Add(10 * time.Second)
	var ps map[string]map[string]any
	for {
		ps = byProvider()
		if ps["anthropic"]["breaker"] == "open" && ps["deepseek"]["checked_at"] != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("breaker did not open: %v (bad hits %d)", ps, badHits.Load())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(ps) != 2 {
		t.Fatalf("want anthropic and deepseek only: %v", ps)
	}
	if a := ps["anthropic"]; a["ok"] != false || a["model"] != "m" || a["error"] != "status 500" {
		t.Fatalf("anthropic: %v", a)
	}
	if d := ps["deepseek"]; d["ok"] != true || d["model"] != "d" || d["breaker"] != "closed" {
		t.Fatalf("deepseek: %v", d)
	}
	if n := badHits.Load(); n < 4 {
		t.Fatalf("breaker opened after %d probes, min samples is 4", n)
	}

	e.MustFail(lead, "request_spawn", map[string]any{"brief_id": brief, "worker_kind": "claude-code", "model": "m", "n": 1}, "capacity")
	if out, isErr := e.Call(lead, "request_spawn", map[string]any{"brief_id": brief, "worker_kind": "opencode", "model": "d", "n": 1}); isErr {
		t.Fatalf("spawn on healthy provider: %v", out)
	}
}

func TestProbeRejectsUnsafeKeyEnv(t *testing.T) {
	t.Setenv("SUBSPACE_PROVIDER_PROBE_INTERVAL", "0")
	e, _, _, _ := setup(t, "1h")
	code, _ := e.REST("PUT", "/api/v1/capacity", map[string]any{"limits": []map[string]any{
		{"provider": "anthropic", "model": "m", "key_env": "SUBSPACE_OWNER_TOKEN"},
	}})
	if code != 400 {
		t.Fatalf("key_env naming a Subspace secret: %d", code)
	}
}
