package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/llm"
)

func newProvider(t *testing.T, provider, url string, slept *[]time.Duration) llm.Provider {
	t.Helper()
	p, err := llm.New(llm.Config{
		Provider: provider, Model: "m-1", BaseURL: url, APIKey: "k-test", Retries: 3, Timeout: 200 * time.Millisecond,
		Sleep: func(_ context.Context, d time.Duration) error { *slept = append(*slept, d); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAnthropicMessages(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "k-test" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("bad request %s %v", r.URL.Path, r.Header)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"id":"msg_1","model":"m-1","stop_reason":"end_turn","content":[{"type":"text","text":"{\"a\":"},{"type":"text","text":"1}"}],
			"usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":7,"cache_creation_input_tokens":3}}`))
	}))
	defer srv.Close()
	var slept []time.Duration
	p := newProvider(t, "anthropic", srv.URL, &slept)
	resp, err := p.Complete(context.Background(), llm.Request{System: "sys", Messages: []llm.Message{{Role: "user", Content: "hi"}}, MaxTokens: 50})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != `{"a":1}` || resp.ID != "msg_1" || resp.Usage != (llm.Usage{InputTokens: 100, OutputTokens: 20, CacheRead: 7, CacheWrite: 3}) {
		t.Fatalf("resp %+v", resp)
	}
	if got["system"] != "sys" || got["model"] != "m-1" || got["max_tokens"] != float64(50) {
		t.Fatalf("body %v", got)
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "user" {
		t.Fatalf("messages %v", msgs)
	}
	if p.Name() != "anthropic" || len(slept) != 0 {
		t.Fatalf("name %q slept %v", p.Name(), slept)
	}
}

func TestOpenAICompatible(t *testing.T) {
	for _, provider := range []string{"openai", "deepseek"} {
		t.Run(provider, func(t *testing.T) {
			var got map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer k-test" {
					t.Errorf("bad request %s", r.URL.Path)
				}
				_ = json.NewDecoder(r.Body).Decode(&got)
				_, _ = w.Write([]byte(`{"id":"c-1","model":"m-1","choices":[{"message":{"content":"{}"},"finish_reason":"stop"}],
					"usage":{"prompt_tokens":120,"completion_tokens":9,"prompt_tokens_details":{"cached_tokens":20}}}`))
			}))
			defer srv.Close()
			var slept []time.Duration
			p := newProvider(t, provider, srv.URL, &slept)
			resp, err := p.Complete(context.Background(), llm.Request{System: "sys", Messages: []llm.Message{{Role: "user", Content: "hi"}}, JSON: true})
			if err != nil {
				t.Fatal(err)
			}
			if resp.Text != "{}" || resp.Usage != (llm.Usage{InputTokens: 100, OutputTokens: 9, CacheRead: 20}) || p.Name() != provider {
				t.Fatalf("resp %+v", resp)
			}
			msgs := got["messages"].([]any)
			if len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" || msgs[0].(map[string]any)["content"] != "sys" {
				t.Fatalf("messages %v", msgs)
			}
			if got["response_format"].(map[string]any)["type"] != "json_object" {
				t.Fatalf("response_format %v", got["response_format"])
			}
		})
	}
}

func TestRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
		case 2:
			w.WriteHeader(http.StatusInternalServerError)
		case 3:
			time.Sleep(400 * time.Millisecond)
		default:
			_, _ = w.Write([]byte(`{"id":"c","choices":[{"message":{"content":"ok"}}],"usage":{}}`))
		}
	}))
	defer srv.Close()
	var slept []time.Duration
	p := newProvider(t, "openai", srv.URL, &slept)
	resp, err := p.Complete(context.Background(), llm.Request{Messages: []llm.Message{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "ok" || calls.Load() != 4 {
		t.Fatalf("text %q calls %d", resp.Text, calls.Load())
	}
	want := []time.Duration{2 * time.Second, 2 * time.Second, 4 * time.Second}
	if len(slept) != 3 || slept[0] != want[0] || slept[1] != want[1] || slept[2] != want[2] {
		t.Fatalf("slept %v want %v", slept, want)
	}
}

func TestNoRetryOnClientError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad model"}}`))
	}))
	defer srv.Close()
	var slept []time.Duration
	_, err := newProvider(t, "anthropic", srv.URL, &slept).Complete(context.Background(), llm.Request{})
	var ae *llm.APIError
	if err == nil || calls.Load() != 1 || !errors.As(err, &ae) || ae.Status != 400 || ae.Message != "bad model" {
		t.Fatalf("err %v calls %d", err, calls.Load())
	}
}

func TestRateLimitExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	var slept []time.Duration
	_, err := newProvider(t, "deepseek", srv.URL, &slept).Complete(context.Background(), llm.Request{})
	if !llm.IsRateLimited(err) || len(slept) != 3 {
		t.Fatalf("err %v slept %v", err, slept)
	}
}
