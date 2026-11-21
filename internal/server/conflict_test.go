package server_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/servertest"
)

type conflictEnv struct {
	*servertest.Env
	lead string
	bid  string
}

func newConflictEnv(t *testing.T, mode string) *conflictEnv {
	t.Setenv("SUBSPACE_CONFLICT_MODE", mode)
	e := &conflictEnv{Env: servertest.New(t)}
	e.lead = e.Register("rex", "leader", "other")
	e.Register("w1", "worker", "claude-code")
	e.Register("w2", "worker", "codex")
	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B", "repos": []string{"o/r", "o/s"}})
	e.bid = br["id"].(string)
	return e
}

func (e *conflictEnv) plan(tasks ...map[string]any) (string, map[string]map[string]any) {
	e.T.Helper()
	items := make([]any, len(tasks))
	for i, tk := range tasks {
		tk["spec"], tk["acceptance_criteria"] = "s", []string{"ok"}
		if tk["repo"] == nil {
			tk["repo"] = "o/r"
		}
		items[i] = tk
	}
	p := e.Must(e.lead, "propose_plan", map[string]any{"brief_id": e.bid, "tasks": items})["plan"].(map[string]any)
	byTitle := map[string]map[string]any{}
	for _, tk := range p["tasks"].([]any) {
		m := tk.(map[string]any)
		byTitle[m["title"].(string)] = m
	}
	return p["id"].(string), byTitle
}

func (e *conflictEnv) approve(pid string) {
	e.T.Helper()
	if code, out := e.REST("POST", "/api/v1/plans/"+pid+"/approve", map[string]any{}); code != 200 {
		e.T.Fatalf("approve: %d %v", code, out)
	}
}

func (e *conflictEnv) assign(taskID, agent string) (map[string]any, bool) {
	return e.Call(e.lead, "assign", map[string]any{"task_id": taskID, "agent_id": agent})
}

func (e *conflictEnv) systemMessages(taskID string) []string {
	e.T.Helper()
	_, out := e.REST("GET", "/api/v1/threads/task/"+taskID+"/messages", nil)
	var bodies []string
	for _, m := range out["messages"].([]any) {
		if mm := m.(map[string]any); mm["kind"] == "system" {
			bodies = append(bodies, mm["body"].(string))
		}
	}
	return bodies
}

func (e *conflictEnv) decisionIDs() []string {
	e.T.Helper()
	_, out := e.REST("GET", "/api/v1/decisions", nil)
	var ids []string
	for _, d := range out["decisions"].([]any) {
		ids = append(ids, d.(map[string]any)["id"].(string))
	}
	return ids
}

func paths(tk map[string]any) []string {
	var out []string
	for _, p := range tk["paths"].([]any) {
		out = append(out, p.(string))
	}
	return out
}

func TestTaskPathsInputs(t *testing.T) {
	e := newConflictEnv(t, "")
	for _, bad := range []string{"../etc", "/abs", "a/../b", `a\b`, ""} {
		out, isErr := e.Call(e.lead, "propose_plan", map[string]any{"brief_id": e.bid, "tasks": []any{
			map[string]any{"title": "x", "spec": "s", "repo": "o/r", "paths": []string{bad}}}})
		if !isErr || out["error"].(map[string]any)["code"] != "bad_request" {
			t.Fatalf("path %q accepted: %v", bad, out)
		}
	}
	if code, _ := e.REST("PUT", "/api/v1/briefs/"+e.bid+"/components", map[string]any{"components": []any{
		map[string]any{"name": "api", "paths": []string{"internal/api", "internal/api/"}}}}); code != 200 {
		t.Fatalf("components: %d", code)
	}
	pid, ts := e.plan(
		map[string]any{"title": "api", "component_id": "C-1"},
		map[string]any{"title": "handler", "component_id": "C-1", "paths": []string{"./internal//api/handler.go", "internal/api/handler.go"}},
		map[string]any{"title": "none"},
	)
	for title, want := range map[string][]string{"api": {"internal/api"}, "handler": {"internal/api/handler.go"}, "none": nil} {
		if got := paths(ts[title]); !slices.Equal(got, want) {
			t.Fatalf("%s paths %q want %q", title, got, want)
		}
	}

	code, p := e.REST("POST", "/api/v1/plans/"+pid+"/tasks", map[string]any{"title": "docs", "spec": "s", "repo": "o/r", "paths": []string{"docs/"}})
	if code != 201 {
		t.Fatalf("add plan task: %d %v", code, p)
	}
	tasks := p["tasks"].([]any)
	added := tasks[len(tasks)-1].(map[string]any)
	if got := paths(added); !slices.Equal(got, []string{"docs"}) {
		t.Fatalf("added task paths %q", got)
	}
	if code, out := e.REST("POST", "/api/v1/plans/"+pid+"/tasks", map[string]any{"title": "bad", "spec": "s", "repo": "o/r", "paths": []string{"/etc"}}); code != 400 {
		t.Fatalf("absolute path on plan edit: %d %v", code, out)
	}

	id := added["id"].(string)
	code, tk := e.REST("PATCH", "/api/v1/tasks/"+id, map[string]any{"paths": []string{"docs/*.md", "docs/*.md"}})
	if code != 200 || !slices.Equal(paths(tk), []string{"docs/*.md"}) {
		t.Fatalf("patch paths: %d %v", code, tk)
	}
	if code, out := e.REST("PATCH", "/api/v1/tasks/"+id, map[string]any{"paths": []string{"docs/../../x"}}); code != 400 {
		t.Fatalf("escaping path on patch: %d %v", code, out)
	}
	_, audit := e.REST("GET", "/api/v1/audit?task_id="+id+"&tool=edit_task", nil)
	if len(audit["events"].([]any)) != 1 {
		t.Fatalf("edit_task audit: %v", audit)
	}

	created := e.Must(e.lead, "create_task", map[string]any{"brief_id": e.bid, "title": "c", "spec": "s", "repo": "o/r",
		"paths": []string{"cmd/**"}})["task"].(map[string]any)
	if got := paths(created); !slices.Equal(got, []string{"cmd/**"}) {
		t.Fatalf("create_task paths %q", got)
	}
}

func TestAssignOverlapWarn(t *testing.T) {
	e := newConflictEnv(t, "warn")
	pid, ts := e.plan(
		map[string]any{"title": "api", "paths": []string{"internal/api"}},
		map[string]any{"title": "handler", "paths": []string{"internal/*/handler.go"}},
		map[string]any{"title": "web", "paths": []string{"web/**"}},
	)
	e.approve(pid)
	api, handler, web := ts["api"]["id"].(string), ts["handler"]["id"].(string), ts["web"]["id"].(string)

	e.Must(e.lead, "assign", map[string]any{"task_id": api, "agent_id": "w1"})
	e.Must(e.lead, "assign", map[string]any{"task_id": handler, "agent_id": "w2"})
	e.Must(e.lead, "assign", map[string]any{"task_id": web, "agent_id": "w2"})

	msgs := e.systemMessages(handler)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "Path overlap") || !strings.Contains(msgs[0], api+" (w1") {
		t.Fatalf("warning on %s: %q", handler, msgs)
	}
	if msgs := e.systemMessages(web); len(msgs) != 0 {
		t.Fatalf("unexpected warning on %s: %q", web, msgs)
	}
	ids := e.decisionIDs()
	if !slices.Contains(ids, "conflict:"+handler) || slices.Contains(ids, "conflict:"+api) || slices.Contains(ids, "conflict:"+web) {
		t.Fatalf("decisions %v", ids)
	}

	if code, out := e.REST("POST", "/api/v1/tasks/"+api+"/cancel", map[string]any{"reason": "x"}); code != 200 {
		t.Fatalf("cancel: %d %v", code, out)
	}
	if ids := e.decisionIDs(); slices.Contains(ids, "conflict:"+handler) {
		t.Fatalf("conflict decision should clear once the overlap is gone: %v", ids)
	}
}

func TestAssignOverlapBlock(t *testing.T) {
	e := newConflictEnv(t, "block")
	pid, ts := e.plan(
		map[string]any{"title": "api", "paths": []string{"internal/api"}},
		map[string]any{"title": "store", "paths": []string{"internal/store"}},
		map[string]any{"title": "both", "paths": []string{"internal/**/*.go"}},
		map[string]any{"title": "other repo", "repo": "o/s", "paths": []string{"internal/api"}},
	)
	e.approve(pid)
	id := func(title string) string { return ts[title]["id"].(string) }

	e.Must(e.lead, "assign", map[string]any{"task_id": id("api"), "agent_id": "w1"})
	e.Must(e.lead, "assign", map[string]any{"task_id": id("store"), "agent_id": "w1"})
	out, isErr := e.assign(id("both"), "w2")
	if !isErr {
		t.Fatalf("overlapping assign should fail: %v", out)
	}
	er := out["error"].(map[string]any)
	if er["code"] != "conflict" || !strings.Contains(er["message"].(string), id("api")) || !strings.Contains(er["message"].(string), id("store")) {
		t.Fatalf("block error: %v", er)
	}
	if _, tk := e.REST("GET", "/api/v1/tasks/"+id("both"), nil); tk["status"] != "ready" || tk["assignee"] != nil {
		t.Fatalf("blocked task changed: %v", tk)
	}
	e.Must(e.lead, "assign", map[string]any{"task_id": id("other repo"), "agent_id": "w2"})
	e.Must(e.lead, "assign", map[string]any{"task_id": id("both"), "agent_id": "w1"})
	if msgs := e.systemMessages(id("both")); len(msgs) != 0 {
		t.Fatalf("same-agent overlap should not warn: %q", msgs)
	}
	for _, d := range e.decisionIDs() {
		if strings.HasPrefix(d, "conflict:") {
			t.Fatalf("unexpected conflict decision %s", d)
		}
	}
}

func TestAssignOverlapOff(t *testing.T) {
	e := newConflictEnv(t, "off")
	pid, ts := e.plan(
		map[string]any{"title": "a", "paths": []string{"internal"}},
		map[string]any{"title": "b", "paths": []string{"internal/api"}},
	)
	e.approve(pid)
	a, b := ts["a"]["id"].(string), ts["b"]["id"].(string)
	e.Must(e.lead, "assign", map[string]any{"task_id": a, "agent_id": "w1"})
	e.Must(e.lead, "assign", map[string]any{"task_id": b, "agent_id": "w2"})
	if msgs := e.systemMessages(b); len(msgs) != 0 {
		t.Fatalf("mode off should not warn: %q", msgs)
	}
	if ids := e.decisionIDs(); slices.ContainsFunc(ids, func(s string) bool { return strings.HasPrefix(s, "conflict:") }) {
		t.Fatalf("mode off should not hint: %v", ids)
	}
}
