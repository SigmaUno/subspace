package chat_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/chat"
	"github.com/SigmaUno/subspace/internal/model"
)

type fakeDir struct {
	agents     map[string]*model.Agent
	components map[string][]string
	briefs     map[string][]string
	leader     string
}

func (d *fakeDir) Agent(_ context.Context, id string) (*model.Agent, error) {
	return d.agents[id], nil
}

func (d *fakeDir) ByComponent(_ context.Context, id string) ([]string, error) {
	return slices.Clone(d.components[id]), nil
}

func (d *fakeDir) ByBrief(_ context.Context, id string) ([]string, error) {
	return slices.Clone(d.briefs[id]), nil
}

func (d *fakeDir) ByRole(_ context.Context, role string) ([]string, error) {
	var ids []string
	for id, a := range d.agents {
		if a.Role == role && a.Status != model.AgentRevoked {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (d *fakeDir) All(ctx context.Context) ([]string, error) {
	var ids []string
	for _, role := range []string{model.RoleLeader, model.RoleWorker} {
		more, _ := d.ByRole(ctx, role)
		ids = append(ids, more...)
	}
	return ids, nil
}

func (d *fakeDir) Leader(context.Context) (string, error) { return d.leader, nil }

type fakeThreads struct {
	mu      sync.Mutex
	next    int64
	entries map[string][]chat.Entry
}

func (f *fakeThreads) Append(_ context.Context, agentID, convID string, m model.Message) (model.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	m.ID = f.next
	f.entries[agentID] = append(f.entries[agentID], chat.Entry{Message: m, ConvID: convID})
	return m, nil
}

func (f *fakeThreads) Messages(_ context.Context, agentID string, since int64) ([]chat.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []chat.Entry
	for _, e := range f.entries[agentID] {
		if e.Message.ID > since {
			out = append(out, e)
		}
	}
	return out, nil
}

type harness struct {
	t       *testing.T
	router  *chat.Router
	handler http.Handler
	threads *fakeThreads
	clock   time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		t:       t,
		threads: &fakeThreads{entries: map[string][]chat.Entry{}},
		clock:   time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC),
	}
	dir := &fakeDir{
		agents: map[string]*model.Agent{
			"w1":  {ID: "w1", Role: model.RoleWorker, Status: model.AgentActive},
			"w2":  {ID: "w2", Role: model.RoleWorker, Status: model.AgentActive},
			"old": {ID: "old", Role: model.RoleWorker, Status: model.AgentRevoked},
			"rex": {ID: "rex", Role: model.RoleLeader, Status: model.AgentActive},
		},
		components: map[string][]string{"api": {"w2", "w1", "w2"}},
		briefs:     map[string][]string{"B-1": {"w1", "rex"}},
		leader:     "rex",
	}
	n := 0
	h.router = chat.NewRouter(chat.Config{
		Directory: dir,
		Threads:   h.threads,
		Now:       func() time.Time { h.clock = h.clock.Add(time.Second); return h.clock },
		NewID:     func() string { n++; return fmt.Sprintf("C-%d", n) },
	})
	h.handler = h.router.Handler()
	return h
}

func (h *harness) do(method, path string, body any) (int, []byte) {
	h.t.Helper()
	var rd bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		rd = *bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, &rd)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func (h *harness) send(to, body string) chat.Conversation {
	h.t.Helper()
	code, raw := h.do("POST", "/messages", chat.Message{To: to, Body: body})
	if code != http.StatusCreated {
		h.t.Fatalf("send %s: status %d: %s", to, code, raw)
	}
	var c chat.Conversation
	if err := json.Unmarshal(raw, &c); err != nil {
		h.t.Fatal(err)
	}
	return c
}

func (h *harness) reply(agentID, body string) {
	h.t.Helper()
	h.clock = h.clock.Add(time.Second)
	_, err := h.threads.Append(context.Background(), agentID, "", model.Message{
		ThreadKind: model.ThreadAgent,
		ThreadID:   agentID,
		AuthorID:   agentID,
		AuthorRole: model.RoleWorker,
		Kind:       model.MsgMessage,
		Body:       body,
		CreatedAt:  h.clock,
	})
	if err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) conversation(id string) chat.Conversation {
	h.t.Helper()
	code, raw := h.do("GET", "/conversations/"+id, nil)
	if code != http.StatusOK {
		h.t.Fatalf("get %s: status %d: %s", id, code, raw)
	}
	var c chat.Conversation
	if err := json.Unmarshal(raw, &c); err != nil {
		h.t.Fatal(err)
	}
	return c
}

func bodies(c chat.Conversation) []string {
	var out []string
	for _, m := range c.Messages {
		out = append(out, m.AuthorID+": "+m.Body)
	}
	return out
}

func TestOwnerChatFlow(t *testing.T) {
	h := newHarness(t)

	c1 := h.send("component:api", "status of the api work?")
	if !slices.Equal(c1.Recipients, []string{"w1", "w2"}) {
		t.Fatalf("recipients = %v", c1.Recipients)
	}
	if h.router.Unread("w1") != 1 || h.router.Unread("w2") != 1 || h.router.Unread("rex") != 0 {
		t.Fatalf("unread after send: w1=%d w2=%d rex=%d", h.router.Unread("w1"), h.router.Unread("w2"), h.router.Unread("rex"))
	}

	h.reply("w2", "half done")
	h.router.MarkRead("w1")
	if h.router.Unread("w1") != 0 || h.router.Unread("w2") != 1 {
		t.Fatalf("unread after mark read: w1=%d w2=%d", h.router.Unread("w1"), h.router.Unread("w2"))
	}
	h.reply("w1", "done, PR open")

	c2 := h.send("agent:w1", "thanks, please add tests")
	h.reply("w1", "tests added")
	h.reply("w2", "late reply to the first question")

	want1 := []string{
		"owner: status of the api work?",
		"owner: status of the api work?",
		"w2: half done",
		"w1: done, PR open",
		"w2: late reply to the first question",
	}
	if got := bodies(h.conversation(c1.ID)); !slices.Equal(got, want1) {
		t.Fatalf("conversation 1 = %q, want %q", got, want1)
	}
	if got := bodies(h.conversation(c2.ID)); !slices.Equal(got, []string{"owner: thanks, please add tests", "w1: tests added"}) {
		t.Fatalf("conversation 2 = %q", got)
	}

	code, raw := h.do("GET", "/conversations", nil)
	if code != http.StatusOK {
		t.Fatalf("list status %d", code)
	}
	var list struct {
		Conversations []chat.Conversation `json:"conversations"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Conversations) != 2 || list.Conversations[0].ID != c2.ID || list.Conversations[1].ID != c1.ID {
		t.Fatalf("list = %+v", list.Conversations)
	}
}

func TestSendTargets(t *testing.T) {
	h := newHarness(t)

	if got := h.send("leader", "hello").Recipients; !slices.Equal(got, []string{"rex"}) {
		t.Fatalf("leader recipients = %v", got)
	}
	if got := h.send("brief:B-1", "brief update").Recipients; !slices.Equal(got, []string{"rex", "w1"}) {
		t.Fatalf("brief recipients = %v", got)
	}

	if got := h.send("role:worker", "workers only").Recipients; !slices.Equal(got, []string{"w1", "w2"}) {
		t.Fatalf("role recipients = %v", got)
	}
	if got := h.send("all", "everyone").Recipients; !slices.Equal(got, []string{"rex", "w1", "w2"}) {
		t.Fatalf("all recipients = %v", got)
	}

	cases := []struct {
		name string
		body any
		code int
	}{
		{"unknown role", chat.Message{To: "role:janitor", Body: "x"}, http.StatusBadRequest},
		{"role without agents", chat.Message{To: "role:tester", Body: "x"}, http.StatusNotFound},
		{"role missing", chat.Message{To: "role:", Body: "x"}, http.StatusBadRequest},
		{"unknown agent", chat.Message{To: "agent:ghost", Body: "x"}, http.StatusNotFound},
		{"revoked agent", chat.Message{To: "agent:old", Body: "x"}, http.StatusConflict},
		{"empty component", chat.Message{To: "component:none", Body: "x"}, http.StatusNotFound},
		{"bad target", chat.Message{To: "everyone", Body: "x"}, http.StatusBadRequest},
		{"missing id", chat.Message{To: "agent:", Body: "x"}, http.StatusBadRequest},
		{"blank body", chat.Message{To: "leader", Body: "  "}, http.StatusBadRequest},
		{"unknown field", map[string]string{"to": "leader", "body": "x", "extra": "y"}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		if code, raw := h.do("POST", "/messages", tc.body); code != tc.code {
			t.Errorf("%s: status %d, want %d: %s", tc.name, code, tc.code, raw)
		}
	}
	if code, _ := h.do("GET", "/conversations/C-999", nil); code != http.StatusNotFound {
		t.Errorf("unknown conversation: status %d", code)
	}
	if h.router.Unread("rex") != 3 || h.router.Unread("w1") != 3 {
		t.Errorf("unread rex=%d w1=%d", h.router.Unread("rex"), h.router.Unread("w1"))
	}
	if h.router.Unread("old") != 0 || h.router.Unread("ghost") != 0 {
		t.Error("rejected messages must not create unread counts")
	}
}
