package client_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/model"
)

type sleeps struct {
	mu sync.Mutex
	d  []time.Duration
}

func (s *sleeps) sleep(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	s.d = append(s.d, d)
	s.mu.Unlock()
	return nil
}

func TestRetriesRateLimitedHonoringRetryAfter(t *testing.T) {
	var mu sync.Mutex
	calls, limited := 0, 2
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n <= limited {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"code":"rate_limited","message":"too many requests"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"task":{"id":"T-1"}},"isError":false}}`)
	}))
	defer ts.Close()

	s := &sleeps{}
	c := client.NewMCP(ts.URL, "tok", client.WithSleep(s.sleep))
	task, err := c.ClaimTask(context.Background(), "T-1")
	if err != nil || task.ID != "T-1" {
		t.Fatalf("claim: %v %v", task, err)
	}
	if !slices.Equal(s.d, []time.Duration{2 * time.Second, 2 * time.Second}) {
		t.Fatalf("slept %v, want Retry-After twice", s.d)
	}

	mu.Lock()
	calls, limited = 0, 100
	mu.Unlock()
	c = client.NewMCP(ts.URL, "tok", client.WithSleep(s.sleep), client.WithRetries(2))
	if _, err := c.WhoAmI(context.Background()); client.StatusOf(err) != http.StatusTooManyRequests {
		t.Fatalf("want 429 after retries, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("%d attempts, want 3", calls)
	}
}

type dropTransport struct {
	mu   sync.Mutex
	drop map[string]int
	keys map[string][]string
}

var errDropped = errors.New("connection reset after request was handled")

func (d *dropTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	var req struct {
		Params struct {
			Name string `json:"name"`
			Meta struct {
				Key string `json:"idempotencyKey"`
			} `json:"_meta"`
		} `json:"params"`
	}
	_ = json.Unmarshal(body, &req)
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	name := req.Params.Name
	d.keys[name] = append(d.keys[name], req.Params.Meta.Key)
	if d.drop[name] > 0 {
		d.drop[name]--
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return nil, errDropped
	}
	return resp, nil
}

func TestRetriesDroppedResponseAppliesOnce(t *testing.T) {
	e := newEnv(t)
	tm := e.team()
	b := e.brief()
	task := e.readyTask(tm.leader, b.ID, "drop")
	if _, err := tm.leader.Assign(e.ctx, task.ID, "wally", ""); err != nil {
		t.Fatal(err)
	}
	r, err := e.owner.RotateToken(e.ctx, "wally")
	if err != nil {
		t.Fatal(err)
	}
	tr := &dropTransport{drop: map[string]int{"claim_task": 2, "add_note": 1, "heartbeat": 1}, keys: map[string][]string{}}
	s := &sleeps{}
	wk := client.NewMCP(e.url+"/mcp", r.Connect.Token, client.WithHTTPClient(&http.Client{Transport: tr}), client.WithSleep(s.sleep))

	got, err := wk.ClaimTask(e.ctx, task.ID)
	if err != nil || got.Status != model.StatusInProgress {
		t.Fatalf("claim with dropped responses: %v %v", got, err)
	}
	msg, err := wk.AddNote(e.ctx, task.ID, "exactly once")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wk.Heartbeat(e.ctx, task.ID); !errors.Is(err, errDropped) {
		t.Fatalf("keyless heartbeat must not be retried, got %v", err)
	}

	for tool, want := range map[string]int{"claim_task": 3, "add_note": 2, "heartbeat": 1} {
		keys := tr.keys[tool]
		if len(keys) != want {
			t.Fatalf("%s sent %d times, want %d", tool, len(keys), want)
		}
		if tool != "heartbeat" && (keys[0] == "" || slices.ContainsFunc(keys, func(k string) bool { return k != keys[0] })) {
			t.Fatalf("%s retries must reuse one key: %v", tool, keys)
		}
	}
	msgs, err := e.owner.ThreadMessages(e.ctx, "task", task.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	notes := 0
	for _, m := range msgs {
		if m.Body == "exactly once" {
			notes++
			if m.ID != msg.ID {
				t.Fatalf("returned message %d, stored %d", msg.ID, m.ID)
			}
		}
	}
	if notes != 1 {
		t.Fatalf("note applied %d times", notes)
	}
}
