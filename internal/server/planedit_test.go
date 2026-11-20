package server

import (
	"slices"
	"strings"
	"testing"
)

func (e *env) proposePlan(lead, bid string, tasks ...map[string]any) (string, []string) {
	e.t.Helper()
	in := make([]any, len(tasks))
	for i, t := range tasks {
		in[i] = t
	}
	plan := e.must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": in})["plan"].(map[string]any)
	var ids []string
	for _, t := range plan["tasks"].([]any) {
		ids = append(ids, t.(map[string]any)["id"].(string))
	}
	return plan["id"].(string), ids
}

func (e *env) restCode(want int, code, method, path string, body any) {
	e.t.Helper()
	got, out := e.rest(method, path, body)
	if got != want {
		e.t.Fatalf("%s %s: %d want %d: %v", method, path, got, want, out)
	}
	if c, _ := out["error"].(map[string]any)["code"].(string); want >= 400 && c != code {
		e.t.Fatalf("%s %s: error code %q want %q: %v", method, path, c, code, out)
	}
}

func (e *env) briefMessages(bid string) []string {
	e.t.Helper()
	var out []string
	for _, m := range e.restWant(200, "GET", "/api/v1/threads/brief/"+bid+"/messages", nil)["messages"].([]any) {
		out = append(out, m.(map[string]any)["body"].(string))
	}
	return out
}

func (e *env) auditTools(taskID string) []string {
	e.t.Helper()
	var out []string
	for _, ev := range e.restWant(200, "GET", "/api/v1/audit?task_id="+taskID, nil)["events"].([]any) {
		out = append(out, ev.(map[string]any)["tool"].(string))
	}
	return out
}

func hasMessage(msgs []string, parts ...string) bool {
	return slices.ContainsFunc(msgs, func(m string) bool {
		for _, p := range parts {
			if !strings.Contains(m, p) {
				return false
			}
		}
		return true
	})
}

func TestOwnerEditsPlanTasks(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	wk := e.register("w1", "worker", "claude-code")
	bid := e.brief(map[string]any{"repos": []string{"o/r", "o/x"}})
	pid, ids := e.proposePlan(lead, bid, planTask("first", nil), planTask("second", nil))
	a, b := ids[0], ids[1]

	e.restCode(400, "bad_request", "PATCH", "/api/v1/tasks/"+a, map[string]any{})
	e.restCode(404, "not_found", "PATCH", "/api/v1/tasks/T-999", map[string]any{"title": "x"})

	got := e.restWant(200, "PATCH", "/api/v1/tasks/"+a, map[string]any{
		"title": "Renamed task", "spec": "new spec", "acceptance_criteria": []string{"one", "two"},
		"repo": "o/x", "role": "tester", "depends_on": []string{b}, "budget": map[string]any{"max_minutes": 30, "max_cost_eur": 2.5},
	})
	if got["title"] != "Renamed task" || got["spec"] != "new spec" || got["repo"] != "o/x" || got["role"] != "tester" ||
		len(got["acceptance_criteria"].([]any)) != 2 || got["depends_on"].([]any)[0] != b || got["status"] != "backlog" {
		t.Fatalf("edited task: %v", got)
	}
	if got["budget"].(map[string]any)["max_minutes"].(float64) != 30 || !strings.HasSuffix(got["branch"].(string), "renamed-task") {
		t.Fatalf("budget/branch: %v", got)
	}
	if !hasMessage(e.briefMessages(bid), "Owner edited "+a, "title", "depends_on") {
		t.Fatalf("brief thread: %v", e.briefMessages(bid))
	}
	if !slices.Contains(e.auditTools(a), "edit_task") {
		t.Fatalf("audit: %v", e.auditTools(a))
	}
	_, audit := e.rest("GET", "/api/v1/audit?task_id="+a+"&tool=edit_task", nil)
	ev := audit["events"].([]any)[0].(map[string]any)
	if ev["role"] != "owner" || ev["before"].(map[string]any)["title"] != "first" || ev["after"].(map[string]any)["title"] != "Renamed task" {
		t.Fatalf("audit event: %v", ev)
	}

	for name, body := range map[string]map[string]any{
		"empty title":  {"title": "  "},
		"empty spec":   {"spec": ""},
		"foreign repo": {"repo": "o/other"},
		"empty repo":   {"repo": ""},
		"bad role":     {"role": "leader"},
		"self dep":     {"depends_on": []string{a}},
		"unknown dep":  {"depends_on": []string{"T-999"}},
		"negative bud": {"title": "x", "budget": map[string]any{"max_minutes": -1}},
		"cycle":        {"depends_on": []string{a}, "title": "y"},
	} {
		target := a
		if name == "cycle" {
			target = b
		}
		if got, _ := e.rest("PATCH", "/api/v1/tasks/"+target, body); got != 400 {
			t.Fatalf("%s: status %d", name, got)
		}
	}
	if tk := e.task(a); tk["title"] != "Renamed task" || tk["repo"] != "o/x" {
		t.Fatalf("rejected edits must not apply: %v", tk)
	}
	if tk := e.task(b); len(tk["depends_on"].([]any)) != 0 || tk["title"] != "second" {
		t.Fatalf("cycle edit must not apply: %v", tk)
	}

	tok := e.register("x1", "reviewer", "codex")
	if c := e.restAs(tok, "PATCH", "/api/v1/tasks/"+a); c != 403 {
		t.Fatalf("agent token on PATCH: %d", c)
	}
	if c := e.restAs("", "PATCH", "/api/v1/tasks/"+a); c != 401 {
		t.Fatalf("no token on PATCH: %d", c)
	}

	e.restWant(200, "POST", "/api/v1/plans/"+pid+"/approve", map[string]any{})
	e.restWant(200, "PATCH", "/api/v1/tasks/"+b, map[string]any{"spec": "edited while ready", "depends_on": []string{}})
	if tk := e.task(b); tk["status"] != "ready" || tk["spec"] != "edited while ready" {
		t.Fatalf("ready edit: %v", tk)
	}

	e.must(lead, "assign", map[string]any{"task_id": b, "agent_id": "w1"})
	e.restCode(409, "conflict", "PATCH", "/api/v1/tasks/"+b, map[string]any{"title": "late"})
	e.must(wk, "claim_task", map[string]any{"task_id": b})
	e.restCode(409, "conflict", "PATCH", "/api/v1/tasks/"+b, map[string]any{"spec": "late", "budget": map[string]any{"max_minutes": 5}})
	if tk := e.task(b); tk["spec"] != "edited while ready" || tk["budget"].(map[string]any)["max_minutes"].(float64) != 0 {
		t.Fatalf("in-progress task must be untouched: %v", tk)
	}
	e.restWant(200, "PATCH", "/api/v1/tasks/"+b, map[string]any{"budget": map[string]any{"max_minutes": 90}})
}

func TestOwnerAddsPlanTask(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	bid := e.brief(map[string]any{"repos": []string{"o/r"}})
	pid, ids := e.proposePlan(lead, bid, planTask("first", nil))

	plan := e.restWant(201, "POST", "/api/v1/plans/"+pid+"/tasks", planTask("added", map[string]any{"depends_on": []string{ids[0]}, "role": "tester"}))
	tasks := plan["tasks"].([]any)
	if len(tasks) != 2 {
		t.Fatalf("plan tasks: %v", tasks)
	}
	added := tasks[1].(map[string]any)
	if added["plan_id"] != pid || added["brief_id"] != bid || added["status"] != "backlog" || added["role"] != "tester" || added["depends_on"].([]any)[0] != ids[0] {
		t.Fatalf("added task: %v", added)
	}
	id := added["id"].(string)
	if got := e.restWant(200, "GET", "/api/v1/plans/"+pid, nil)["tasks"].([]any); len(got) != 2 {
		t.Fatalf("plan after add: %v", got)
	}
	if !hasMessage(e.briefMessages(bid), "Owner added "+id) || !slices.Contains(e.auditTools(id), "add_plan_task") {
		t.Fatalf("thread %v audit %v", e.briefMessages(bid), e.auditTools(id))
	}

	e.restCode(400, "bad_request", "POST", "/api/v1/plans/"+pid+"/tasks", planTask("x", map[string]any{"repo": "o/other"}))
	e.restCode(400, "bad_request", "POST", "/api/v1/plans/"+pid+"/tasks", map[string]any{"title": "x", "repo": "o/r"})
	e.restCode(400, "bad_request", "POST", "/api/v1/plans/"+pid+"/tasks", planTask("x", map[string]any{"depends_on": []string{"T-999"}}))
	e.restCode(400, "bad_request", "POST", "/api/v1/plans/"+pid+"/tasks", planTask("x", map[string]any{"role": "reviewer"}))
	e.restCode(404, "not_found", "POST", "/api/v1/plans/P-999/tasks", planTask("x", nil))
	if got := e.restWant(200, "GET", "/api/v1/plans/"+pid, nil)["tasks"].([]any); len(got) != 2 {
		t.Fatalf("rejected adds must not create tasks: %v", got)
	}

	tok := e.register("x1", "reviewer", "codex")
	if c := e.restAs(tok, "POST", "/api/v1/plans/"+pid+"/tasks"); c != 403 {
		t.Fatalf("agent token on add: %d", c)
	}

	e.restWant(200, "POST", "/api/v1/plans/"+pid+"/approve", map[string]any{})
	if tk := e.task(id); tk["status"] != "ready" {
		t.Fatalf("added task approved with plan: %v", tk["status"])
	}
	e.restCode(409, "conflict", "POST", "/api/v1/plans/"+pid+"/tasks", planTask("late", nil))
}

func TestOwnerDropsPlanTask(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	bid := e.brief(map[string]any{})
	pid, ids := e.proposePlan(lead, bid,
		planTask("base", map[string]any{"ref": "base"}),
		planTask("mid", map[string]any{"ref": "mid", "depends_on": []string{"base"}}),
		planTask("top", map[string]any{"depends_on": []string{"mid"}}),
		planTask("loose", nil),
	)
	base, mid, top, loose := ids[0], ids[1], ids[2], ids[3]

	code, out := e.rest("DELETE", "/api/v1/plans/"+pid+"/tasks/"+base, nil)
	msg, _ := out["error"].(map[string]any)["message"].(string)
	if code != 409 || !strings.Contains(msg, mid) {
		t.Fatalf("drop referenced task: %d %v", code, out)
	}
	if tk := e.task(base); tk["status"] != "backlog" {
		t.Fatalf("refused drop must not cancel: %v", tk["status"])
	}

	plan := e.restWant(200, "DELETE", "/api/v1/plans/"+pid+"/tasks/"+loose, nil)
	if plan["id"] != pid || len(plan["tasks"].([]any)) != 4 {
		t.Fatalf("plan after drop: %v", plan)
	}
	if tk := e.task(loose); tk["status"] != "cancelled" {
		t.Fatalf("dropped task: %v", tk["status"])
	}
	if !hasMessage(e.briefMessages(bid), "Owner dropped "+loose) || !slices.Contains(e.auditTools(loose), "drop_plan_task") {
		t.Fatalf("thread %v audit %v", e.briefMessages(bid), e.auditTools(loose))
	}
	e.restCode(409, "conflict", "DELETE", "/api/v1/plans/"+pid+"/tasks/"+loose, nil)
	e.restCode(400, "bad_request", "PATCH", "/api/v1/tasks/"+top, map[string]any{"depends_on": []string{loose}})
	e.restCode(400, "bad_request", "POST", "/api/v1/plans/"+pid+"/tasks", planTask("x", map[string]any{"depends_on": []string{loose}}))
	e.restCode(404, "not_found", "DELETE", "/api/v1/plans/"+pid+"/tasks/T-999", nil)
	e.restCode(404, "not_found", "DELETE", "/api/v1/plans/P-999/tasks/"+base, nil)

	other := e.brief(map[string]any{})
	_, otherIDs := e.proposePlan(lead, other, planTask("elsewhere", nil))
	e.restCode(404, "not_found", "DELETE", "/api/v1/plans/"+pid+"/tasks/"+otherIDs[0], nil)

	tok := e.register("x1", "reviewer", "codex")
	if c := e.restAs(tok, "DELETE", "/api/v1/plans/"+pid+"/tasks/"+top); c != 403 {
		t.Fatalf("agent token on drop: %d", c)
	}

	e.restWant(200, "PATCH", "/api/v1/tasks/"+top, map[string]any{"depends_on": []string{}})
	e.restWant(200, "DELETE", "/api/v1/plans/"+pid+"/tasks/"+mid, nil)
	if tk := e.task(top); len(tk["depends_on"].([]any)) != 0 {
		t.Fatalf("top deps: %v", tk["depends_on"])
	}

	e.restWant(200, "POST", "/api/v1/plans/"+pid+"/approve", map[string]any{})
	if e.task(loose)["status"] != "cancelled" || e.task(mid)["status"] != "cancelled" || e.task(base)["status"] != "ready" || e.task(top)["status"] != "ready" {
		t.Fatal("approval must only ready the remaining tasks")
	}
	e.restCode(409, "conflict", "DELETE", "/api/v1/plans/"+pid+"/tasks/"+base, nil)
}

func TestPlanEditsRejectCycles(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	bid := e.brief(map[string]any{})
	pid, ids := e.proposePlan(lead, bid,
		planTask("a", map[string]any{"ref": "a"}),
		planTask("b", map[string]any{"depends_on": []string{"a"}, "ref": "b"}),
		planTask("c", map[string]any{"depends_on": []string{"b"}}),
	)
	e.restCode(400, "bad_request", "PATCH", "/api/v1/tasks/"+ids[0], map[string]any{"depends_on": []string{ids[2]}})
	e.restCode(400, "bad_request", "PATCH", "/api/v1/tasks/"+ids[0], map[string]any{"depends_on": []string{ids[1]}})
	e.restWant(200, "PATCH", "/api/v1/tasks/"+ids[2], map[string]any{"depends_on": []string{ids[0], ids[1]}})
	plan := e.restWant(201, "POST", "/api/v1/plans/"+pid+"/tasks", planTask("d", map[string]any{"depends_on": []string{ids[2], ids[2]}}))
	added := plan["tasks"].([]any)[3].(map[string]any)
	if deps := added["depends_on"].([]any); len(deps) != 1 {
		t.Fatalf("duplicate deps must collapse: %v", deps)
	}
}
