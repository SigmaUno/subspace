package leakguard_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type fixture struct {
	*servertest.Env
	dbPath string
	logs   *syncBuffer
	sub    events.Subscription
	mu     sync.Mutex
	feed   bytes.Buffer
	lead   string
	brief  string
	n      int
	leaked []string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	logs := &syncBuffer{}
	db := servertest.DB(t)
	e := servertest.Start(t, server.Config{PublicURL: "http://sub.test", DBPath: db,
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute,
		Log: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	f := &fixture{Env: e, dbPath: db, logs: logs}
	f.sub = e.Srv.Hub.Subscribe([]string{"all"})
	go func() {
		for ev := range f.sub.C() {
			b, _ := json.Marshal(ev)
			f.mu.Lock()
			f.feed.Write(b)
			f.mu.Unlock()
		}
	}()
	t.Cleanup(f.sub.Close)
	f.lead = e.Register("rex", "leader", "other")
	code, out := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B", "body": "b", "repos": []string{"o/r"}})
	if code != 201 {
		t.Fatalf("brief: %d %v", code, out)
	}
	f.brief = out["id"].(string)
	return f
}

func (f *fixture) agent(role, kind string) (id, tok string) {
	f.n++
	id = fmt.Sprintf("a%d", f.n)
	return id, f.Register(id, role, kind)
}

func (f *fixture) victim() (id, tok string) {
	id, tok = f.agent("worker", "claude-code")
	f.leaked = append(f.leaked, tok)
	return
}

func (f *fixture) task(assignee, role string) string {
	f.T.Helper()
	f.n++
	plan := f.Must(f.lead, "propose_plan", map[string]any{"brief_id": f.brief, "tasks": []any{
		map[string]any{"title": fmt.Sprintf("t%d", f.n), "spec": "s", "repo": "o/r", "role": role, "acceptance_criteria": []string{"ok"}},
	}})["plan"].(map[string]any)
	if code, out := f.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		f.T.Fatalf("approve: %d %v", code, out)
	}
	id := plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
	f.Must(f.lead, "assign", map[string]any{"task_id": id, "agent_id": assignee})
	return id
}

func (f *fixture) claimed(role, kind string) (tok, task string) {
	id, tok := f.agent(role, kind)
	task = f.task(id, role)
	f.Must(tok, "claim_task", map[string]any{"task_id": task})
	return
}

func (f *fixture) status(agent string) string {
	f.T.Helper()
	code, out := f.REST("GET", "/api/v1/agents/"+agent, nil)
	if code != 200 {
		f.T.Fatalf("agent %s: %d %v", agent, code, out)
	}
	return out["status"].(string)
}

func (f *fixture) mcpCode(tok string) int {
	f.T.Helper()
	req, _ := http.NewRequest("POST", f.TS.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.T.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func (f *fixture) list(path, key string) []map[string]any {
	f.T.Helper()
	code, out := f.REST("GET", path, nil)
	if code != 200 {
		f.T.Fatalf("%s: %d %v", path, code, out)
	}
	var res []map[string]any
	for _, x := range out[key].([]any) {
		res = append(res, x.(map[string]any))
	}
	return res
}

func (f *fixture) leakDecisions() []map[string]any {
	var out []map[string]any
	for _, d := range f.list("/api/v1/decisions", "decisions") {
		if strings.HasPrefix(d["id"].(string), "leak:") {
			out = append(out, d)
		}
	}
	return out
}

func (f *fixture) auditOf(tool string) []map[string]any {
	var out []map[string]any
	for _, e := range f.list("/api/v1/audit?limit=1000", "events") {
		if e["tool"] == tool {
			out = append(out, e)
		}
	}
	return out
}

func (f *fixture) expectRevoked(victim, tok, channel string, others ...string) {
	f.T.Helper()
	if s := f.status(victim); s != "revoked" {
		f.T.Fatalf("%s is %s after the leak in %s", victim, s, channel)
	}
	if c := f.mcpCode(tok); c != 401 {
		f.T.Fatalf("leaked token of %s still works: %d", victim, c)
	}
	for _, o := range others {
		if s := f.status(o); s != "active" {
			f.T.Fatalf("%s is %s, only %s should be revoked", o, s, victim)
		}
	}
	found := false
	for _, e := range f.auditOf("token_leak_detected") {
		after := e["after"].(map[string]any)
		if e["before"].(map[string]any)["agent"] == victim {
			found = true
			if after["channel"] != channel || after["identity"] != "agent" || e["agent_id"] != "system" {
				f.T.Fatalf("audit of %s: %v", victim, e)
			}
		}
	}
	if !found {
		f.T.Fatalf("no token_leak_detected audit event for %s", victim)
	}
	var notice bool
	for _, m := range f.list("/api/v1/threads/agent/"+victim+"/messages", "messages") {
		if m["kind"] == "system" && strings.Contains(m["body"].(string), "revoked") {
			notice = true
		}
	}
	if !notice {
		f.T.Fatalf("no system message in the thread of %s", victim)
	}
	var decided bool
	for _, d := range f.leakDecisions() {
		if d["agent_id"] == victim && d["kind"] == "token_leak" {
			decided = true
		}
	}
	if !decided {
		f.T.Fatalf("no owner decision for %s: %v", victim, f.leakDecisions())
	}
}

func (f *fixture) nothingRevoked(ids ...string) {
	f.T.Helper()
	for _, id := range ids {
		if s := f.status(id); s != "active" {
			f.T.Fatalf("%s is %s", id, s)
		}
	}
	if d := f.leakDecisions(); len(d) != 0 {
		f.T.Fatalf("unexpected leak decisions: %v", d)
	}
	if a := f.auditOf("token_leak_detected"); len(a) != 0 {
		f.T.Fatalf("unexpected leak audit events: %v", a)
	}
}

func TestLeakInEveryChannelRevokesOnlyThatIdentity(t *testing.T) {
	f := setup(t)
	_, bystander := f.agent("worker", "claude-code")
	bystanderID := "a1"
	_ = bystander

	cases := []struct {
		channel string
		leak    func(victim string)
	}{
		{"message", func(v string) {
			tok, _ := f.claimed("worker", "claude-code")
			f.Must(tok, "post_message", map[string]any{"thread": "agent:me", "body": "found this in env: " + v})
		}},
		{"note", func(v string) {
			tok, task := f.claimed("worker", "claude-code")
			f.Must(tok, "add_note", map[string]any{"task_id": task, "body": "token=" + v + " works"})
		}},
		{"question", func(v string) {
			tok, _ := f.claimed("worker", "claude-code")
			f.Must(tok, "ask_owner", map[string]any{"question": "may I use " + v + "?"})
		}},
		{"question", func(v string) {
			tok, _ := f.claimed("worker", "claude-code")
			f.Must(tok, "ask_owner", map[string]any{"question": "which one?", "options": []string{"a", "x" + v}})
		}},
		{"status_note", func(v string) {
			tok, task := f.claimed("worker", "claude-code")
			f.Must(tok, "update_status", map[string]any{"task_id": task, "status": "in_progress", "note": "using " + v})
		}},
		{"submit_summary", func(v string) {
			tok, task := f.claimed("worker", "claude-code")
			f.Must(tok, "submit", map[string]any{"task_id": task, "pr_url": "https://git.example/o/r/pull/1", "summary": "done, key " + v})
		}},
		{"pr_text", func(v string) {
			tok, task := f.claimed("worker", "claude-code")
			f.Must(tok, "submit", map[string]any{"task_id": task, "pr_url": "https://git.example/o/r/pull/2?k=" + v, "summary": "done"})
		}},
		{"test_log", func(v string) {
			tok, task := f.claimed("tester", "claude-code")
			f.Must(tok, "report_tests", map[string]any{"task_id": task, "passed": 1, "failed": 0, "log_excerpt": "FAIL auth " + v})
		}},
		{"review", func(v string) {
			wtok, task := f.claimed("worker", "claude-code")
			f.Must(wtok, "submit", map[string]any{"task_id": task, "pr_url": "https://git.example/o/r/pull/3", "summary": "done"})
			rid, rtok := f.agent("reviewer", "codex")
			f.Must(f.lead, "assign", map[string]any{"task_id": task, "agent_id": rid, "as": "reviewer"})
			f.Must(rtok, "review", map[string]any{"task_id": task, "verdict": "changes_requested", "comments": "do not commit " + v})
		}},
		{"status_note", func(v string) {
			tok, task := f.claimed("worker", "claude-code")
			f.Must(tok, "report_blocker", map[string]any{"task_id": task, "reason": "login fails with " + v, "needs": "leader"})
		}},
		{"help_request", func(v string) {
			tok, task := f.claimed("worker", "claude-code")
			f.Must(tok, "request_help", map[string]any{"task_id": task, "kind": "missing_access", "details": "is " + v + " the right key?"})
		}},
		{"help_request", func(v string) {
			tok, task := f.claimed("worker", "claude-code")
			f.Must(tok, "suggest_split", map[string]any{"task_id": task, "proposed_tasks": []any{
				map[string]any{"title": "part one", "spec": "do the first half", "acceptance_criteria": []string{"uses " + v}},
				map[string]any{"title": "part two", "spec": "do the second half"},
			}})
		}},
		{"log_output", func(v string) {
			tok, _ := f.claimed("worker", "claude-code")
			f.Must(tok, "log_output", map[string]any{"stream": "stdout", "text": "$ env\nTOKEN_X=" + v + "\n"})
		}},
	}
	for _, c := range cases {
		t.Run(c.channel, func(t *testing.T) {
			f.T = t
			before := len(f.leakDecisions())
			id, tok := f.victim()
			c.leak(tok)
			f.expectRevoked(id, tok, c.channel, bystanderID)
			if n := len(f.leakDecisions()); n != before+1 {
				t.Fatalf("leak decisions %d, want %d", n, before+1)
			}
		})
	}

	f.T = t
	time.Sleep(300 * time.Millisecond)
	f.mu.Lock()
	feed := f.feed.String()
	f.mu.Unlock()
	if n := strings.Count(feed, `"token_leak:L-`); n != len(cases) {
		t.Fatalf("%d token_leak notifications, want %d", n, len(cases))
	}
	f.noTrace(t, feed)
}

func (f *fixture) noTrace(t *testing.T, feed string) {
	t.Helper()
	var texts []string
	texts = append(texts, feed, f.logs.String())
	if !strings.HasPrefix(f.dbPath, "postgres") {
		for _, p := range []string{f.dbPath, f.dbPath + "-wal"} {
			b, err := os.ReadFile(p)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			texts = append(texts, string(b))
		}
	}
	for _, path := range []string{"/api/v1/audit?limit=1000", "/api/v1/activity?limit=200", "/api/v1/decisions", "/api/v1/agent-output", "/api/v1/questions"} {
		_, out := f.REST("GET", path, nil)
		b, _ := json.Marshal(out)
		texts = append(texts, string(b))
	}
	for _, a := range f.list("/api/v1/agents", "agents") {
		id := a["id"].(string)
		b, _ := json.Marshal(f.list("/api/v1/threads/agent/"+id+"/messages", "messages"))
		o, _ := json.Marshal(f.list("/api/v1/agents/"+id+"/output", "lines"))
		texts = append(texts, string(b), string(o))
	}
	for _, tk := range f.list("/api/v1/tasks", "tasks") {
		id := tk["id"].(string)
		b, _ := json.Marshal(tk)
		m, _ := json.Marshal(f.list("/api/v1/threads/task/"+id+"/messages", "messages"))
		texts = append(texts, string(b), string(m))
	}
	for _, secret := range f.leaked {
		for i, text := range texts {
			if strings.Contains(text, secret) {
				t.Fatalf("leaked token found in source %d", i)
			}
			if strings.Contains(text, secret[4:]) {
				t.Fatalf("leaked token body found in source %d", i)
			}
		}
	}
}

func TestNearMissesRevokeNothing(t *testing.T) {
	f := setup(t)
	vid, vtok := f.agent("worker", "claude-code")
	_, other := f.agent("worker", "claude-code")
	oid := "a2"
	wtok, task := f.claimed("worker", "claude-code")
	code, out := f.REST("POST", "/api/v1/tokens", map[string]any{"name": "ci", "scope": "owner"})
	if code != 201 {
		t.Fatalf("token: %d %v", code, out)
	}
	secret := out["secret"].(string)

	flip := func(s string) string {
		b := []byte(s)
		i := len(b) - 10
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
		return string(b)
	}
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	unknown := "ssp_" + base64.RawURLEncoding.EncodeToString(raw)
	near := []string{
		flip(vtok),
		flip(secret),
		vtok[:len(vtok)-1],
		secret[:len(secret)-1],
		"ssx_" + vtok[4:],
		"ssp-" + vtok[4:],
		"SSP_" + vtok[4:],
		unknown,
		"ssp_" + vtok[4:20],
		"ssp_ ssu_ ssp_ssu_ ssp_",
		strings.Repeat("ssp_", 3000),
	}
	for _, n := range near {
		f.Must(wtok, "add_note", map[string]any{"task_id": task, "body": "see " + n + " there"})
		f.Must(wtok, "log_output", map[string]any{"stream": "stdout", "text": n})
	}
	f.nothingRevoked(vid, oid)
	if c := f.mcpCode(vtok); c != 200 {
		t.Fatalf("victim token no longer works: %d", c)
	}
	if c := f.mcpCode(other); c != 200 {
		t.Fatalf("other token no longer works: %d", c)
	}
	if code, _ := f.RESTAs(secret, "GET", "/api/v1/me", nil); code != 200 {
		t.Fatalf("api token no longer works: %d", code)
	}
}

func TestSeveralTokensInOneTextAreAllRevokedAndMasked(t *testing.T) {
	f := setup(t)
	_, bystander := f.agent("worker", "claude-code")
	v1, t1 := f.victim()
	v2, t2 := f.victim()
	tok, task := f.claimed("worker", "claude-code")
	f.Must(tok, "add_note", map[string]any{"task_id": task, "body": fmt.Sprintf("%s %s %s %s", t1, t2, t1, "ssp_notatoken")})
	f.expectRevoked(v1, t1, "note", "a1")
	f.expectRevoked(v2, t2, "note", "a1")
	if n := len(f.leakDecisions()); n != 2 {
		t.Fatalf("leak decisions: %d", n)
	}
	if n := len(f.auditOf("token_leak_detected")); n != 2 {
		t.Fatalf("audit events: %d", n)
	}
	if c := f.mcpCode(bystander); c != 200 {
		t.Fatalf("bystander: %d", c)
	}
	f.noTrace(t, "")
}

func TestAgentLeakingItsOwnTokenIsRevoked(t *testing.T) {
	f := setup(t)
	id, tok := f.agent("worker", "claude-code")
	f.leaked = append(f.leaked, tok)
	f.Must(tok, "post_message", map[string]any{"thread": "agent:me", "body": "my token is " + tok})
	f.expectRevoked(id, tok, "message")
	f.noTrace(t, "")
}

func TestAgentTokenLeakReissue(t *testing.T) {
	f := setup(t)
	id, tok := f.victim()
	wtok, task := f.claimed("worker", "claude-code")
	f.Must(wtok, "add_note", map[string]any{"task_id": task, "body": tok})
	d := f.leakDecisions()
	if len(d) != 1 {
		t.Fatalf("decisions: %v", d)
	}
	leak := strings.TrimPrefix(d[0]["id"].(string), "leak:")

	code, out := f.REST("POST", "/api/v1/token-leaks/"+leak+"/reissue", map[string]any{})
	if code != 200 || out["kind"] != "agent" {
		t.Fatalf("reissue: %d %v", code, out)
	}
	fresh := out["connect"].(map[string]any)["token"].(string)
	if fresh == tok || out["agent"].(map[string]any)["status"] != "active" {
		t.Fatalf("reissue result: %v", out)
	}
	if c := f.mcpCode(tok); c != 401 {
		t.Fatalf("leaked token works again: %d", c)
	}
	if c := f.mcpCode(fresh); c != 200 {
		t.Fatalf("new token: %d", c)
	}
	if f.status(id) != "active" || len(f.leakDecisions()) != 0 {
		t.Fatal("decision not resolved")
	}
	if code, out := f.REST("POST", "/api/v1/token-leaks/"+leak+"/reissue", map[string]any{}); code != 409 {
		t.Fatalf("second reissue: %d %v", code, out)
	}
	if code, out := f.REST("POST", "/api/v1/token-leaks/L-999/reissue", map[string]any{}); code != 404 {
		t.Fatalf("unknown leak: %d %v", code, out)
	}
	if n := len(f.auditOf("reissue_leaked_token")); n != 1 {
		t.Fatalf("reissue audit events: %d", n)
	}
	f.noTrace(t, "")
}

func TestOwnerAPITokenLeak(t *testing.T) {
	f := setup(t)
	create := func(name string) (string, string) {
		code, out := f.REST("POST", "/api/v1/tokens", map[string]any{"name": name, "scope": "owner"})
		if code != 201 {
			t.Fatalf("token: %d %v", code, out)
		}
		return out["token"].(map[string]any)["id"].(string), out["secret"].(string)
	}
	id, secret := create("leaky")
	_, kept := create("kept")
	f.leaked = append(f.leaked, secret)
	wid, wtok := f.agent("worker", "claude-code")
	tid := f.task(wid, "worker")
	f.Must(wtok, "claim_task", map[string]any{"task_id": tid})
	f.Must(wtok, "add_note", map[string]any{"task_id": tid, "body": "owner key: " + secret})

	if code, _ := f.RESTAs(secret, "GET", "/api/v1/me", nil); code != 401 {
		t.Fatalf("leaked api token still works: %d", code)
	}
	if code, _ := f.RESTAs(kept, "GET", "/api/v1/me", nil); code != 200 {
		t.Fatalf("other api token: %d", code)
	}
	if code, _ := f.REST("GET", "/api/v1/me", nil); code != 200 {
		t.Fatalf("root token: %d", code)
	}
	if f.status(wid) != "active" {
		t.Fatal("the agent that wrote the text must stay active")
	}
	audit := f.auditOf("token_leak_detected")
	if len(audit) != 1 || audit[0]["after"].(map[string]any)["token_id"] != id || audit[0]["after"].(map[string]any)["identity"] != "api_token" {
		t.Fatalf("audit: %v", audit)
	}
	var notice bool
	for _, m := range f.list("/api/v1/threads/agent/"+wid+"/messages", "messages") {
		notice = notice || (m["kind"] == "system" && strings.Contains(m["body"].(string), id))
	}
	if !notice {
		t.Fatal("the writer was not told")
	}
	d := f.leakDecisions()
	if len(d) != 1 {
		t.Fatalf("decisions: %v", d)
	}

	code, out := f.REST("POST", "/api/v1/token-leaks/"+strings.TrimPrefix(d[0]["id"].(string), "leak:")+"/reissue", map[string]any{})
	if code != 200 || out["kind"] != "api_token" {
		t.Fatalf("reissue: %d %v", code, out)
	}
	fresh := out["secret"].(string)
	if fresh == secret || out["token"].(map[string]any)["name"] != "leaky" || out["token"].(map[string]any)["scope"] != "owner" {
		t.Fatalf("reissue result: %v", out)
	}
	if code, _ := f.RESTAs(fresh, "GET", "/api/v1/me", nil); code != 200 {
		t.Fatalf("new api token: %d", code)
	}
	if len(f.leakDecisions()) != 0 {
		t.Fatal("decision not resolved")
	}
	f.noTrace(t, "")
}

func TestViewerTokenCannotReissue(t *testing.T) {
	f := setup(t)
	_, tok := f.victim()
	wtok, task := f.claimed("worker", "claude-code")
	f.Must(wtok, "add_note", map[string]any{"task_id": task, "body": tok})
	leak := strings.TrimPrefix(f.leakDecisions()[0]["id"].(string), "leak:")
	code, out := f.REST("POST", "/api/v1/tokens", map[string]any{"name": "v", "scope": "viewer"})
	if code != 201 {
		t.Fatalf("token: %d %v", code, out)
	}
	if code, _ := f.RESTAs(out["secret"].(string), "POST", "/api/v1/token-leaks/"+leak+"/reissue", map[string]any{}); code != 403 {
		t.Fatalf("viewer reissue: %d", code)
	}
	if code, _ := f.RESTAs(wtok, "POST", "/api/v1/token-leaks/"+leak+"/reissue", map[string]any{}); code != 403 && code != 401 {
		t.Fatalf("agent reissue: %d", code)
	}
}
