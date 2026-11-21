package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/SigmaUno/subspace/internal/servertest"
)

type collector struct {
	*httptest.Server
	mu    sync.Mutex
	spans []map[string]any
}

func newCollector(t *testing.T) *collector {
	t.Helper()
	c := &collector{}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct {
			ResourceSpans []struct {
				ScopeSpans []struct {
					Spans []map[string]any `json:"spans"`
				} `json:"scopeSpans"`
			} `json:"resourceSpans"`
		}
		if r.URL.Path != "/v1/traces" || json.Unmarshal(b, &req) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		c.mu.Lock()
		for _, rs := range req.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				c.spans = append(c.spans, ss.Spans...)
			}
		}
		c.mu.Unlock()
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *collector) find(traceID, name string) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.spans {
		if s["traceId"] == traceID && s["name"] == name {
			return s
		}
	}
	return nil
}

func tracedRequest(t *testing.T, e *servertest.Env, method, path string, body any, traceparent string) (*http.Response, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, e.TS.URL+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+servertest.OwnerToken)
	if traceparent != "" {
		req.Header.Set("traceparent", traceparent)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestTracePropagatesThroughAsyncPaths(t *testing.T) {
	col := newCollector(t)
	t.Setenv("SUBSPACE_OTLP_ENDPOINT", col.URL)
	t.Setenv("SUBSPACE_OTLP_ALLOW_PRIVATE", "true")
	t.Setenv("SUBSPACE_OTLP_SAMPLE", "0")
	t.Setenv("SUBSPACE_WEBHOOKS_OUT_ALLOW_CIDRS", "127.0.0.0/8")
	e, _ := newTokenEnv(t)
	rcv := newOutReceiver(t)

	code, out := e.REST("POST", "/api/v1/webhooks-out", map[string]any{"url": rcv.URL + "/in", "events": []string{"agent.updated"}})
	if code != 201 {
		t.Fatalf("create webhook: %d %v", code, out)
	}

	const (
		traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
		parent  = "00f067aa0ba902b7"
	)
	resp, out := tracedRequest(t, e, "POST", "/api/v1/agents",
		map[string]any{"id": "w1", "role": "worker", "kind": "claude-code", "model": "m"}, "00-"+traceID+"-"+parent+"-01")
	if resp.StatusCode != 201 {
		t.Fatalf("register: %d %v", resp.StatusCode, out)
	}
	if got := resp.Header.Get("X-Trace-Id"); got != traceID {
		t.Fatalf("X-Trace-Id %q, want %q", got, traceID)
	}

	waitOut(t, "webhook delivery", func() bool { return rcv.count() == 1 })
	hit := rcv.hit(0)
	var payload map[string]any
	if err := json.Unmarshal(hit.body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["trace_id"] != traceID {
		t.Fatalf("webhook payload trace_id %v, want %s", payload["trace_id"], traceID)
	}
	if tp := hit.header.Get("Traceparent"); !strings.HasPrefix(tp, "00-"+traceID+"-") {
		t.Fatalf("webhook traceparent %q", tp)
	}

	code, out = e.REST("GET", "/api/v1/audit?limit=50", nil)
	if code != 200 {
		t.Fatalf("audit: %d %v", code, out)
	}
	var note string
	for _, ev := range out["events"].([]any) {
		m := ev.(map[string]any)
		if m["tool"] == "register_agents" {
			note, _ = m["note"].(string)
		}
	}
	if want := "token=root; trace=" + traceID; note != want {
		t.Fatalf("audit note %q, want %q", note, want)
	}
	code, out = e.REST("GET", "/api/v1/audit/verify", nil)
	if code != 200 || out["ok"] != true {
		t.Fatalf("audit chain: %d %v", code, out)
	}

	code, out = e.REST("GET", "/api/v1/activity?limit=50", nil)
	if code != 200 {
		t.Fatalf("activity: %d %v", code, out)
	}
	var seen bool
	for _, it := range out["items"].([]any) {
		m := it.(map[string]any)
		if m["tool"] == "register_agents" {
			seen = m["trace_id"] == traceID
			if strings.Contains(m["text"].(string), "trace=") {
				t.Fatalf("activity text leaks the trace note: %v", m["text"])
			}
		}
	}
	if !seen {
		t.Fatalf("activity item lacks trace_id: %v", out["items"])
	}

	e.Stop()
	for _, name := range []string{"POST /api/v1/agents", "store.commit", "webhook.deliver"} {
		if col.find(traceID, name) == nil {
			t.Fatalf("collector has no %q span in trace %s: %v", name, traceID, col.spans)
		}
	}
	if s := col.find(traceID, "POST /api/v1/agents"); s["parentSpanId"] != parent || s["kind"] != float64(2) {
		t.Fatalf("route span %v", s)
	}
}

func TestTraceMCPMetaAndUnsampledHeaders(t *testing.T) {
	col := newCollector(t)
	t.Setenv("SUBSPACE_OTLP_ENDPOINT", col.URL)
	t.Setenv("SUBSPACE_OTLP_ALLOW_PRIVATE", "true")
	t.Setenv("SUBSPACE_OTLP_SAMPLE", "0")
	e, _ := newTokenEnv(t)
	tok := e.Register("w1", "worker", "claude-code")

	const (
		metaTrace = "0af7651916cd43dd8448eb211c80319c"
		metaSpan  = "b7ad6b7169203331"
		hdrTrace  = "4bf92f3577b34da6a3ce929d0e0e4736"
	)
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
		"name": "whoami", "arguments": map[string]any{}, "_meta": map[string]any{"traceparent": "00-" + metaTrace + "-" + metaSpan + "-01"}}})
	req, _ := http.NewRequest("POST", e.TS.URL+"/mcp", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("traceparent", "00-"+hdrTrace+"-00f067aa0ba902b7-00")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := resp.Header.Get("X-Trace-Id"); got != hdrTrace {
		t.Fatalf("X-Trace-Id %q, want the header trace %q", got, hdrTrace)
	}

	e.Stop()
	s := col.find(metaTrace, "tools/call whoami")
	if s == nil || s["parentSpanId"] != metaSpan {
		t.Fatalf("tool span %v in %v", s, col.spans)
	}
	col.mu.Lock()
	defer col.mu.Unlock()
	for _, sp := range col.spans {
		if sp["traceId"] == hdrTrace {
			t.Fatalf("unsampled header trace was exported: %v", sp)
		}
	}
}
