package redteam2

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

const (
	actorOwner  = "owner"
	actorViewer = "viewer"
	actorAnon   = "anon"
)

type agentSpec struct {
	id, role, kind string
}

var agentSpecs = []agentSpec{
	{"lead1", "leader", "other"},
	{"lead2", "leader", "other"},
	{"w1", "worker", "claude-code"},
	{"w2", "worker", "codex"},
	{"tst1", "tester", "opencode"},
	{"rev1", "reviewer", "codex"},
	{"decoy", "worker", "opencode"},
}

type marker struct {
	kind string
	id   string
}

type tool struct {
	name   string
	schema map[string]any
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) snapshot() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Clone(s.b.Bytes())
}

type env struct {
	t      testing.TB
	te     *servertest.Env
	url    string
	dbPath string
	client *http.Client
	logs   *syncBuf
	sse    *syncBuf
	stopSS context.CancelFunc
	sseWG  sync.WaitGroup

	ownerTok string
	tokens   map[string]string
	canaries []string
	needles  []string
	markers  map[string]marker
	tools    map[string][]tool
	allTools []string
	openapi  map[string]any

	collect func(*violation)

	baseGoroutines int
	baseHeap       uint64
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

func randAlnum(n int, alphabet string) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

const (
	alnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	upnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

func newEnv(t testing.TB) *env {
	t.Helper()
	for _, k := range []string{"SUBSPACE_RATE_MCP", "SUBSPACE_RATE_AUTHFAIL", "SUBSPACE_OUTPUT_RATE"} {
		if _, ok := os.LookupEnv(k); !ok {
			_ = os.Setenv(k, "off")
		}
	}
	dir := t.TempDir()
	e := &env{
		t:        t,
		dbPath:   filepath.Join(dir, "rt.db"),
		logs:     &syncBuf{},
		sse:      &syncBuf{},
		ownerTok: "rt2-owner-" + randAlnum(40, alnum),
		tokens:   map[string]string{},
		markers:  map[string]marker{},
		tools:    map[string][]tool{},
		client:   &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 8}},
	}
	log := slog.New(slog.NewJSONHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	tt, ok := t.(*testing.T)
	if !ok {
		t.Fatal("redteam2 needs *testing.T")
	}
	e.te = servertest.Start(tt, server.Config{
		DBPath: e.dbPath, DataDir: dir, OwnerToken: e.ownerTok, PublicURL: "http://sub.test", Log: log,
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute,
	})
	e.url = e.te.TS.URL
	e.setup()
	return e
}

func (e *env) close() {
	if e.stopSS != nil {
		e.stopSS()
	}
	e.te.Stop()
	e.sseWG.Wait()
	e.client.CloseIdleConnections()
}

func (e *env) token(actor string) string {
	switch actor {
	case actorOwner:
		return e.ownerTok
	case actorAnon:
		return ""
	}
	return e.tokens[actor]
}

func (e *env) do(method, path, tok string, body []byte, hdr map[string]string) (int, []byte) {
	req, err := http.NewRequest(method, e.url+path, bytes.NewReader(body))
	if err != nil {
		return 0, []byte(err.Error())
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return 0, []byte(err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	return resp.StatusCode, out
}

func (e *env) owner(method, path string, body any) (int, map[string]any) {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	code, raw := e.do(method, path, e.ownerTok, b, nil)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return code, out
}

func (e *env) mustOwner(method, path string, body any, want int) map[string]any {
	code, out := e.owner(method, path, body)
	if code != want {
		e.t.Fatalf("setup %s %s: %d %v", method, path, code, out)
	}
	return out
}

func (e *env) call(actor, name string, args any) (map[string]any, bool) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args}})
	_, raw := e.do("POST", "/mcp", e.token(actor), b, nil)
	var env struct {
		Result struct {
			SC      map[string]any `json:"structuredContent"`
			IsError bool           `json:"isError"`
		} `json:"result"`
		Error any `json:"error"`
	}
	_ = json.Unmarshal(raw, &env)
	if env.Error != nil {
		return map[string]any{"rpc_error": env.Error}, true
	}
	return env.Result.SC, env.Result.IsError
}

func (e *env) mustCall(actor, name string, args any) map[string]any {
	out, isErr := e.call(actor, name, args)
	if isErr {
		e.t.Fatalf("setup %s %s: %v", actor, name, out)
	}
	return out
}

func (e *env) mark(kind, id string) string {
	m := "zq" + randHex(20)
	e.markers[m] = marker{kind: kind, id: id}
	return m
}

func (e *env) setup() {
	for _, a := range agentSpecs {
		out := e.mustOwner("POST", "/api/v1/agents", map[string]any{"id": a.id, "role": a.role, "kind": a.kind, "model": "m"}, 201)
		e.tokens[a.id] = out["connect"].(map[string]any)["token"].(string)
	}
	out := e.mustOwner("POST", "/api/v1/tokens", map[string]any{"name": "viewer", "scope": "viewer"}, 201)
	e.tokens[actorViewer] = out["secret"].(string)
	out = e.mustOwner("POST", "/api/v1/tokens", map[string]any{"name": "decoy", "scope": "owner"}, 201)
	decoyAPI := out["secret"].(string)

	briefs := []struct {
		id, leader, repo string
	}{{"B-1", "lead1", "o/r"}, {"B-2", "lead2", "o/s"}, {"B-3", "", "o/t"}}
	for _, b := range briefs {
		body := map[string]any{"title": "brief " + b.id, "body": "goal " + e.mark("brief", b.id), "repos": []string{b.repo}, "max_cost_eur": 100}
		if b.leader != "" {
			body["leader_id"] = b.leader
		}
		e.mustOwner("POST", "/api/v1/briefs", body, 201)
	}
	plans := []struct {
		leader, brief, repo string
		roles               []string
	}{{"lead1", "B-1", "o/r", []string{"worker", "worker", "tester"}}, {"lead2", "B-2", "o/s", []string{"worker", "worker"}}, {"lead1", "B-3", "o/t", []string{"worker"}}}
	next := 1
	for i, p := range plans {
		var tasks []any
		for j, role := range p.roles {
			id := fmt.Sprintf("T-%d", next+j)
			tasks = append(tasks, map[string]any{"title": fmt.Sprintf("task %s", id), "spec": "do it " + e.mark("task", id),
				"acceptance_criteria": []string{"works"}, "repo": p.repo, "role": role})
		}
		next += len(p.roles)
		e.mustCall(p.leader, "propose_plan", map[string]any{"brief_id": p.brief, "tasks": tasks})
		e.mustOwner("POST", fmt.Sprintf("/api/v1/plans/P-%d/approve", i+1), map[string]any{}, 200)
	}
	for _, a := range []struct{ leader, task, agent string }{
		{"lead1", "T-1", "w1"}, {"lead1", "T-2", "w2"}, {"lead1", "T-3", "tst1"}, {"lead2", "T-4", "w2"}, {"lead1", "T-6", "decoy"},
	} {
		e.mustCall(a.leader, "assign", map[string]any{"task_id": a.task, "agent_id": a.agent})
	}
	e.mustCall("w1", "claim_task", map[string]any{"task_id": "T-1"})
	e.mustCall("w1", "submit", map[string]any{"task_id": "T-1", "pr_url": "https://github.com/o/r/pull/1", "summary": "done"})
	for i, a := range agentSpecs {
		e.mustOwner("POST", "/api/v1/threads/agent/"+a.id+"/messages", map[string]any{"body": "private " + e.mark("thread", a.id)}, 201)
		e.mustCall(a.id, "log_output", map[string]any{"stream": "stdout", "text": "out " + e.mark("output", a.id)})
		e.mustOwner("PUT", "/api/v1/agents/"+a.id+"/quota", map[string]any{"max_cost_eur_per_day": 500 + i, "max_tasks_per_day": 900 + i}, 200)
	}
	e.mustCall("w2", "ask_owner", map[string]any{"task_id": "T-2", "question": "which? " + e.mark("question", "w2 T-2")})

	e.canaries = []string{
		"ghp_" + randAlnum(36, alnum),
		"sk-ant-api03-" + randAlnum(40, alnum),
		"AKIA" + randAlnum(16, upnum),
		"password=" + randAlnum(24, alnum),
		"Bearer " + randAlnum(40, alnum),
		pem("BEGIN") + "\nMIIE" + randAlnum(60, alnum) + "\n" + pem("END"),
		e.tokens["decoy"],
		decoyAPI,
	}
	for _, c := range e.canaries {
		e.needles = append(e.needles, needle(c))
	}
	e.needles = append(e.needles, e.ownerTok)
	for id, tok := range e.tokens {
		if id != "decoy" {
			e.needles = append(e.needles, tok)
		}
	}
	sort.Strings(e.needles)

	for _, a := range agentSpecs {
		code, raw := e.do("POST", "/mcp", e.tokens[a.id], []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`), nil)
		if code != 200 {
			e.t.Fatalf("tools/list %s: %d", a.id, code)
		}
		var res struct {
			Result struct {
				Tools []struct {
					Name   string         `json:"name"`
					Schema map[string]any `json:"inputSchema"`
				} `json:"tools"`
			} `json:"result"`
		}
		_ = json.Unmarshal(raw, &res)
		for _, tl := range res.Result.Tools {
			e.tools[a.id] = append(e.tools[a.id], tool{name: tl.Name, schema: tl.Schema})
		}
	}
	seen := map[string]bool{}
	for _, a := range agentSpecs {
		for _, tl := range e.tools[a.id] {
			if !seen[tl.name] {
				seen[tl.name] = true
				e.allTools = append(e.allTools, tl.name)
			}
		}
	}
	sort.Strings(e.allTools)
	_, raw := e.do("GET", "/api/v1/openapi.json", e.ownerTok, nil, nil)
	if err := json.Unmarshal(raw, &e.openapi); err != nil {
		e.t.Fatalf("openapi: %v", err)
	}
	e.startSSE()
	e.baseGoroutines, e.baseHeap = usage()
}

func pem(edge string) string {
	return "-----" + edge + " RSA " + "PRIVATE" + " KEY-----"
}

func needle(c string) string {
	switch {
	case strings.HasPrefix(c, "password="):
		return c[len("password=")+4:]
	case strings.HasPrefix(c, "Bearer "):
		return c[len("Bearer ")+4:]
	case strings.HasPrefix(c, "-----BEGIN"):
		i := strings.Index(c, "MIIE")
		return c[i+8 : i+40]
	case strings.HasPrefix(c, "AKIA"):
		return c[4:]
	}
	return c[8:]
}

func (e *env) startSSE() {
	ctx, cancel := context.WithCancel(context.Background())
	e.stopSS = cancel
	for _, topics := range []string{"all", "output"} {
		req, _ := http.NewRequestWithContext(ctx, "GET", e.url+"/api/v1/events?topics="+topics, nil)
		req.Header.Set("Authorization", "Bearer "+e.ownerTok)
		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			e.t.Fatalf("sse: %v", err)
		}
		e.sseWG.Add(1)
		go func() {
			defer e.sseWG.Done()
			defer func() { _ = resp.Body.Close() }()
			sc := bufio.NewScanner(resp.Body)
			sc.Buffer(make([]byte, 1<<20), 16<<20)
			for sc.Scan() {
				_, _ = e.sse.Write(append(sc.Bytes(), '\n'))
			}
		}()
	}
}
