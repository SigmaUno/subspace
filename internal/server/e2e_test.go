package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const ownerTok = "owner-token-for-tests-0123456789"

type env struct {
	t   *testing.T
	ts  *httptest.Server
	srv *Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s, err := New(ctx, Config{DBPath: filepath.Join(t.TempDir(), "t.db"), OwnerToken: ownerTok, PublicURL: "http://sub.test",
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	go s.Board.Run(ctx)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); cancel(); _ = s.Close() })
	return &env{t: t, ts: ts, srv: s}
}

func (e *env) rest(method, path string, body any) (int, map[string]any) {
	e.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+ownerTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (e *env) register(id, role, kind string) string {
	e.t.Helper()
	code, out := e.rest("POST", "/api/v1/agents", map[string]any{"id": id, "role": role, "kind": kind, "model": "m"})
	if code != 201 {
		e.t.Fatalf("register %s: %d %v", id, code, out)
	}
	return out["connect"].(map[string]any)["token"].(string)
}

// call performs an MCP tools/call; returns (structured result, isError).
func (e *env) call(tok, tool string, args any) (map[string]any, bool) {
	e.t.Helper()
	res := e.rpc(tok, "tools/call", map[string]any{"name": tool, "arguments": args})
	if res["error"] != nil {
		return map[string]any{"rpc_error": res["error"]}, true
	}
	r := res["result"].(map[string]any)
	sc, _ := r["structuredContent"].(map[string]any)
	return sc, r["isError"] == true
}

func (e *env) rpc(tok, method string, params any) map[string]any {
	e.t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, _ := http.NewRequest("POST", e.ts.URL+"/mcp", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func (e *env) must(tok, tool string, args any) map[string]any {
	e.t.Helper()
	out, isErr := e.call(tok, tool, args)
	if isErr {
		e.t.Fatalf("%s failed: %v", tool, out)
	}
	return out
}

func (e *env) mustFail(tok, tool string, args any, code string) {
	e.t.Helper()
	out, isErr := e.call(tok, tool, args)
	if !isErr {
		e.t.Fatalf("%s should fail with %s, got %v", tool, code, out)
	}
	if _, rpcErr := out["rpc_error"]; rpcErr && code == "" {
		return
	}
	if got, _ := out["error"].(map[string]any)["code"].(string); got != code {
		e.t.Fatalf("%s: code %q want %q (%v)", tool, got, code, out)
	}
}

func status(m map[string]any) string { return m["task"].(map[string]any)["status"].(string) }

func TestEndToEnd(t *testing.T) {
	e := newEnv(t)

	// bootstrap: empty server works
	if code, out := e.rest("GET", "/api/v1/state", nil); code != 200 || out["paused"] != false {
		t.Fatalf("state: %d %v", code, out)
	}
	if code, _ := e.rest("GET", "/api/v1/agents", nil); code != 200 {
		t.Fatal("agents")
	}

	lead := e.register("rex", "leader", "other")
	wk := e.register("claude-1", "worker", "claude-code")
	rev := e.register("codex-r", "reviewer", "codex")

	_, br := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "Demo", "body": "do it", "repos": []string{"o/r"}, "max_cost_eur": 10, "max_parallel_agents": 5})
	bid := br["id"].(string)

	// role-filtered tools
	tools := func(tok string) map[string]bool {
		res := e.rpc(tok, "tools/list", nil)["result"].(map[string]any)["tools"].([]any)
		m := map[string]bool{}
		for _, x := range res {
			m[x.(map[string]any)["name"].(string)] = true
		}
		return m
	}
	if tw := tools(wk); !tw["claim_task"] || tw["assign"] || tw["review"] {
		t.Fatalf("worker tools wrong: %v", tw)
	}
	e.mustFail(wk, "assign", map[string]any{"task_id": "T-1", "agent_id": "x"}, "")

	// leader proposes; owner approves
	plan := e.must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{
		map[string]any{"ref": "a", "title": "Add login", "spec": "build it", "repo": "o/r", "acceptance_criteria": []string{"works"}},
		map[string]any{"ref": "b", "title": "Docs", "spec": "docs", "repo": "o/r", "depends_on": []string{"a"}},
	}})["plan"].(map[string]any)
	pid := plan["id"].(string)
	tasks := plan["tasks"].([]any)
	t1 := tasks[0].(map[string]any)["id"].(string)
	t2 := tasks[1].(map[string]any)["id"].(string)
	if !strings.HasPrefix(tasks[0].(map[string]any)["branch"].(string), "sub/t-1-add-login") {
		t.Fatalf("branch: %v", tasks[0])
	}
	e.mustFail(lead, "assign", map[string]any{"task_id": t1, "agent_id": "claude-1"}, "conflict") // still backlog
	if code, _ := e.rest("POST", "/api/v1/plans/"+pid+"/approve", map[string]any{}); code != 200 {
		t.Fatal("approve")
	}
	e.must(lead, "assign", map[string]any{"task_id": t1, "agent_id": "claude-1"})
	e.mustFail(wk, "claim_task", map[string]any{"task_id": t2}, "not_assigned")

	// worker flow
	claimed := e.must(wk, "claim_task", map[string]any{"task_id": t1})
	if status(claimed) != "in_progress" || claimed["lease_expires_at"] == nil {
		t.Fatalf("claim: %v", claimed)
	}
	hb := e.must(wk, "heartbeat", nil)
	if hb["ok"] != true {
		t.Fatal("heartbeat")
	}
	e.must(wk, "update_status", map[string]any{"task_id": t1, "status": "in_progress", "progress_pct": 50, "note": "half", "cost_eur": 0.5})
	e.mustFail(wk, "submit", map[string]any{"task_id": t1, "summary": "x"}, "bad_request") // needs pr_url
	sub := e.must(wk, "submit", map[string]any{"task_id": t1, "pr_url": "https://git.example/o/r/pulls/1", "summary": "done"})
	if status(sub) != "submitted" {
		t.Fatal(sub)
	}

	// reviewer (different provider) reviews; worker cannot
	if lr := e.must(rev, "list_reviews", nil)["tasks"].([]any); len(lr) != 1 {
		t.Fatalf("list_reviews: %v", lr)
	}
	e.mustFail(rev, "review", map[string]any{"task_id": t1, "verdict": "changes_requested", "comments": ""}, "bad_request")
	if r := e.must(rev, "review", map[string]any{"task_id": t1, "verdict": "approve", "comments": "lgtm"}); status(r) != "approved" {
		t.Fatal(r)
	}
	// dependency blocks t2 until owner marks t1 done
	e.must(lead, "assign", map[string]any{"task_id": t2, "agent_id": "claude-1"})
	e.mustFail(wk, "claim_task", map[string]any{"task_id": t2}, "conflict")
	if code, _ := e.rest("POST", "/api/v1/tasks/"+t1+"/done", map[string]any{"note": "merged"}); code != 200 {
		t.Fatal("done")
	}
	e.must(wk, "claim_task", map[string]any{"task_id": t2})

	// thread + audit
	if code, out := e.rest("GET", "/api/v1/threads/task/"+t1+"/messages", nil); code != 200 || len(out["messages"].([]any)) < 5 {
		t.Fatalf("thread: %d %v", code, out)
	}
	if _, out := e.rest("GET", "/api/v1/audit?task_id="+t1, nil); len(out["events"].([]any)) < 4 {
		t.Fatalf("audit: %v", out)
	}

	// ask_owner → answer
	q := e.must(wk, "ask_owner", map[string]any{"task_id": t2, "question": "which db?", "options": []string{"a", "b"}})["question"].(map[string]any)
	if code, _ := e.rest("POST", "/api/v1/questions/"+q["id"].(string)+"/answer", map[string]any{"answer": "a"}); code != 200 {
		t.Fatal("answer")
	}
	ms := e.must(wk, "read_thread", map[string]any{"thread": "task:" + t2})["messages"].([]any)
	if last := ms[len(ms)-1].(map[string]any); last["kind"] != "answer" {
		t.Fatalf("answer not in thread: %v", ms)
	}
	// thread scoping: reviewer cannot read t2's thread
	e.mustFail(rev, "read_thread", map[string]any{"thread": "task:" + t2}, "not_found")

	// pause_all revokes leases and blocks claims
	e.rest("POST", "/api/v1/pause-all", map[string]any{"reason": "test"})
	_, tk := e.rest("GET", "/api/v1/tasks/"+t2, nil)
	if tk["status"] != "assigned" {
		t.Fatalf("pause should release lease: %v", tk["status"])
	}
	e.mustFail(wk, "claim_task", map[string]any{"task_id": t2}, "paused")
	e.rest("POST", "/api/v1/resume-all", nil)
	e.must(wk, "claim_task", map[string]any{"task_id": t2})

	// auth boundaries
	if code, _ := e.rest("GET", "/api/v1/state", nil); code != 200 {
		t.Fatal()
	}
	req, _ := http.NewRequest("GET", e.ts.URL+"/api/v1/state", nil)
	req.Header.Set("Authorization", "Bearer "+wk)
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 403 {
		t.Fatalf("agent on REST: %d", r.StatusCode)
	}
	req, _ = http.NewRequest("POST", e.ts.URL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+ownerTok)
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 403 {
		t.Fatalf("owner on /mcp: %d", r.StatusCode)
	}
}

func TestLoopProtectionPausesAgent(t *testing.T) {
	e := newEnv(t)
	wk := e.register("w1", "worker", "claude-code")
	for i := 0; i < 5; i++ {
		e.mustFail(wk, "add_note", map[string]any{"task_id": "T-99", "body": "x"}, "not_found")
	}
	e.mustFail(wk, "add_note", map[string]any{"task_id": "T-99", "body": "x"}, "agent_paused")
	_, a := e.rest("GET", "/api/v1/agents/w1", nil)
	if a["status"] != "paused" {
		t.Fatalf("agent not paused: %v", a)
	}
	e.mustFail(wk, "add_note", map[string]any{"body": "other"}, "agent_paused")
	e.rest("POST", "/api/v1/agents/w1/resume", nil)
	e.must(wk, "add_note", map[string]any{"body": "fine"})
}

func TestLeaseExpiryAndRevoke(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := New(ctx, Config{DBPath: filepath.Join(t.TempDir(), "t.db"), OwnerToken: ownerTok, Lease: 150 * time.Millisecond,
		FlushEvery: 20 * time.Millisecond, SweepEvery: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	go s.Board.Run(ctx)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	e := &env{t: t, ts: ts, srv: s}
	lead := e.register("rex", "leader", "other")
	wk := e.register("w1", "worker", "claude-code")
	_, br := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "B"})
	bid := br["id"].(string)
	e.must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{map[string]any{"title": "t", "spec": "s", "repo": "o/r"}}})
	e.rest("POST", "/api/v1/plans/P-1/approve", nil)
	e.must(lead, "assign", map[string]any{"task_id": "T-1", "agent_id": "w1"})
	e.must(wk, "claim_task", map[string]any{"task_id": "T-1"})
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, tk := e.rest("GET", "/api/v1/tasks/T-1", nil)
		if tk["status"] == "assigned" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lease did not expire: %v", tk["status"])
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.rest("POST", "/api/v1/agents/w1/revoke", map[string]any{})
	req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Authorization", "Bearer "+wk)
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 401 {
		t.Fatalf("revoked token should be 401, got %d", r.StatusCode)
	}
}

func TestSSEReceivesTaskEvents(t *testing.T) {
	e := newEnv(t)
	req, _ := http.NewRequest("GET", e.ts.URL+"/api/v1/events?topics=board", nil)
	req.Header.Set("Authorization", "Bearer "+ownerTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	lines := make(chan string, 100)
	go func() {
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	wait := func(sub string) {
		t.Helper()
		timeout := time.After(3 * time.Second)
		for {
			select {
			case l := <-lines:
				if strings.Contains(l, sub) {
					return
				}
			case <-timeout:
				t.Fatalf("no %q event", sub)
			}
		}
	}
	wait("event: hello")
	lead := e.register("rex", "leader", "other")
	_, br := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "B"})
	e.must(lead, "propose_plan", map[string]any{"brief_id": br["id"], "tasks": []any{map[string]any{"title": "t", "spec": "s", "repo": "o/r"}}})
	wait("event: task.created")
}
