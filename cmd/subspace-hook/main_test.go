package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/servertest"
)

type call struct {
	tool string
	args map[string]any
}

type tap struct {
	mu    sync.Mutex
	calls []call
	url   string
}

func newTap(t *testing.T, target string) *tap {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	tp := &tap{}
	proxy := httputil.NewSingleHostReverseProxy(u)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var rpc struct {
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal(body, &rpc) == nil && rpc.Method == "tools/call" {
			tp.mu.Lock()
			tp.calls = append(tp.calls, call{rpc.Params.Name, rpc.Params.Arguments})
			tp.mu.Unlock()
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	tp.url = ts.URL
	return tp
}

func (tp *tap) tools() []string {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	var out []string
	for _, c := range tp.calls {
		out = append(out, c.tool)
	}
	return out
}

func (tp *tap) last() call {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.calls[len(tp.calls)-1]
}

func (tp *tap) count() int {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return len(tp.calls)
}

type clock struct{ ns atomic.Int64 }

func newClock() *clock {
	c := &clock{}
	c.ns.Store(time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC).UnixNano())
	return c
}

func (c *clock) now() time.Time { return time.Unix(0, c.ns.Load()).UTC() }

func (c *clock) advance(d time.Duration) { c.ns.Add(int64(d)) }

type rig struct {
	t      *testing.T
	env    *servertest.Env
	tap    *tap
	clock  *clock
	vars   map[string]string
	token  string
	taskID string
	state  string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	e := servertest.New(t)
	leader := e.Register("rex", "leader", "other")
	token := e.Register("wally", "worker", "claude-code")

	code, brief := e.REST("POST", "/api/v1/briefs", map[string]any{
		"title": "hooks", "body": "b", "repos": []string{"o/r"}, "max_cost_eur": 10, "max_parallel_agents": 2,
	})
	if code != 201 {
		t.Fatalf("brief: %d %v", code, brief)
	}
	plan := e.Must(leader, "propose_plan", map[string]any{"brief_id": brief["id"], "tasks": []any{
		map[string]any{"title": "hooked", "spec": "spec", "repo": "o/r"},
	}})["plan"].(map[string]any)
	if code, out := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	taskID := plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
	e.Must(leader, "assign", map[string]any{"task_id": taskID, "agent_id": "wally"})
	e.Must(token, "claim_task", map[string]any{"task_id": taskID})

	tp := newTap(t, e.TS.URL)
	state := t.TempDir()
	return &rig{
		t: t, env: e, tap: tp, clock: newClock(), token: token, taskID: taskID, state: state,
		vars: map[string]string{"SUBSPACE_URL": tp.url, "SUBSPACE_TOKEN": token, "SUBSPACE_HOOK_STATE_DIR": state},
	}
}

func (r *rig) run(stdin string, args ...string) (int, string, string) {
	r.t.Helper()
	return runWith(r.t, r.vars, r.clock, stdin, args...)
}

func runWith(t *testing.T, vars map[string]string, c *clock, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, func(k string) string { return vars[k] }, strings.NewReader(stdin), &stdout, &stderr, c.now)
	return code, stdout.String(), stderr.String()
}

func (r *rig) ok(stdin string, args ...string) string {
	r.t.Helper()
	code, stdout, stderr := r.run(stdin, args...)
	if code != exitOK {
		r.t.Fatalf("%v: exit %d\n%s", args, code, stderr)
	}
	if strings.Contains(stdout+stderr, r.token) {
		r.t.Fatalf("%v printed the token:\n%s\n%s", args, stdout, stderr)
	}
	return stdout
}

func (r *rig) output() []map[string]any {
	r.t.Helper()
	code, out := r.env.REST("GET", "/api/v1/agents/wally/output", nil)
	if code != 200 {
		r.t.Fatalf("output: %d %v", code, out)
	}
	var lines []map[string]any
	for _, l := range out["lines"].([]any) {
		lines = append(lines, l.(map[string]any))
	}
	return lines
}

func (r *rig) thread(kind, id string) string {
	r.t.Helper()
	code, out := r.env.REST("GET", "/api/v1/threads/"+kind+"/"+id+"/messages", nil)
	if code != 200 {
		r.t.Fatalf("thread: %d %v", code, out)
	}
	var b strings.Builder
	for _, m := range out["messages"].([]any) {
		b.WriteString(m.(map[string]any)["body"].(string) + "\n")
	}
	return b.String()
}

func bashPayload(cmd, stdout, stderr string) string {
	b, _ := json.Marshal(map[string]any{
		"hook_event_name": "PostToolUse", "tool_name": "Bash",
		"tool_input":    map[string]any{"command": cmd},
		"tool_response": map[string]any{"stdout": stdout, "stderr": stderr},
	})
	return string(b)
}

func TestSubcommandsCallTheRightTools(t *testing.T) {
	r := newRig(t)
	r.vars["SUBSPACE_TASK_ID"] = r.taskID

	r.ok("", "heartbeat")
	if got := r.tap.last(); got.tool != "heartbeat" || got.args["task_id"] != r.taskID {
		t.Fatalf("heartbeat call = %+v", got)
	}

	r.ok(bashPayload("go test ./...", "ok  pkg 0.1s\nPASS\n", "warning: slow"), "log")
	got := r.tap.last()
	if got.tool != "log_output" || got.args["task_id"] != r.taskID || got.args["stream"] != "stdout" {
		t.Fatalf("log call = %+v", got)
	}
	var texts []string
	for _, l := range r.output() {
		texts = append(texts, l["text"].(string))
	}
	if want := []string{"$ go test ./...", "ok  pkg 0.1s", "PASS", "! warning: slow"}; strings.Join(texts, "|") != strings.Join(want, "|") {
		t.Fatalf("output lines = %q want %q", texts, want)
	}

	r.clock.advance(3 * time.Second)
	r.ok(`{"tool_name":"Edit","tool_input":{"file_path":"/repo/main.go"}}`, "log")
	if got := r.output(); got[len(got)-1]["text"] != "edit /repo/main.go" {
		t.Fatalf("edit line = %v", got[len(got)-1])
	}

	r.ok(`{"hook_event_name":"Stop"}`, "status")
	if tools := r.tap.tools(); tools[len(tools)-1] != "update_status" {
		t.Fatalf("tools = %v", tools)
	}
	got = r.tap.last()
	if got.args["task_id"] != r.taskID || got.args["status"] != "in_progress" {
		t.Fatalf("status call = %+v", got)
	}
	if th := r.thread("task", r.taskID); !strings.Contains(th, "Claude Code finished responding") {
		t.Fatalf("task thread:\n%s", th)
	}

	r.ok("", "status", "-progress", "40", "-status", "blocked", "waiting on review")
	got = r.tap.last()
	if got.args["progress_pct"] != float64(40) || got.args["status"] != "blocked" || got.args["note"] != "waiting on review" {
		t.Fatalf("explicit status call = %+v", got)
	}

	r.ok(`{"hook_event_name":"Notification","message":"needs permission for Bash"}`, "note")
	if got := r.tap.last(); got.tool != "add_note" || got.args["task_id"] != r.taskID {
		t.Fatalf("note call = %+v", got)
	}
	if th := r.thread("task", r.taskID); !strings.Contains(th, "Claude Code notification: needs permission for Bash") {
		t.Fatalf("task thread:\n%s", th)
	}

	out := r.ok("", "instructions")
	if !strings.Contains(out, "wally") || !strings.Contains(out, "worker") {
		t.Fatalf("instructions:\n%s", out)
	}
}

func TestStatusResolvesTheInProgressTask(t *testing.T) {
	r := newRig(t)
	r.ok(`{"hook_event_name":"SubagentStop"}`, "status")
	if got := r.tap.tools(); strings.Join(got, ",") != "list_my_tasks,update_status" {
		t.Fatalf("tools = %v", got)
	}
	if got := r.tap.last(); got.args["task_id"] != r.taskID {
		t.Fatalf("status call = %+v", got)
	}
	if th := r.thread("task", r.taskID); !strings.Contains(th, "Claude Code subagent finished") {
		t.Fatalf("task thread:\n%s", th)
	}
}

func TestThrottles(t *testing.T) {
	r := newRig(t)
	payload := bashPayload("ls", "a\n", "")

	r.ok("", "heartbeat")
	r.clock.advance(59 * time.Second)
	r.ok("", "heartbeat")
	if r.tap.count() != 1 {
		t.Fatalf("heartbeat was not throttled: %v", r.tap.tools())
	}
	r.clock.advance(2 * time.Second)
	r.ok("", "heartbeat")
	if r.tap.count() != 2 {
		t.Fatalf("heartbeat after the window was skipped: %v", r.tap.tools())
	}

	before := r.tap.count()
	r.ok(payload, "log")
	r.clock.advance(time.Second)
	r.ok(payload, "log")
	r.clock.advance(time.Second)
	r.ok(payload, "log")
	if got := r.tap.count() - before; got != 2 {
		t.Fatalf("log calls = %d, want 2 over 2s", got)
	}

	before = r.tap.count()
	for range 20 {
		r.ok("", "status", "-task", r.taskID, "still working")
		r.ok("", "note", "-task", r.taskID, "still working")
	}
	if got := r.tap.count() - before; got != 8 {
		t.Fatalf("status+note calls = %d, want 4 each", got)
	}
	code, out := r.env.REST("GET", "/api/v1/agents/wally", nil)
	if code != 200 || out["status"] != "active" {
		t.Fatalf("agent after burst: %d %v", code, out)
	}

	r.clock.advance(11 * time.Minute)
	before = r.tap.count()
	r.ok("", "note", "-task", r.taskID, "still working")
	if r.tap.count() != before+1 {
		t.Fatal("note window did not reopen")
	}
}

func TestConcurrentHooksShareOneThrottle(t *testing.T) {
	r := newRig(t)
	payload := bashPayload("make", "built\n", "")
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _, stderr := runWith(t, r.vars, r.clock, payload, "log")
			if code != exitOK {
				t.Errorf("exit %d: %s", code, stderr)
			}
		}()
	}
	wg.Wait()
	if got := r.tap.count(); got != 1 {
		t.Fatalf("log calls = %d, want 1", got)
	}
}

func TestLogSequenceSurvivesStateLoss(t *testing.T) {
	r := newRig(t)
	payload := bashPayload("echo one", "one\n", "")
	r.ok(payload, "log")
	r.clock.advance(5 * time.Second)
	r.ok(bashPayload("echo two", "two\n", ""), "log")
	if err := os.RemoveAll(r.state); err != nil {
		t.Fatal(err)
	}
	r.clock.advance(5 * time.Second)
	r.ok(bashPayload("echo three", "three\n", ""), "log")
	var texts []string
	for _, l := range r.output() {
		texts = append(texts, l["text"].(string))
	}
	if !strings.Contains(strings.Join(texts, "|"), "$ echo three") {
		t.Fatalf("chunk after state loss was dropped as duplicate: %q", texts)
	}
}

func TestFailsOpenWhenServerIsDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var accepts atomic.Int64
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			_ = c.Close()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })

	token := "ssp_" + strings.Repeat("a", 43)
	vars := map[string]string{
		"SUBSPACE_URL": "http://" + ln.Addr().String(), "SUBSPACE_TOKEN": token,
		"SUBSPACE_HOOK_STATE_DIR": t.TempDir(),
	}
	c := newClock()
	payload := bashPayload("ls", "x\n", "")

	for _, args := range [][]string{{"heartbeat"}, {"log"}, {"status", "-task", "T-1"}, {"note", "hello"}, {"instructions"}} {
		start := time.Now()
		code, stdout, stderr := runWith(t, vars, c, payload, args...)
		if code != exitOK {
			t.Fatalf("%v: exit %d\n%s", args, code, stderr)
		}
		if time.Since(start) > 2*time.Second {
			t.Fatalf("%v blocked for %s", args, time.Since(start))
		}
		if strings.Contains(stdout+stderr, token) {
			t.Fatalf("%v printed the token", args)
		}
		c.advance(31 * time.Second)
	}

	c.advance(time.Minute)
	runWith(t, vars, c, payload, "log")
	seen := accepts.Load()
	c.advance(3 * time.Second)
	code, _, _ := runWith(t, vars, c, payload, "log")
	if code != exitOK || accepts.Load() != seen {
		t.Fatalf("down server was contacted again within the backoff: %d -> %d", seen, accepts.Load())
	}
	c.advance(downFor)
	runWith(t, vars, c, payload, "log")
	if accepts.Load() == seen {
		t.Fatal("server was never retried after the backoff")
	}
}

func TestNeverBlocksLongerThanTwoSeconds(t *testing.T) {
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(func() { close(release); ts.Close() })
	vars := map[string]string{
		"SUBSPACE_URL": ts.URL, "SUBSPACE_TOKEN": "ssp_" + strings.Repeat("b", 43),
		"SUBSPACE_HOOK_STATE_DIR": t.TempDir(),
	}
	start := time.Now()
	code, _, _ := runWith(t, vars, newClock(), "", "heartbeat")
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	if d := time.Since(start); d >= 2*time.Second {
		t.Fatalf("took %s", d)
	}
}

func TestStdinThatNeverClosesDoesNotBlock(t *testing.T) {
	r := newRig(t)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	var stdout, stderr bytes.Buffer
	start := time.Now()
	code := run(context.Background(), []string{"note"}, func(k string) string { return r.vars[k] }, pr, &stdout, &stderr, r.clock.now)
	if code != exitOK || time.Since(start) >= 2*time.Second {
		t.Fatalf("exit %d after %s", code, time.Since(start))
	}
	if r.tap.count() != 0 {
		t.Fatalf("note without text called the server: %v", r.tap.tools())
	}
}

func TestTokenNeverLeaks(t *testing.T) {
	r := newRig(t)
	r.ok("", "log", "-task", r.taskID, "export TOKEN="+r.token)
	r.ok("", "note", "-task", r.taskID, "token is "+r.token)
	r.ok(bashPayload("echo "+r.token, r.token+"\n", r.token), "status", "-task", r.taskID, "pw "+r.token)

	for _, c := range r.tap.calls {
		if b, _ := json.Marshal(c.args); strings.Contains(string(b), r.token) {
			t.Fatalf("%s sent the token: %s", c.tool, b)
		}
	}
	for _, l := range r.output() {
		if strings.Contains(l["text"].(string), r.token) {
			t.Fatalf("output holds the token: %v", l)
		}
	}
	if th := r.thread("task", r.taskID); strings.Contains(th, r.token) {
		t.Fatalf("thread holds the token:\n%s", th)
	}
	err := filepath.Walk(r.state, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		if b, _ := os.ReadFile(p); strings.Contains(string(b)+fi.Name(), r.token) {
			return fmt.Errorf("%s holds the token", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	bad := map[string]string{"SUBSPACE_URL": r.tap.url, "SUBSPACE_TOKEN": "ssp_" + strings.Repeat("z", 43), "SUBSPACE_HOOK_STATE_DIR": t.TempDir()}
	code, stdout, stderr := runWith(t, bad, r.clock, "", "heartbeat")
	if code != exitOK || strings.Contains(stdout+stderr, bad["SUBSPACE_TOKEN"]) || !strings.Contains(stderr, "401") {
		t.Fatalf("rejected token: exit %d\n%s\n%s", code, stdout, stderr)
	}
}

func TestConfigSources(t *testing.T) {
	r := newRig(t)
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte(r.token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	r.vars["SUBSPACE_TOKEN"] = "ssp_" + strings.Repeat("w", 43)
	r.vars["SUBSPACE_TOKEN_FILE"] = file
	r.ok("", "heartbeat")
	if r.tap.count() != 1 {
		t.Fatal("token file did not win over SUBSPACE_TOKEN")
	}

	r.vars["SUBSPACE_URL"] += "/mcp/"
	r.clock.advance(time.Minute + time.Second)
	r.ok("", "heartbeat")
	if r.tap.count() != 2 {
		t.Fatal("url with /mcp suffix was not accepted")
	}

	for name, vars := range map[string]map[string]string{
		"no url":          {"SUBSPACE_TOKEN": r.token},
		"no token":        {"SUBSPACE_URL": r.tap.url},
		"unreadable file": {"SUBSPACE_URL": r.tap.url, "SUBSPACE_TOKEN_FILE": file + ".missing"},
	} {
		before := r.tap.count()
		code, _, stderr := runWith(t, vars, r.clock, "", "heartbeat")
		if code != exitOK || stderr == "" || r.tap.count() != before {
			t.Fatalf("%s: exit %d stderr %q calls %d", name, code, stderr, r.tap.count()-before)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{nil, {"explode"}} {
		if code, _, stderr := runWith(t, nil, newClock(), "", args...); code != exitUsage || !strings.Contains(stderr, "usage:") {
			t.Fatalf("%v: exit %d %q", args, code, stderr)
		}
	}
}
