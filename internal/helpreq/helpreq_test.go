package helpreq_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/servertest"
)

type fixture struct {
	*servertest.Env
	lead, worker, tester, reviewer string
	brief                          string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	e := servertest.New(t)
	f := &fixture{Env: e}
	f.lead = e.Register("rex", "leader", "other")
	f.worker = e.Register("w1", "worker", "claude-code")
	f.tester = e.Register("t1", "tester", "claude-code")
	f.reviewer = e.Register("r1", "reviewer", "codex")
	code, out := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B", "body": "b"})
	if code != 201 {
		t.Fatalf("brief: %d %v", code, out)
	}
	f.brief = out["id"].(string)
	return f
}

func task(title string, extra map[string]any) map[string]any {
	m := map[string]any{"title": title, "spec": "spec of " + title, "repo": "o/r", "acceptance_criteria": []string{"works"}}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func (f *fixture) plan(tasks ...map[string]any) []string {
	f.T.Helper()
	in := make([]any, len(tasks))
	for i, x := range tasks {
		in[i] = x
	}
	plan := f.Must(f.lead, "propose_plan", map[string]any{"brief_id": f.brief, "tasks": in})["plan"].(map[string]any)
	if code, out := f.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		f.T.Fatalf("approve: %d %v", code, out)
	}
	var ids []string
	for _, x := range plan["tasks"].([]any) {
		ids = append(ids, x.(map[string]any)["id"].(string))
	}
	return ids
}

func (f *fixture) working(id string) {
	f.T.Helper()
	f.Must(f.lead, "assign", map[string]any{"task_id": id, "agent_id": "w1"})
	f.Must(f.worker, "claim_task", map[string]any{"task_id": id})
}

func (f *fixture) taskOf(id string) map[string]any {
	f.T.Helper()
	code, out := f.REST("GET", "/api/v1/tasks/"+id, nil)
	if code != 200 {
		f.T.Fatalf("task %s: %d %v", id, code, out)
	}
	return out
}

func (f *fixture) thread(kind, id string) []string {
	f.T.Helper()
	_, out := f.REST("GET", "/api/v1/threads/"+kind+"/"+id+"/messages", nil)
	var bodies []string
	for _, m := range out["messages"].([]any) {
		bodies = append(bodies, m.(map[string]any)["body"].(string))
	}
	return bodies
}

func hasBody(bodies []string, sub string) bool {
	return slices.ContainsFunc(bodies, func(b string) bool { return strings.Contains(b, sub) })
}

func (f *fixture) requests(status string) []map[string]any {
	f.T.Helper()
	out := f.Must(f.lead, "list_help_requests", map[string]any{"status": status})
	var rs []map[string]any
	for _, r := range out["requests"].([]any) {
		rs = append(rs, r.(map[string]any))
	}
	return rs
}

func toolNames(f *fixture, tok string) []string {
	f.T.Helper()
	res := f.RPC(tok, "tools/list", map[string]any{})["result"].(map[string]any)
	var names []string
	for _, x := range res["tools"].([]any) {
		names = append(names, x.(map[string]any)["name"].(string))
	}
	return names
}

func TestToolsPerRole(t *testing.T) {
	f := setup(t)
	want := map[string]map[string]bool{
		f.lead:     {"list_help_requests": true, "accept_split": true, "request_help": false, "report_blocker": false, "suggest_split": false},
		f.worker:   {"list_help_requests": false, "accept_split": false, "request_help": true, "report_blocker": true, "suggest_split": true},
		f.tester:   {"list_help_requests": false, "accept_split": false, "request_help": true, "report_blocker": true, "suggest_split": true},
		f.reviewer: {"list_help_requests": false, "accept_split": false, "request_help": true, "report_blocker": false, "suggest_split": true},
	}
	for tok, tools := range want {
		names := toolNames(f, tok)
		for tool, listed := range tools {
			if slices.Contains(names, tool) != listed {
				t.Errorf("%s listed=%v, want %v", tool, !listed, listed)
			}
		}
	}
	f.MustFail(f.worker, "list_help_requests", map[string]any{}, "")
	f.MustFail(f.worker, "accept_split", map[string]any{"request_id": "HR-1"}, "")
	f.MustFail(f.lead, "request_help", map[string]any{"task_id": "T-1", "kind": "too_big", "details": "x"}, "")
	f.MustFail(f.reviewer, "report_blocker", map[string]any{"task_id": "T-1", "reason": "x", "needs": "owner"}, "")
}

func TestRequestHelp(t *testing.T) {
	f := setup(t)
	ids := f.plan(task("a", nil), task("b", nil))
	f.working(ids[0])

	args := map[string]any{"task_id": ids[0], "kind": "unclear_spec", "details": "which endpoint should this extend?"}
	req := f.Must(f.worker, "request_help", args)["request"].(map[string]any)
	if req["type"] != "help" || req["status"] != "open" || req["agent_id"] != "w1" || req["kind"] != "unclear_spec" {
		t.Fatalf("request: %v", req)
	}
	if got := f.taskOf(ids[0])["status"]; got != "in_progress" {
		t.Fatalf("status changed to %v", got)
	}
	if !hasBody(f.thread("task", ids[0]), "help requested (unclear_spec): which endpoint") {
		t.Fatalf("task thread: %v", f.thread("task", ids[0]))
	}
	if !hasBody(f.thread("brief", f.brief), "help requested (unclear_spec): which endpoint") {
		t.Fatalf("brief thread: %v", f.thread("brief", f.brief))
	}

	if rs := f.requests("open"); len(rs) != 1 || rs[0]["id"] != req["id"] {
		t.Fatalf("leader list: %v", rs)
	}
	_, rest := f.REST("GET", "/api/v1/help-requests?task_id="+ids[0], nil)
	if list, _ := rest["help_requests"].([]any); len(list) != 1 {
		t.Fatalf("rest list: %v", rest)
	}

	_, dec := f.REST("GET", "/api/v1/decisions", nil)
	var found map[string]any
	for _, d := range dec["decisions"].([]any) {
		if d.(map[string]any)["id"] == "help:"+req["id"].(string) {
			found = d.(map[string]any)
		}
	}
	if found == nil || found["kind"] != "help" || found["task_id"] != ids[0] || found["agent_id"] != "w1" {
		t.Fatalf("decision hint missing: %v", dec)
	}

	f.MustFail(f.worker, "request_help", map[string]any{"task_id": ids[0], "kind": "nope", "details": "x"}, "bad_request")
	f.MustFail(f.worker, "request_help", map[string]any{"task_id": ids[0], "kind": "too_big", "details": " "}, "bad_request")
	f.MustFail(f.worker, "request_help", map[string]any{"task_id": ids[1], "kind": "too_big", "details": "x"}, "not_found")
	f.MustFail(f.tester, "request_help", map[string]any{"task_id": ids[0], "kind": "too_big", "details": "x"}, "not_found")

	f.Must(f.lead, "update_task", map[string]any{"task_id": ids[0], "status": "cancelled"})
	if rs := f.requests("open"); len(rs) != 0 {
		t.Fatalf("closed task keeps open requests: %v", rs)
	}
	if rs := f.requests("closed"); len(rs) != 1 {
		t.Fatalf("closed list: %v", rs)
	}
	f.MustFail(f.worker, "request_help", map[string]any{"task_id": ids[0], "kind": "too_big", "details": "more"}, "conflict")
}

func TestRequestHelpByTesterAndReviewer(t *testing.T) {
	f := setup(t)
	ids := f.plan(task("a", map[string]any{"role": "tester"}))
	f.Must(f.lead, "assign", map[string]any{"task_id": ids[0], "agent_id": "t1"})
	f.Must(f.tester, "request_help", map[string]any{"task_id": ids[0], "kind": "missing_access", "details": "no CI token"})
	f.Must(f.tester, "claim_task", map[string]any{"task_id": ids[0]})
	f.Must(f.tester, "submit", map[string]any{"task_id": ids[0], "summary": "done"})
	f.Must(f.lead, "assign", map[string]any{"task_id": ids[0], "agent_id": "r1", "as": "reviewer"})
	f.Must(f.reviewer, "request_help", map[string]any{"task_id": ids[0], "kind": "dependency_issue", "details": "needs T-9 first"})
	if rs := f.requests("open"); len(rs) != 2 {
		t.Fatalf("requests: %v", rs)
	}
}

func TestReportBlocker(t *testing.T) {
	f := setup(t)
	ids := f.plan(task("a", nil), task("b", nil))
	f.working(ids[0])

	req := f.Must(f.worker, "report_blocker", map[string]any{"task_id": ids[0], "reason": "API key missing", "needs": "leader"})["request"].(map[string]any)
	if req["type"] != "blocker" || req["needs"] != "leader" || req["status"] != "open" {
		t.Fatalf("request: %v", req)
	}
	tk := f.taskOf(ids[0])
	if tk["status"] != "blocked" || !strings.Contains(tk["blocked_reason"].(string), "API key missing") {
		t.Fatalf("task: %v", tk)
	}
	if !hasBody(f.thread("agent", "rex"), "blocker (needs leader): API key missing") {
		t.Fatalf("leader thread: %v", f.thread("agent", "rex"))
	}
	if !hasBody(f.thread("task", ids[0]), "blocker (needs leader)") || !hasBody(f.thread("brief", f.brief), "blocker (needs leader)") {
		t.Fatal("blocker not announced on task and brief threads")
	}

	f.Must(f.lead, "update_task", map[string]any{"task_id": ids[0], "status": "ready"})
	if rs := f.requests("open"); len(rs) != 0 {
		t.Fatalf("unblocked task keeps open blocker: %v", rs)
	}
	if rs := f.requests("all"); len(rs) != 1 || rs[0]["status"] != "closed" {
		t.Fatalf("all: %v", rs)
	}

	f.working(ids[1])
	f.Must(f.worker, "report_blocker", map[string]any{"task_id": ids[1], "reason": "needs a budget decision", "needs": "owner"})
	if hasBody(f.thread("agent", "rex"), "needs a budget decision") {
		t.Fatal("owner blocker leaked to the leader's agent thread")
	}
	if !hasBody(f.thread("brief", f.brief), "blocker (needs owner): needs a budget decision") {
		t.Fatalf("brief thread: %v", f.thread("brief", f.brief))
	}
	_, dec := f.REST("GET", "/api/v1/decisions", nil)
	var unblock bool
	for _, d := range dec["decisions"].([]any) {
		unblock = unblock || d.(map[string]any)["id"] == "unblock:"+ids[1]
	}
	if !unblock {
		t.Fatalf("owner does not see the blocked task: %v", dec)
	}
}

func TestReportBlockerRules(t *testing.T) {
	f := setup(t)
	ids := f.plan(task("a", nil))
	f.Must(f.lead, "assign", map[string]any{"task_id": ids[0], "agent_id": "w1"})

	f.MustFail(f.worker, "report_blocker", map[string]any{"task_id": ids[0], "reason": "x", "needs": "leader"}, "conflict")
	f.Must(f.worker, "claim_task", map[string]any{"task_id": ids[0]})
	f.MustFail(f.worker, "report_blocker", map[string]any{"task_id": ids[0], "reason": "x", "needs": "everyone"}, "bad_request")
	f.MustFail(f.worker, "report_blocker", map[string]any{"task_id": ids[0], "reason": "", "needs": "owner"}, "bad_request")
	f.MustFail(f.tester, "report_blocker", map[string]any{"task_id": ids[0], "reason": "x", "needs": "owner"}, "not_assigned")
	if got := f.taskOf(ids[0])["status"]; got != "in_progress" {
		t.Fatalf("failed calls changed the status to %v", got)
	}
	if rs := f.requests("all"); len(rs) != 0 {
		t.Fatalf("failed calls recorded requests: %v", rs)
	}
}

func TestSuggestAndAcceptSplit(t *testing.T) {
	f := setup(t)
	ids := f.plan(task("base", nil), task("big", map[string]any{"depends_on": nil}), task("after", nil))
	big, after := ids[1], ids[2]
	f.Must(f.lead, "update_task", map[string]any{"task_id": big, "depends_on": []string{ids[0]}})
	f.Must(f.lead, "update_task", map[string]any{"task_id": after, "depends_on": []string{big}})
	f.working(ids[0])
	f.Must(f.lead, "assign", map[string]any{"task_id": big, "agent_id": "w1"})

	proposal := []any{
		task("schema", map[string]any{"ref": "schema"}),
		task("api", map[string]any{"ref": "api", "depends_on": []string{"schema"}, "role": "worker"}),
		task("tests", map[string]any{"ref": "tests", "depends_on": []string{"schema"}, "role": "tester"}),
	}
	req := f.Must(f.worker, "suggest_split", map[string]any{"task_id": big, "proposed_tasks": proposal})["request"].(map[string]any)
	if req["type"] != "split" || req["status"] != "open" || len(req["proposed_tasks"].([]any)) != 3 {
		t.Fatalf("request: %v", req)
	}
	if got := f.taskOf(big)["status"]; got != "assigned" {
		t.Fatalf("suggest changed the status to %v", got)
	}
	if !hasBody(f.thread("task", big), "split proposed: 3 tasks: schema; api; tests") {
		t.Fatalf("task thread: %v", f.thread("task", big))
	}

	rs := f.requests("open")
	if len(rs) != 1 || rs[0]["id"] != req["id"] {
		t.Fatalf("leader list: %v", rs)
	}

	f.MustFail(f.worker, "accept_split", map[string]any{"request_id": req["id"]}, "")
	f.MustFail(f.lead, "accept_split", map[string]any{"request_id": "HR-999"}, "not_found")

	res := f.Must(f.lead, "accept_split", map[string]any{"request_id": req["id"]})
	created := res["tasks"].([]any)
	if len(created) != 3 {
		t.Fatalf("created: %v", res)
	}
	byTitle := map[string]map[string]any{}
	for _, c := range created {
		m := c.(map[string]any)
		byTitle[m["title"].(string)] = m
		if m["status"] != "ready" || m["brief_id"] != f.brief {
			t.Fatalf("new task: %v", m)
		}
	}
	idOf := func(title string) string { return byTitle[title]["id"].(string) }
	deps := func(title string) []any { return byTitle[title]["depends_on"].([]any) }
	if d := deps("schema"); len(d) != 1 || d[0] != ids[0] {
		t.Fatalf("root keeps the original's dependencies: %v", d)
	}
	if d := deps("api"); len(d) != 1 || d[0] != idOf("schema") {
		t.Fatalf("api deps: %v", d)
	}
	if byTitle["tests"]["role"] != "tester" {
		t.Fatalf("tests role: %v", byTitle["tests"])
	}

	if got := f.taskOf(big)["status"]; got != "cancelled" {
		t.Fatalf("original is %v", got)
	}
	afterDeps := f.taskOf(after)["depends_on"].([]any)
	var got []string
	for _, d := range afterDeps {
		got = append(got, d.(string))
	}
	slices.Sort(got)
	want := []string{idOf("api"), idOf("tests")}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("dependent not rewired to the leaves: %v want %v", got, want)
	}
	if rw := res["rewired"].([]any); len(rw) != 1 || rw[0] != after {
		t.Fatalf("rewired: %v", rw)
	}

	if rs := f.requests("accepted"); len(rs) != 1 || len(rs[0]["created_tasks"].([]any)) != 3 {
		t.Fatalf("accepted list: %v", rs)
	}
	if rs := f.requests("open"); len(rs) != 0 {
		t.Fatalf("open list: %v", rs)
	}
	f.MustFail(f.lead, "accept_split", map[string]any{"request_id": req["id"]}, "conflict")

	_, audit := f.REST("GET", "/api/v1/audit?tool=accept_split", nil)
	if evs := audit["events"].([]any); len(evs) != 1 {
		t.Fatalf("audit: %v", audit)
	}
	if !hasBody(f.thread("brief", f.brief), "split accepted: "+big+" replaced by") {
		t.Fatalf("brief thread: %v", f.thread("brief", f.brief))
	}
}

func TestSplitProposalValidation(t *testing.T) {
	f := setup(t)
	ids := f.plan(task("big", nil), task("downstream", nil))
	f.Must(f.lead, "update_task", map[string]any{"task_id": ids[1], "depends_on": []string{ids[0]}})
	f.working(ids[0])
	call := func(p ...any) {
		f.T.Helper()
		f.MustFail(f.worker, "suggest_split", map[string]any{"task_id": ids[0], "proposed_tasks": p}, "bad_request")
	}

	call(task("only one", nil))
	call(task("a", map[string]any{"ref": "a"}), task("b", map[string]any{"ref": "a"}))
	call(task("a", map[string]any{"ref": "a", "depends_on": []string{"b"}}), task("b", map[string]any{"ref": "b", "depends_on": []string{"a"}}))
	call(task("a", map[string]any{"depends_on": []string{"T-999"}}), task("b", nil))
	call(task("a", map[string]any{"depends_on": []string{ids[0]}}), task("b", nil))
	call(task("a", map[string]any{"depends_on": []string{ids[1]}}), task("b", nil))
	call(task("a", map[string]any{"role": "reviewer"}), task("b", nil))
	call(task("a", map[string]any{"spec": " "}), task("b", nil))
	if rs := f.requests("all"); len(rs) != 0 {
		t.Fatalf("invalid proposals were stored: %v", rs)
	}
}

func TestNewSplitSupersedesOpenOne(t *testing.T) {
	f := setup(t)
	ids := f.plan(task("big", nil))
	f.working(ids[0])
	first := f.Must(f.worker, "suggest_split", map[string]any{"task_id": ids[0], "proposed_tasks": []any{task("a", nil), task("b", nil)}})["request"].(map[string]any)
	second := f.Must(f.worker, "suggest_split", map[string]any{"task_id": ids[0], "proposed_tasks": []any{task("c", nil), task("d", nil)}})["request"].(map[string]any)

	if rs := f.requests("open"); len(rs) != 1 || rs[0]["id"] != second["id"] {
		t.Fatalf("open: %v", rs)
	}
	if rs := f.requests("superseded"); len(rs) != 1 || rs[0]["id"] != first["id"] {
		t.Fatalf("superseded: %v", rs)
	}
	f.MustFail(f.lead, "accept_split", map[string]any{"request_id": first["id"]}, "conflict")
}

func TestAcceptSplitFailureLeavesNoTasks(t *testing.T) {
	f := setup(t)
	ids := f.plan(task("big", nil))
	f.working(ids[0])
	req := f.Must(f.worker, "suggest_split", map[string]any{"task_id": ids[0], "proposed_tasks": []any{task("a", nil), task("b", nil)}})["request"].(map[string]any)

	code, _ := f.REST("PATCH", "/api/v1/briefs/"+f.brief, map[string]any{"status": "closed"})
	if code != 200 {
		t.Fatalf("close brief: %d", code)
	}
	f.MustFail(f.lead, "accept_split", map[string]any{"request_id": req["id"]}, "conflict")
	if rs := f.requests("open"); len(rs) != 1 {
		t.Fatalf("proposal should stay open: %v", rs)
	}
	_, tasks := f.REST("GET", "/api/v1/tasks?brief_id="+f.brief, nil)
	if n := len(tasks["tasks"].([]any)); n != 1 {
		t.Fatalf("tasks created despite failure: %d", n)
	}
}

func TestLoopGuardRateLimitsHelpTools(t *testing.T) {
	f := setup(t)
	ids := f.plan(task("a", nil))
	f.working(ids[0])
	args := map[string]any{"task_id": ids[0], "kind": "too_big", "details": "same again"}
	for i := 0; i < 5; i++ {
		f.Must(f.worker, "request_help", args)
	}
	f.MustFail(f.worker, "request_help", args, "agent_paused")
	_, a := f.REST("GET", "/api/v1/agents/w1", nil)
	if a["status"] != "paused" || fmt.Sprint(a["pause_reason"]) == "" {
		t.Fatalf("agent: %v", a)
	}
	f.MustFail(f.worker, "report_blocker", map[string]any{"task_id": ids[0], "reason": "x", "needs": "owner"}, "agent_paused")
}
