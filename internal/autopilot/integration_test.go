package autopilot_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/autopilot"
	"github.com/SigmaUno/subspace/internal/server"
)

const ownerTok = "owner-token-for-tests-0123456789"

type env struct {
	t  *testing.T
	ts *httptest.Server
}

func (e *env) do(method, path, tok string, body any) (int, map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, e.ts.URL+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (e *env) rest(method, path string, body any) map[string]any {
	e.t.Helper()
	code, out := e.do(method, path, ownerTok, body)
	if code >= 300 {
		e.t.Fatalf("%s %s: %d %v", method, path, code, out)
	}
	return out
}

func (e *env) register(id, role, kind string) string {
	e.t.Helper()
	out := e.rest("POST", "/api/v1/agents", map[string]any{"id": id, "role": role, "kind": kind, "model": "m"})
	return out["connect"].(map[string]any)["token"].(string)
}

func (e *env) call(tok, tool string, args any) map[string]any {
	e.t.Helper()
	_, out := e.do("POST", "/mcp", tok, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args}})
	r, _ := out["result"].(map[string]any)
	if r == nil || r["isError"] == true {
		e.t.Fatalf("%s: %v", tool, out)
	}
	return r["structuredContent"].(map[string]any)
}

func (e *env) waitTasks(what string, ok func(map[string]map[string]any) bool) map[string]map[string]any {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ts := map[string]map[string]any{}
		for _, x := range e.rest("GET", "/api/v1/tasks", nil)["tasks"].([]any) {
			m := x.(map[string]any)
			ts[m["id"].(string)] = m
		}
		if ok(ts) {
			return ts
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s: %v", what, ts)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAutopilotAssignsAndSetsReviewer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv, err := server.New(ctx, server.Config{DBPath: filepath.Join(t.TempDir(), "t.db"), OwnerToken: ownerTok,
		PublicURL: "http://sub.test", FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	go srv.Board.Run(ctx)

	ap := autopilot.New(srv.Board, autopilot.Options{})
	mux := http.NewServeMux()
	mux.Handle("/", srv.Handler())
	mux.Handle("/api/v1/autopilot", http.StripPrefix("/api/v1", autopilot.Handler(ap)))
	mux.Handle("/api/v1/autopilot/", http.StripPrefix("/api/v1", autopilot.Handler(ap)))
	ts := httptest.NewServer(mux)
	defer ts.Close()
	e := &env{t: t, ts: ts}

	lead := e.register("rex", "leader", "other")
	tokens := map[string]string{
		"claude-1": e.register("claude-1", "worker", "claude-code"),
		"claude-2": e.register("claude-2", "worker", "claude-code"),
	}
	e.register("claude-r", "reviewer", "claude-code")
	e.register("codex-r", "reviewer", "codex")

	bid := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "Demo", "body": "b", "repos": []string{"o/r"},
		"max_cost_eur": 10, "max_parallel_agents": 5})["id"].(string)
	plan := e.call(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{
		map[string]any{"title": "One", "spec": "s", "repo": "o/r"},
		map[string]any{"title": "Two", "spec": "s", "repo": "o/r"},
		map[string]any{"title": "Three", "spec": "s", "repo": "o/r"},
	}})["plan"].(map[string]any)

	go ap.Run(ctx, 20*time.Millisecond)
	e.rest("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{})
	time.Sleep(100 * time.Millisecond)
	for _, x := range e.rest("GET", "/api/v1/tasks", nil)["tasks"].([]any) {
		if x.(map[string]any)["status"] != "ready" {
			t.Fatalf("autopilot acted on a disabled brief: %v", x)
		}
	}

	if code, _ := e.do("POST", "/api/v1/autopilot/briefs/B-404", ownerTok, map[string]any{"enabled": true}); code != 404 {
		t.Fatalf("unknown brief: %d", code)
	}
	if out := e.rest("POST", "/api/v1/autopilot/briefs/"+bid, map[string]any{"enabled": true}); out["enabled"] != true {
		t.Fatalf("enable: %v", out)
	}

	tasks := e.waitTasks("all assigned", func(ts map[string]map[string]any) bool {
		for _, x := range ts {
			if x["status"] != "assigned" {
				return false
			}
		}
		return len(ts) == 3
	})
	per := map[string]int{}
	for _, x := range tasks {
		per[x["assignee"].(string)]++
	}
	if per["claude-1"] != 2 || per["claude-2"] != 1 {
		t.Fatalf("unbalanced assignment: %v", per)
	}

	t1 := tasks["T-1"]
	wk := tokens[t1["assignee"].(string)]
	e.call(wk, "claim_task", map[string]any{"task_id": "T-1"})
	e.call(wk, "submit", map[string]any{"task_id": "T-1", "pr_url": "https://git.example/o/r/pulls/1", "summary": "done"})

	e.waitTasks("reviewer set", func(ts map[string]map[string]any) bool {
		return ts["T-1"]["status"] == "in_review" && ts["T-1"]["reviewer"] == "codex-r"
	})

	if n := len(e.rest("GET", "/api/v1/audit?agent_id=autopilot", nil)["events"].([]any)); n != 4 {
		t.Fatalf("want 4 autopilot audit events, got %d", n)
	}
	for _, x := range e.rest("GET", "/api/v1/audit?agent_id=rex", nil)["events"].([]any) {
		if tool := x.(map[string]any)["tool"]; tool != "propose_plan" {
			t.Fatalf("leader did more than propose: %v", x)
		}
	}

	st := e.rest("GET", "/api/v1/autopilot", nil)
	if st["state"] != "running" || len(st["last_actions"].([]any)) < 4 {
		t.Fatalf("status: %v", st)
	}
	if bs := st["briefs"].([]any); len(bs) != 1 || bs[0].(map[string]any)["enabled"] != true {
		t.Fatalf("brief flags: %v", bs)
	}

	e.rest("POST", "/api/v1/pause-all", map[string]any{"reason": "stop"})
	if st := e.rest("GET", "/api/v1/autopilot", nil); st["state"] != "paused" {
		t.Fatalf("state while paused: %v", st["state"])
	}
}

func TestAutopilotLeavesLedBriefsToTheirLeader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv, err := server.New(ctx, server.Config{DBPath: filepath.Join(t.TempDir(), "t.db"), OwnerToken: ownerTok,
		PublicURL: "http://sub.test", FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	go srv.Board.Run(ctx)
	ap := autopilot.New(srv.Board, autopilot.Options{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	e := &env{t: t, ts: ts}

	lead := e.register("rex", "leader", "other")
	e.register("claude-1", "worker", "claude-code")
	bid := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "Led", "repos": []string{"o/r"}, "leader_id": "rex"})["id"].(string)
	plan := e.call(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{
		map[string]any{"title": "One", "spec": "s", "repo": "o/r"},
	}})["plan"].(map[string]any)
	e.rest("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{})
	ap.SetEnabled(bid, true)

	res, err := ap.Tick(ctx)
	if err != nil || len(res) != 0 {
		t.Fatalf("autopilot acted on a brief led by rex: %v %v", res, err)
	}
	e.rest("POST", "/api/v1/briefs/"+bid+"/leader", map[string]any{"agent_id": nil})
	if res, err = ap.Tick(ctx); err != nil || len(res) != 1 || res[0].Error != "" {
		t.Fatalf("autopilot on a shared brief: %v %v", res, err)
	}
	e.waitTasks("assigned", func(ts map[string]map[string]any) bool { return ts["T-1"]["status"] == "assigned" })
}
