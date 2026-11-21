package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/model"
)

type memSessions struct {
	mu   sync.Mutex
	byID map[string]string
}

func (m *memSessions) CreateSession(_ context.Context, id, agent string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byID[id] = agent
	return nil
}

func (m *memSessions) SessionValid(_ context.Context, id, agent string, _ time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byID[id] == agent, nil
}

func (m *memSessions) DropSession(_ context.Context, id, agent string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.byID[id] != agent {
		return false, nil
	}
	delete(m.byID, id)
	return true, nil
}

func sharedSessionServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	return pushMCPServer(t, &memSessions{byID: map[string]string{}})
}

func pushMCPServer(t *testing.T, sessions SessionStore) (*Server, *httptest.Server) {
	t.Helper()
	s, _ := confServer()
	s.Sessions = sessions
	s.Push = NewPush()
	s.Push.Tasks = ownersFunc(func(string) (string, string, bool) { return "", "", false })
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return s, ts
}

type ownersFunc func(string) (string, string, bool)

func (f ownersFunc) Owners(_ context.Context, id string) (string, string, bool) { return f(id) }

func openStream(t *testing.T, ts *httptest.Server, tok, sid string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", sid)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func pushStreams(p *Push) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.streams)
}

func TestPushStreamWorksWithSharedSessions(t *testing.T) {
	s, ts := sharedSessionServer(t)
	sid := post(t, s, "tok-worker", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`).Header().Get("Mcp-Session-Id")
	resp := openStream(t, ts, "tok-worker", sid)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream on a shared session: %d", resp.StatusCode)
	}
	if foreign := openStream(t, ts, "tok-tester", sid); foreign.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign session: %d", foreign.StatusCode)
	}

	r := httptest.NewRequest(http.MethodDelete, "/mcp", nil)
	r.Header.Set("Authorization", "Bearer tok-worker")
	r.Header.Set("Mcp-Session-Id", sid)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}
	if n := pushStreams(s.Push); n != 0 {
		t.Fatalf("%d push streams left after DELETE", n)
	}
}

func TestPushStreamsPerAgentStayBoundedWithSharedSessions(t *testing.T) {
	s, _ := sharedSessionServer(t)
	for range 500 {
		sid := post(t, s, "tok-worker", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`).Header().Get("Mcp-Session-Id")
		post(t, s, "tok-worker", `{"jsonrpc":"2.0","id":2,"method":"resources/subscribe","params":{"uri":"subspace://thread/agent/me"}}`,
			"Mcp-Session-Id", sid)
	}
	if n := pushStreams(s.Push); n > maxSessionsPer {
		t.Fatalf("%d push streams for one agent", n)
	}
	s.Push.mu.Lock()
	uris := len(s.Push.byURI["subspace://thread/agent/claude-1"])
	s.Push.mu.Unlock()
	if uris > maxSessionsPer {
		t.Fatalf("%d subscriptions indexed for one agent", uris)
	}
}

func TestRevokedAgentLosesItsPushStream(t *testing.T) {
	s, ts := pushMCPServer(t, nil)
	sid := post(t, s, "tok-worker", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`).Header().Get("Mcp-Session-Id")
	hub := events.NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	(&PushWatcher{Push: s.Push}).Activate(ctx, hub)

	resp := openStream(t, ts, "tok-worker", sid)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream: %d", resp.StatusCode)
	}
	hub.Publish([]string{events.TopicAll}, "agent.updated", &model.Agent{ID: "claude-1", Status: model.AgentRevoked})
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			if _, err := resp.Body.Read(buf); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("push stream stayed open after the agent was revoked")
	}
	if n := pushStreams(s.Push); n != 0 {
		t.Fatalf("%d push streams left for a revoked agent", n)
	}
}

func TestPushWatcherForgetsFinishedTasksAndRevokedAgents(t *testing.T) {
	w := &PushWatcher{Push: NewPush()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Activate(ctx, events.NewHub())
	agent := "w1"
	for i := range 200 {
		id := fmt.Sprintf("T-%d", i)
		final := model.StatusDone
		if i%2 == 1 {
			final = model.StatusCancelled
		}
		for _, st := range []string{model.StatusAssigned, model.StatusInProgress, final} {
			w.handle(ctx, events.Event{Type: "task.updated", Data: &model.Task{ID: id, Status: st, Assignee: &agent}})
		}
		w.handle(ctx, events.Event{Type: "agent.updated", Data: &model.Agent{ID: fmt.Sprintf("a%d", i), Status: model.AgentActive}})
		w.handle(ctx, events.Event{Type: "agent.updated", Data: &model.Agent{ID: fmt.Sprintf("a%d", i), Status: model.AgentRevoked}})
	}
	if len(w.tasks) != 0 || len(w.agents) != 0 {
		t.Fatalf("tracking %d finished tasks and %d revoked agents", len(w.tasks), len(w.agents))
	}
}
