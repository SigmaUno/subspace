package loadtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/client"
)

// FakeServer is a small in-memory stand-in for the Subspace REST + MCP + SSE surface used by the
// load test (docs/api.md subset). It exists so the load test can be tested without the real store.
type FakeServer struct {
	OwnerToken string
	// FailTool makes every FailEvery-th call of this tool return a tool error (0/"" = never).
	FailTool  string
	FailEvery int64

	mu      sync.Mutex
	seq     int64
	tokens  map[string]fakeAgent // token → agent
	agents  map[string]fakeAgent // id → agent
	tasks   map[string]*fakeTask
	plans   map[string][]string
	subs    map[chan []byte]struct{}
	calls   map[string]int64
	evSeq   atomic.Int64
	Handled atomic.Int64
}

type fakeAgent struct{ id, role, provider string }

type fakeTask struct {
	ID       string  `json:"id"`
	BriefID  string  `json:"brief_id"`
	Status   string  `json:"status"`
	Assignee *string `json:"assignee"`
	Reviewer *string `json:"reviewer"`
}

// NewFakeServer returns an empty fake with the given owner token.
func NewFakeServer(ownerToken string) *FakeServer {
	return &FakeServer{OwnerToken: ownerToken, tokens: map[string]fakeAgent{}, agents: map[string]fakeAgent{},
		tasks: map[string]*fakeTask{}, plans: map[string][]string{}, subs: map[chan []byte]struct{}{}, calls: map[string]int64{}}
}

// Count returns the number of tasks with the given status.
func (f *FakeServer) Count(status string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, t := range f.tasks {
		if t.Status == status {
			n++
		}
	}
	return n
}

func (f *FakeServer) nextID(p string) string {
	f.seq++
	return fmt.Sprintf("%s-%d", p, f.seq)
}

func fakeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fakeErr(w http.ResponseWriter, status int, code, msg string) {
	fakeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": msg}})
}

func bearerOf(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

// emit publishes an SSE event to all subscribers; f.mu must be held.
func (f *FakeServer) emit(typ string, data any) {
	seq := f.evSeq.Add(1)
	b, _ := json.Marshal(map[string]any{"type": typ, "ts": time.Now().UTC(), "data": data})
	frame := []byte(fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", seq, typ, b))
	for c := range f.subs {
		select {
		case c <- frame:
		default:
		}
	}
}

// ServeHTTP implements http.Handler.
func (f *FakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.Handled.Add(1)
	switch {
	case r.URL.Path == "/healthz":
		fakeJSON(w, 200, map[string]any{"status": "ok", "version": "fake", "paused": false})
	case r.URL.Path == "/mcp":
		f.serveMCP(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/v1/"):
		if bearerOf(r) != f.OwnerToken {
			w.Header().Set("WWW-Authenticate", "Bearer")
			fakeErr(w, 401, "unauthorized", "invalid token")
			return
		}
		f.serveREST(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *FakeServer) serveREST(w http.ResponseWriter, r *http.Request) {
	var in map[string]any
	if r.Method == http.MethodPost {
		b, _ := io.ReadAll(r.Body)
		if len(b) > 0 && json.Unmarshal(b, &in) != nil {
			fakeErr(w, 400, "bad_request", "invalid JSON")
			return
		}
	}
	p := strings.TrimPrefix(r.URL.Path, "/api/v1")
	switch {
	case r.Method == "GET" && p == "/events":
		f.serveSSE(w, r)
		return
	case r.Method == "POST" && p == "/briefs":
		f.mu.Lock()
		id := f.nextID("B")
		f.mu.Unlock()
		fakeJSON(w, 201, map[string]any{"id": id, "title": in["title"], "status": "open"})
	case r.Method == "POST" && p == "/agents":
		id, _ := in["id"].(string)
		role, _ := in["role"].(string)
		fakeJSON(w, 201, f.register(id, role, fakeProvider(in)))
	case r.Method == "POST" && p == "/agents/bulk":
		prefix, _ := in["prefix"].(string)
		role, _ := in["role"].(string)
		n, _ := in["count"].(float64)
		out := []any{}
		for i := 1; i <= int(n); i++ {
			out = append(out, f.register(fmt.Sprintf("%s-%03d", prefix, i), role, fakeProvider(in)))
		}
		fakeJSON(w, 201, map[string]any{"agents": out})
	case r.Method == "POST" && strings.HasPrefix(p, "/plans/") && strings.HasSuffix(p, "/approve"):
		pid := strings.TrimSuffix(strings.TrimPrefix(p, "/plans/"), "/approve")
		f.mu.Lock()
		defer f.mu.Unlock()
		ids, ok := f.plans[pid]
		if !ok {
			fakeErr(w, 404, "not_found", "plan not found")
			return
		}
		for _, id := range ids {
			f.tasks[id].Status = "ready"
		}
		f.emit("plan.approved", map[string]any{"id": pid})
		fakeJSON(w, 200, map[string]any{"id": pid, "status": "approved"})
	case r.Method == "POST" && strings.HasPrefix(p, "/tasks/") && strings.HasSuffix(p, "/done"):
		id := strings.TrimSuffix(strings.TrimPrefix(p, "/tasks/"), "/done")
		f.mu.Lock()
		defer f.mu.Unlock()
		t, ok := f.tasks[id]
		if !ok {
			fakeErr(w, 404, "not_found", "task not found")
			return
		}
		if t.Status != "approved" {
			fakeErr(w, 409, "conflict", "task is "+t.Status)
			return
		}
		t.Status = "done"
		f.emit("task.updated", t)
		fakeJSON(w, 200, t)
	default:
		fakeErr(w, 404, "not_found", "no such endpoint")
	}
}

func fakeProvider(in map[string]any) string {
	if p, _ := in["provider"].(string); p != "" {
		return p
	}
	k, _ := in["kind"].(string)
	switch k {
	case "claude-code":
		return "anthropic"
	case "codex":
		return "openai"
	}
	return "other"
}

func (f *FakeServer) register(id, role, provider string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	tok := fmt.Sprintf("ssp_fake%d", f.seq)
	a := fakeAgent{id: id, role: role, provider: provider}
	f.tokens[tok], f.agents[id] = a, a
	return map[string]any{"agent": map[string]any{"id": id, "role": role, "provider": provider}, "connect": map[string]any{"agent_id": id, "token": tok}}
}

func (f *FakeServer) serveSSE(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flusher", http.StatusInternalServerError)
		return
	}
	c := make(chan []byte, 1024)
	f.mu.Lock()
	f.subs[c] = struct{}{}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.subs, c)
		f.mu.Unlock()
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(w, "id: %d\nevent: hello\ndata: {\"type\":\"hello\",\"ts\":%q,\"data\":{\"seq\":%d}}\n\n: ping\n\n",
		f.evSeq.Load(), time.Now().UTC().Format(time.RFC3339Nano), f.evSeq.Load())
	fl.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-c:
			_, _ = w.Write(b)
			fl.Flush()
		}
	}
}

func (f *FakeServer) serveMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tok := bearerOf(r)
	if tok == f.OwnerToken {
		http.Error(w, "owner token not valid on /mcp", http.StatusForbidden)
		return
	}
	f.mu.Lock()
	ag, ok := f.tokens[tok]
	f.mu.Unlock()
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fakeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "parse error"}})
		return
	}
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	switch req.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "fake-"+ag.id)
		resp["result"] = map[string]any{"protocolVersion": client.ProtocolVersion}
	case "tools/call":
		var args map[string]any
		_ = json.Unmarshal(req.Params.Arguments, &args)
		v, code, msg := f.tool(ag, req.Params.Name, args)
		if code != "" {
			v = map[string]any{"error": map[string]any{"code": code, "message": msg}}
		}
		resp["result"] = map[string]any{"content": []any{}, "structuredContent": v, "isError": code != ""}
	default:
		resp["error"] = map[string]any{"code": -32601, "message": "method not found"}
	}
	fakeJSON(w, 200, resp)
}

func (f *FakeServer) tool(a fakeAgent, name string, args map[string]any) (any, string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[name]++
	if name == f.FailTool && f.FailEvery > 0 && f.calls[name]%f.FailEvery == 0 {
		return nil, "internal", "injected failure"
	}
	id, _ := args["task_id"].(string)
	t := f.tasks[id]
	mine := t != nil && t.Assignee != nil && *t.Assignee == a.id
	move := func(to string) (any, string, string) {
		t.Status = to
		f.emit("task.updated", t)
		return map[string]any{"task": t}, "", ""
	}
	switch name {
	case "get_instructions":
		return map[string]any{"instructions": "fake", "role": a.role, "agent_id": a.id}, "", ""
	case "propose_plan":
		ts, _ := args["tasks"].([]any)
		pid := f.nextID("P")
		var list []any
		for range ts {
			nt := &fakeTask{ID: f.nextID("T"), Status: "backlog"}
			f.tasks[nt.ID] = nt
			f.plans[pid] = append(f.plans[pid], nt.ID)
			list = append(list, nt)
		}
		return map[string]any{"plan": map[string]any{"id": pid, "status": "proposed", "tasks": list}}, "", ""
	case "create_task":
		nt := &fakeTask{ID: f.nextID("T"), Status: "ready"}
		f.tasks[nt.ID] = nt
		f.emit("task.created", nt)
		return map[string]any{"task": nt}, "", ""
	case "assign":
		aid, _ := args["agent_id"].(string)
		if t == nil || t.Status != "ready" {
			return nil, "conflict", "task not ready"
		}
		if _, ok := f.agents[aid]; !ok {
			return nil, "not_found", "agent not found"
		}
		t.Assignee = &aid
		return move("assigned")
	case "list_my_tasks":
		want := map[string]bool{}
		if ss, ok := args["status"].([]any); ok {
			for _, s := range ss {
				want[fmt.Sprint(s)] = true
			}
		}
		out := []any{}
		for _, x := range f.tasks {
			if x.Assignee != nil && *x.Assignee == a.id && (len(want) == 0 || want[x.Status]) {
				out = append(out, x)
			}
		}
		return map[string]any{"tasks": out}, "", ""
	case "claim_task":
		if !mine || (t.Status != "assigned" && t.Status != "changes_requested") {
			return nil, "not_assigned", "cannot claim"
		}
		return move("in_progress")
	case "heartbeat":
		if !mine || t.Status != "in_progress" {
			return nil, "not_assigned", "not in progress"
		}
		return map[string]any{"ok": true, "paused": false}, "", ""
	case "update_status":
		if !mine || t.Status != "in_progress" {
			return nil, "conflict", "not in progress"
		}
		return move("in_progress")
	case "submit":
		if !mine || t.Status != "in_progress" {
			return nil, "conflict", "not in progress"
		}
		return move("submitted")
	case "list_reviews":
		out := []any{}
		for _, x := range f.tasks {
			if x.Status == "submitted" {
				out = append(out, x)
			}
		}
		return map[string]any{"tasks": out}, "", ""
	case "review":
		if a.role != "reviewer" {
			return nil, "rpc-32601", "tool not available for role"
		}
		if t == nil || (t.Status != "submitted" && t.Status != "in_review") {
			return nil, "conflict", "not awaiting review"
		}
		if wk := f.agents[*t.Assignee]; wk.provider == a.provider {
			return nil, "forbidden", "same provider"
		}
		t.Reviewer = &a.id
		return move("approved")
	}
	return nil, "not_found", "unknown tool " + name
}
