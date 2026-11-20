package bots_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/bots"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/spawner"
	"github.com/SigmaUno/subspace/internal/store"
)

type memStore struct {
	mu       sync.Mutex
	bots     map[string]*bots.Bot
	released []string
}

func newMemStore() *memStore { return &memStore{bots: map[string]*bots.Bot{}} }

func (m *memStore) Insert(_ context.Context, bs []*bots.Bot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range bs {
		if _, ok := m.bots[b.ID]; ok {
			return store.ErrDuplicate
		}
	}
	for _, b := range bs {
		c := *b
		m.bots[b.ID] = &c
	}
	return nil
}

func (m *memStore) Get(_ context.Context, id string) (*bots.Bot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.bots[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	c := *b
	return &c, nil
}

func (m *memStore) List(_ context.Context, role, status string) ([]*bots.Bot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*bots.Bot
	for _, b := range m.bots {
		if (role == "" || b.Role == role) && (status == "" || b.Status == status) {
			c := *b
			out = append(out, &c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *memStore) Update(_ context.Context, b *bots.Bot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.bots[b.ID]; !ok {
		return store.ErrNotFound
	}
	c := *b
	m.bots[b.ID] = &c
	return nil
}

func (m *memStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.bots, id)
	return nil
}

func (m *memStore) ReleaseLeases(_ context.Context, id, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.released = append(m.released, id)
	return nil
}

type fakeSpawner struct {
	mu      sync.Mutex
	spawned []spawner.Request
	stopped []string
	fail    error
}

func (f *fakeSpawner) Spawn(_ context.Context, r spawner.Request) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.spawned = append(f.spawned, r)
	return nil
}

func (f *fakeSpawner) Stop(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, id)
	return nil
}

type fakeCapacity struct {
	st  *memStore
	max int
}

func (c *fakeCapacity) Check(_ context.Context, _, _ string, weight int) error {
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	used := 0
	for _, b := range c.st.bots {
		if b.Spawned {
			used += b.Weight
		}
	}
	if used+weight > c.max {
		return model.Errf("capacity", "%d in use + %d > max %d", used, weight, c.max)
	}
	return nil
}

type fakeAuditor struct {
	mu     sync.Mutex
	events []*model.AuditEvent
}

func (a *fakeAuditor) Record(_ context.Context, e *model.AuditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
	return nil
}

func (a *fakeAuditor) tools() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, e := range a.events {
		out = append(out, e.Tool)
	}
	return out
}

type env struct {
	srv   *httptest.Server
	svc   *bots.Service
	st    *memStore
	sp    *fakeSpawner
	audit *fakeAuditor
}

func newEnv(t *testing.T, capMax int) *env {
	t.Helper()
	st := newMemStore()
	e := &env{st: st, sp: &fakeSpawner{}, audit: &fakeAuditor{}}
	clock := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	e.svc = bots.New(bots.Deps{
		Agents: st, Spawner: e.sp, Capacity: &fakeCapacity{st: st, max: capMax}, Auditor: e.audit,
		PublicURL: "https://s.example.com", Now: func() time.Time { return clock },
	})
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", http.StripPrefix("/api/v1", bots.Handler(e.svc, nil)))
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) do(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, e.srv.URL+"/api/v1"+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}

func (e *env) create(t *testing.T, name, kind string, weight int) (token string) {
	t.Helper()
	code, body := e.do(t, "POST", "/bots", map[string]any{"name": name, "role": "worker", "kind": kind, "model": "m", "capacity_weight": weight})
	if code != http.StatusCreated {
		t.Fatalf("create %s: %d %s", name, code, body)
	}
	var out struct {
		Connect struct{ Token string }
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out.Connect.Token
}

func errCode(t *testing.T, body []byte) string {
	t.Helper()
	var out struct{ Error struct{ Code string } }
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out.Error.Code
}

func TestCreateShowsTokenOnceAndStoresHash(t *testing.T) {
	e := newEnv(t, 10)
	code, body := e.do(t, "POST", "/bots", map[string]any{"name": "ds-1", "kind": "opencode-deepseek", "model": "deepseek-chat", "capacity_weight": 3})
	if code != http.StatusCreated {
		t.Fatalf("%d %s", code, body)
	}
	var out struct {
		Bot     bots.Bot
		Connect bots.Connect
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	tok := out.Connect.Token
	if !strings.HasPrefix(tok, auth.TokenPrefix) {
		t.Fatalf("token %q", tok)
	}
	if out.Bot.Kind != model.KindOpenCode || out.Bot.Provider != "deepseek" || out.Bot.Weight != 3 || out.Bot.Role != model.RoleWorker {
		t.Fatalf("bot %+v", out.Bot)
	}
	if !strings.Contains(out.Connect.JoinLine, "https://s.example.com as ds-1.") {
		t.Fatalf("join line %q", out.Connect.JoinLine)
	}
	stored, _ := e.st.Get(context.Background(), "ds-1")
	if stored.TokenHash != auth.Hash(tok) {
		t.Fatal("stored hash does not match issued token")
	}

	_, list := e.do(t, "GET", "/bots", nil)
	if bytes.Contains(list, []byte(tok)) || bytes.Contains(list, []byte(stored.TokenHash)) {
		t.Fatalf("list leaks credentials: %s", list)
	}
	for _, ev := range e.audit.events {
		b, _ := json.Marshal(ev)
		if bytes.Contains(b, []byte(tok)) {
			t.Fatal("audit leaks token")
		}
	}
	if got := e.audit.tools(); len(got) != 1 || got[0] != "create_bot" {
		t.Fatalf("audit %v", got)
	}
}

func TestCreateValidationAndDuplicate(t *testing.T) {
	e := newEnv(t, 10)
	e.create(t, "a", "claude-code", 0)
	cases := []struct {
		body map[string]any
		want int
		code string
	}{
		{map[string]any{"name": "a", "kind": "codex"}, 409, "conflict"},
		{map[string]any{"name": "Bad Name", "kind": "codex"}, 400, "bad_request"},
		{map[string]any{"name": "b", "kind": "gpt"}, 400, "bad_request"},
		{map[string]any{"name": "b", "kind": "opencode-anthropic", "provider": "deepseek"}, 400, "bad_request"},
		{map[string]any{"name": "b", "kind": "codex", "role": "owner"}, 400, "bad_request"},
		{map[string]any{"name": "b", "kind": "codex", "capacity_weight": -2}, 400, "bad_request"},
	}
	for _, c := range cases {
		code, body := e.do(t, "POST", "/bots", c.body)
		if code != c.want || errCode(t, body) != c.code {
			t.Errorf("%v: %d %s", c.body, code, body)
		}
	}
	if n := len(e.audit.events); n != 1 {
		t.Fatalf("failed requests must not be audited, got %d events", n)
	}
}

func TestBulkCreateIsAtomic(t *testing.T) {
	e := newEnv(t, 10)
	e.create(t, "w-002", "codex", 1)
	code, body := e.do(t, "POST", "/bots/bulk", map[string]any{"prefix": "w", "count": 3, "kind": "codex", "model": "m"})
	if code != http.StatusConflict {
		t.Fatalf("%d %s", code, body)
	}
	if bs, _ := e.st.List(context.Background(), "", ""); len(bs) != 1 {
		t.Fatalf("partial insert: %d bots", len(bs))
	}

	code, body = e.do(t, "POST", "/bots/bulk", map[string]any{"prefix": "x", "count": 3, "kind": "claude-code", "model": "m", "capacity_weight": 2})
	if code != http.StatusCreated {
		t.Fatalf("%d %s", code, body)
	}
	var out struct {
		Bots []struct {
			Bot     bots.Bot
			Connect bots.Connect
		}
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i, c := range out.Bots {
		want := []string{"x-001", "x-002", "x-003"}[i]
		stored, _ := e.st.Get(context.Background(), want)
		if c.Bot.ID != want || stored == nil || stored.TokenHash != auth.Hash(c.Connect.Token) || stored.Weight != 2 {
			t.Fatalf("bot %d: %+v", i, c.Bot)
		}
		if seen[c.Connect.Token] {
			t.Fatal("duplicate token")
		}
		seen[c.Connect.Token] = true
	}
	if code, _ := e.do(t, "POST", "/bots/bulk", map[string]any{"prefix": "y", "count": bots.MaxBulk + 1, "kind": "codex"}); code != 400 {
		t.Fatalf("oversize bulk: %d", code)
	}
}

func TestLifecycle(t *testing.T) {
	e := newEnv(t, 10)
	e.create(t, "a", "claude-code", 1)

	code, body := e.do(t, "POST", "/bots/a/pause", map[string]any{"reason": "cost"})
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	a, _ := e.st.Get(context.Background(), "a")
	if a.Status != model.AgentPaused || a.PauseReason == nil || *a.PauseReason != "cost" || len(e.st.released) != 1 {
		t.Fatalf("after pause: %+v released=%v", a.Agent, e.st.released)
	}
	if code, body := e.do(t, "POST", "/bots/a/spawn", nil); code != 409 {
		t.Fatalf("spawn while paused: %d %s", code, body)
	}

	if code, _ := e.do(t, "POST", "/bots/a/resume", nil); code != 200 {
		t.Fatal("resume")
	}
	a, _ = e.st.Get(context.Background(), "a")
	if a.Status != model.AgentActive || a.PauseReason != nil {
		t.Fatalf("after resume: %+v", a.Agent)
	}

	if code, body := e.do(t, "DELETE", "/bots/a", nil); code != 409 {
		t.Fatalf("delete of active bot: %d %s", code, body)
	}
	if code, _ := e.do(t, "POST", "/bots/a/revoke", map[string]any{"reason": "done"}); code != 200 {
		t.Fatal("revoke")
	}
	for _, p := range []string{"revoke", "pause", "resume", "kill", "spawn"} {
		if code, _ := e.do(t, "POST", "/bots/a/"+p, nil); code != 409 {
			t.Errorf("%s on revoked bot: %d", p, code)
		}
	}
	if code, _ := e.do(t, "DELETE", "/bots/a", nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, body := e.do(t, "POST", "/bots/a/pause", nil); code != 404 || errCode(t, body) != "not_found" {
		t.Fatalf("after delete: %d %s", code, body)
	}

	want := []string{"create_bot", "pause_bot", "resume_bot", "revoke_bot", "delete_bot"}
	got := e.audit.tools()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("audit %v want %v", got, want)
	}
	for _, ev := range e.audit.events {
		if ev.Role != model.RoleOwner {
			t.Fatalf("audit actor %+v", ev)
		}
	}
}

func TestSpawnChecksCapacityAndRotatesToken(t *testing.T) {
	e := newEnv(t, 3)
	old := e.create(t, "a", "codex", 2)
	e.create(t, "b", "codex", 2)

	code, body := e.do(t, "POST", "/bots/a/spawn", map[string]any{"repo": "SigmaUno/foo"})
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if len(e.sp.spawned) != 1 {
		t.Fatal("spawner not called")
	}
	r := e.sp.spawned[0]
	a, _ := e.st.Get(context.Background(), "a")
	if r.AgentID != "a" || r.Kind != model.KindCodex || r.Provider != "openai" || r.Repo != "SigmaUno/foo" ||
		r.JoinLine != "Join Subspace at https://s.example.com as a. Call get_instructions and follow them." ||
		r.MCPURL != "https://s.example.com/mcp" {
		t.Fatalf("request %+v", r)
	}
	if a.TokenHash != auth.Hash(r.Token) || a.TokenHash == auth.Hash(old) || !a.Spawned {
		t.Fatal("spawn must hand the spawner a fresh token and invalidate the old one")
	}
	if bytes.Contains(body, []byte(r.Token)) {
		t.Fatal("spawn response leaks token")
	}

	code, body = e.do(t, "POST", "/bots/b/spawn", nil)
	if code != 409 || errCode(t, body) != "capacity" {
		t.Fatalf("over capacity: %d %s", code, body)
	}
	if len(e.sp.spawned) != 1 {
		t.Fatal("spawner called despite capacity denial")
	}
	if code, _ := e.do(t, "POST", "/bots/ghost/spawn", nil); code != 404 {
		t.Fatalf("unknown bot: %d", code)
	}
}

func TestSpawnFailureKeepsOldToken(t *testing.T) {
	e := newEnv(t, 10)
	old := e.create(t, "a", "claude-code", 1)
	e.sp.fail = spawner.ErrNotImplemented
	code, body := e.do(t, "POST", "/bots/a/spawn", nil)
	if code != 500 || !strings.Contains(string(body), "not implemented") {
		t.Fatalf("%d %s", code, body)
	}
	a, _ := e.st.Get(context.Background(), "a")
	if a.TokenHash != auth.Hash(old) || a.Spawned {
		t.Fatalf("failed spawn changed bot: %+v", a)
	}
	last := e.audit.events[len(e.audit.events)-1]
	if last.Tool != "spawn_bot" || last.Note == nil || !strings.Contains(*last.Note, "spawn failed") {
		t.Fatalf("audit %+v", last)
	}
}

func TestKillPausesAndStops(t *testing.T) {
	e := newEnv(t, 10)
	e.create(t, "a", "claude-code", 1)
	if code, _ := e.do(t, "POST", "/bots/a/spawn", nil); code != 200 {
		t.Fatal("spawn")
	}
	code, body := e.do(t, "POST", "/bots/a/kill", nil)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	a, _ := e.st.Get(context.Background(), "a")
	if a.Status != model.AgentPaused || a.Spawned || len(e.sp.stopped) != 1 || e.sp.stopped[0] != "a" || len(e.st.released) != 1 {
		t.Fatalf("after kill: %+v stopped=%v released=%v", a.Agent, e.sp.stopped, e.st.released)
	}
	got := e.audit.tools()
	if got[len(got)-1] != "kill_bot" {
		t.Fatalf("audit %v", got)
	}
}

func TestConcurrentSpawnsRespectCapacity(t *testing.T) {
	e := newEnv(t, 5)
	const n = 20
	for i := range n {
		e.create(t, fmt.Sprintf("b-%02d", i), "codex", 1)
	}
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.do(t, "POST", fmt.Sprintf("/bots/b-%02d/spawn", i), nil)
		}()
	}
	wg.Wait()
	if len(e.sp.spawned) != 5 {
		t.Fatalf("spawned %d bots with capacity 5", len(e.sp.spawned))
	}
}

func TestToolsThroughRegistry(t *testing.T) {
	e := newEnv(t, 10)
	reg := mcp.NewRegistry0()
	tools := e.svc.Tools([]string{model.RoleLeader})
	for _, tl := range tools {
		if tl.Schema["type"] != "object" || tl.Handler == nil {
			t.Fatalf("tool %s incomplete", tl.Name)
		}
		reg.Register(tl)
	}
	if len(reg.List(model.RoleWorker)) != 0 || len(reg.List(model.RoleLeader)) != len(tools) {
		t.Fatal("tools must be visible only to the roles given")
	}

	call := func(name string, args any) (any, error) {
		tl, ok := reg.Get(name, model.RoleLeader)
		if !ok {
			t.Fatalf("tool %s missing", name)
		}
		raw, _ := json.Marshal(args)
		return tl.Handler(context.Background(), &mcp.Call{Actor: model.Actor{ID: "rex", Role: model.RoleLeader}, Args: raw})
	}
	out, err := call("create_bot", map[string]any{"name": "t-1", "kind": "codex"})
	if err != nil || out.(*bots.Created).Connect.Token == "" {
		t.Fatalf("create_bot: %v %v", out, err)
	}
	if _, err := call("create_bot", map[string]any{"name": "t-1", "kind": "codex"}); model.AsError(err).Code != "conflict" {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := call("pause_bot", map[string]any{"id": "t-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call("revoke_bot", map[string]any{"id": "t-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call("delete_bot", map[string]any{"id": "t-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call("pause_bot", map[string]any{"id": "t-1"}); model.AsError(err).Code != "not_found" {
		t.Fatalf("after delete: %v", err)
	}
	if e.audit.events[0].AgentID != "rex" {
		t.Fatalf("audit must record the calling actor: %+v", e.audit.events[0])
	}
}
