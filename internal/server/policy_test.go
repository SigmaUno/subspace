package server_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

type policyClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *policyClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *policyClock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

type policyEnv struct {
	*servertest.Env
	clock *policyClock
	tok   map[string]string
	tasks map[string]string
	brief string
}

func startPolicyEnv(t *testing.T, db string, clock *policyClock) *servertest.Env {
	t.Helper()
	return servertest.Start(t, server.Config{DBPath: db, PublicURL: "http://sub.test", Now: clock.Now,
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Hour})
}

func newPolicyEnv(t *testing.T) *policyEnv {
	t.Helper()
	t.Setenv("SUBSPACE_POLICY_SWEEP", "20ms")
	clock := &policyClock{now: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)}
	e := &policyEnv{Env: startPolicyEnv(t, servertest.DB(t), clock), clock: clock, tok: map[string]string{}, tasks: map[string]string{}}
	for _, a := range [][3]string{
		{"rex", "leader", "other"}, {"claude-1", "worker", "claude-code"}, {"codex-1", "worker", "codex"},
		{"tt1", "tester", "claude-code"}, {"tt2", "tester", "claude-code"}, {"rev", "reviewer", "codex"},
	} {
		e.tok[a[0]] = e.Register(a[0], a[1], a[2])
	}
	br := e.ok("POST", "/api/v1/briefs", 201, map[string]any{"title": "Policies", "body": "b", "repos": []string{"o/r"}, "max_cost_eur": 100})
	e.brief = br["id"].(string)
	specs := []map[string]any{
		{"ref": "sec", "title": "[security] harden auth", "spec": "s", "repo": "o/r"},
		{"ref": "deploy", "title": "deploy pipeline", "spec": "s", "repo": "o/r", "paths": []string{"deploy/ci"}},
		{"ref": "api1", "title": "api one", "spec": "s", "repo": "o/r", "component_id": "api"},
		{"ref": "api2", "title": "api two", "spec": "s", "repo": "o/r", "component_id": "api"},
		{"ref": "test1", "title": "tests one", "spec": "s", "repo": "o/r", "role": "tester"},
		{"ref": "test2", "title": "tests two", "spec": "s", "repo": "o/r", "role": "tester"},
		{"ref": "plain", "title": "plain", "spec": "s", "repo": "o/r"},
		{"ref": "deploy2", "title": "deploy docs", "spec": "s", "repo": "o/r", "paths": []string{"deploy/docs/**"}},
	}
	var anySpecs []any
	for _, s := range specs {
		anySpecs = append(anySpecs, s)
	}
	plan := e.Must(e.tok["rex"], "propose_plan", map[string]any{"brief_id": e.brief, "tasks": anySpecs})["plan"].(map[string]any)
	for i, x := range plan["tasks"].([]any) {
		e.tasks[specs[i]["ref"].(string)] = x.(map[string]any)["id"].(string)
	}
	e.ok("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", 200, map[string]any{})
	for _, doc := range []map[string]any{
		{"name": "security only to anthropic", "enabled": true, "rules": []any{map[string]any{"id": "route",
			"when": map[string]any{"actions": []string{"assign"}, "match": `"security" in task.labels`},
			"then": map[string]any{"effect": "route_to_provider", "provider": "anthropic"}}}},
		{"name": "weekends", "enabled": true, "timezone": "UTC", "rules": []any{map[string]any{"id": "weekend",
			"when": map[string]any{"actions": []string{"assign"}, "match": `time.weekday in ["sat", "sun"] && target.tester_tasks == 0`},
			"then": map[string]any{"effect": "deny", "message": "no weekend assignments to agents without a tester task"}}}},
		{"name": "deploy needs the owner", "enabled": true, "rules": []any{map[string]any{"id": "deploy",
			"when": map[string]any{"actions": []string{"assign"}, "match": `overlaps(task.paths, "deploy/**")`},
			"then": map[string]any{"effect": "require_owner_approval", "timeout": "1h", "message": "touches deploy/**"}}}},
		{"name": "api budget", "enabled": true, "rules": []any{map[string]any{"id": "api-cap",
			"when": map[string]any{"actions": []string{"assign", "claim_task"}, "match": `task.component == "api" && cost.component_used >= 5`},
			"then": map[string]any{"effect": "deny", "message": "component api spent its 5 EUR"}}}},
	} {
		e.ok("POST", "/api/v1/policies", 201, doc)
	}
	return e
}

func (e *policyEnv) ok(method, path string, want int, body any) map[string]any {
	e.T.Helper()
	code, out := e.REST(method, path, body)
	if code != want {
		e.T.Fatalf("%s %s: %d %v", method, path, code, out)
	}
	return out
}

func (e *policyEnv) assign(task, agent, code string) {
	e.T.Helper()
	args := map[string]any{"task_id": e.tasks[task], "agent_id": agent}
	if code == "" {
		e.Must(e.tok["rex"], "assign", args)
		return
	}
	e.MustFail(e.tok["rex"], "assign", args, code)
}

func (e *policyEnv) status(task string) string {
	e.T.Helper()
	return e.ok("GET", "/api/v1/tasks/"+e.tasks[task], 200, nil)["status"].(string)
}

func (e *policyEnv) holds(status string) []map[string]any {
	e.T.Helper()
	var out []map[string]any
	for _, h := range e.ok("GET", "/api/v1/policy-holds?status="+status, 200, nil)["holds"].([]any) {
		out = append(out, h.(map[string]any))
	}
	return out
}

func (e *policyEnv) thread(kind, id string) string {
	e.T.Helper()
	var b strings.Builder
	for _, m := range e.ok("GET", "/api/v1/threads/"+kind+"/"+id+"/messages", 200, nil)["messages"].([]any) {
		b.WriteString(m.(map[string]any)["body"].(string) + "\n")
	}
	return b.String()
}

func TestPolicyEnforcementMatrix(t *testing.T) {
	e := newPolicyEnv(t)

	out, isErr := e.Call(e.tok["rex"], "assign", map[string]any{"task_id": e.tasks["sec"], "agent_id": "codex-1"})
	if !isErr || out["error"].(map[string]any)["code"] != "policy_denied" || !strings.Contains(out["error"].(map[string]any)["message"].(string), "anthropic") {
		t.Fatalf("route denial: %v", out)
	}
	e.assign("sec", "claude-1", "")
	e.assign("test1", "tt1", "")
	e.assign("deploy", "codex-1", "policy_hold")
	if got := e.status("deploy"); got != "ready" {
		t.Fatalf("held task is %s", got)
	}
	e.assign("api1", "codex-1", "")
	e.Must(e.tok["codex-1"], "claim_task", map[string]any{"task_id": e.tasks["api1"]})
	e.Must(e.tok["codex-1"], "update_status", map[string]any{"task_id": e.tasks["api1"], "status": "in_progress", "cost_eur": 6})
	e.assign("api2", "claude-1", "policy_denied")
	e.assign("plain", "claude-1", "")

	e.clock.Set(time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC))
	e.assign("test2", "tt2", "policy_denied")
	e.assign("test2", "tt1", "")
	e.Must(e.tok["rex"], "update_task", map[string]any{"task_id": e.tasks["plain"], "status": "blocked"})
	e.Must(e.tok["rex"], "update_task", map[string]any{"task_id": e.tasks["plain"], "status": "ready"})
	e.MustFail(e.tok["rex"], "reassign", map[string]any{"task_id": e.tasks["plain"], "agent_id": "codex-1", "reason": "x"}, "policy_denied")

	frozen := e.ok("POST", "/api/v1/briefs", 201, map[string]any{"title": "Frozen", "body": "b", "repos": []string{"o/r"}})
	plan := e.Must(e.tok["rex"], "propose_plan", map[string]any{"brief_id": frozen["id"], "tasks": []any{
		map[string]any{"title": "x", "spec": "s", "repo": "o/r"},
	}})["plan"].(map[string]any)
	e.ok("POST", "/api/v1/policies", 201, map[string]any{"name": "freeze", "enabled": true, "rules": []any{map[string]any{"id": "freeze",
		"when": map[string]any{"actions": []string{"approve_plan"}, "match": `brief.title == "Frozen"`},
		"then": map[string]any{"effect": "deny", "message": "brief is frozen"}}}})
	code, res := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{})
	if code != 403 || res["error"].(map[string]any)["code"] != "policy_denied" {
		t.Fatalf("approve frozen plan: %d %v", code, res)
	}
	if p := e.ok("GET", "/api/v1/plans/"+plan["id"].(string), 200, nil); p["status"] != "proposed" {
		t.Fatalf("denied approval changed the plan: %v", p)
	}

	dry := e.ok("POST", "/api/v1/policies/evaluate", 200, map[string]any{"action": "assign", "actor_id": "rex",
		"task_id": e.tasks["sec"], "agent_id": "codex-1", "at": "2026-11-18T10:00:00Z"})
	if dry["outcome"] != "deny" || dry["facts"].(map[string]any)["subject.provider"] != "openai" {
		t.Fatalf("dry run: %v", dry)
	}
	dry = e.ok("POST", "/api/v1/policies/evaluate", 200, map[string]any{"action": "assign", "task_id": e.tasks["plain"],
		"agent_id": "claude-1", "facts": map[string]any{"time.weekday": "mon"},
		"policy": map[string]any{"name": "draft", "rules": []any{map[string]any{"id": "w",
			"when": map[string]any{"actions": []string{"assign"}, "match": `time.weekday == "mon" && target.provider == "anthropic"`},
			"then": map[string]any{"effect": "warn"}}}}})
	if dry["outcome"] != "warn" {
		t.Fatalf("draft dry run: %v", dry)
	}
	if code, _ := e.REST("POST", "/api/v1/policies/evaluate", map[string]any{"action": "assign", "facts": map[string]any{"task.nope": 1}}); code != 400 {
		t.Fatalf("unknown fact override: %d", code)
	}

	counts := map[string]int{}
	for _, d := range e.ok("GET", "/api/v1/policies/decisions?limit=1000", 200, nil)["decisions"].([]any) {
		counts[d.(map[string]any)["outcome"].(string)]++
	}
	if counts["deny"] != 5 || counts["hold"] != 1 {
		t.Fatalf("decision history: %v", counts)
	}
	if n := len(e.ok("GET", "/api/v1/policies/decisions?outcome=deny&action=approve_plan", 200, nil)["decisions"].([]any)); n != 1 {
		t.Fatalf("filtered history: %d", n)
	}
}

func TestPolicyHoldApproveRejectExpire(t *testing.T) {
	e := newPolicyEnv(t)

	e.assign("deploy", "codex-1", "policy_hold")
	e.assign("deploy", "codex-1", "policy_hold")
	pending := e.holds("pending")
	if len(pending) != 1 || pending[0]["task_id"] != e.tasks["deploy"] || pending[0]["actor_id"] != "rex" {
		t.Fatalf("pending holds: %v", pending)
	}
	id := pending[0]["id"].(string)
	feed := e.ok("GET", "/api/v1/decisions", 200, nil)["decisions"].([]any)
	found := false
	for _, d := range feed {
		found = found || d.(map[string]any)["id"] == "policy:"+id
	}
	if !found {
		t.Fatalf("hold %s missing from the decision feed: %v", id, feed)
	}

	h := e.ok("POST", "/api/v1/policy-holds/"+id+"/approve", 200, map[string]any{"note": "ship it"})
	if h["status"] != "consumed" || !strings.Contains(h["result"].(string), "assigned") {
		t.Fatalf("approved hold: %v", h)
	}
	task := e.ok("GET", "/api/v1/tasks/"+e.tasks["deploy"], 200, nil)
	if task["status"] != "assigned" || task["assignee"] != "codex-1" {
		t.Fatalf("task after approval: %v", task)
	}
	if !strings.Contains(e.thread("agent", "rex"), "Owner approved "+id) {
		t.Fatalf("leader not told about the approval: %q", e.thread("agent", "rex"))
	}
	if code, _ := e.REST("POST", "/api/v1/policy-holds/"+id+"/approve", map[string]any{}); code != 409 {
		t.Fatalf("approving twice: %d", code)
	}

	e.assign("deploy2", "claude-1", "policy_hold")
	rid := e.holds("pending")[0]["id"].(string)
	if h := e.ok("POST", "/api/v1/policy-holds/"+rid+"/reject", 200, map[string]any{"note": "not now"}); h["status"] != "rejected" {
		t.Fatalf("rejected hold: %v", h)
	}
	if got := e.status("deploy2"); got != "ready" {
		t.Fatalf("rejected task is %s", got)
	}
	if !strings.Contains(e.thread("agent", "rex"), "Owner rejected "+rid+" (assign on "+e.tasks["deploy2"]+"): not now") {
		t.Fatalf("leader not told about the rejection: %q", e.thread("agent", "rex"))
	}

	e.assign("deploy2", "claude-1", "policy_hold")
	xid := e.holds("pending")[0]["id"].(string)
	if xid == rid {
		t.Fatalf("retry after rejection reused %s", rid)
	}
	e.clock.Set(e.clock.Now().Add(2 * time.Hour))
	waitFor(t, "hold expiry", func() bool {
		h := e.ok("GET", "/api/v1/policy-holds/"+xid, 200, nil)
		return h["status"] == "expired" && h["decided_at"] != nil
	})
	if code, _ := e.REST("POST", "/api/v1/policy-holds/"+xid+"/approve", map[string]any{}); code != 409 {
		t.Fatalf("approving an expired hold: %d", code)
	}
	if got := e.status("deploy2"); got != "ready" {
		t.Fatalf("expired task is %s", got)
	}
	if !strings.Contains(e.thread("agent", "rex"), xid+" (assign on "+e.tasks["deploy2"]+") expired") {
		t.Fatalf("leader not told about the expiry: %q", e.thread("agent", "rex"))
	}
}

func TestPolicyPersistsAcrossRestart(t *testing.T) {
	t.Setenv("SUBSPACE_POLICY_SWEEP", "20ms")
	db := servertest.DB(t)
	clock := &policyClock{now: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)}
	e := startPolicyEnv(t, db, clock)
	lead := e.Register("rex", "leader", "other")
	e.Register("codex-1", "worker", "codex")
	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "P", "body": "b", "repos": []string{"o/r"}})
	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": br["id"], "tasks": []any{
		map[string]any{"title": "[security] one", "spec": "s", "repo": "o/r"},
		map[string]any{"title": "deploy", "spec": "s", "repo": "o/r", "paths": []string{"deploy/x"}},
	}})["plan"].(map[string]any)
	e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{})
	doc := map[string]any{"name": "rules", "enabled": true, "rules": []any{
		map[string]any{"id": "route", "when": map[string]any{"actions": []string{"assign"}, "match": `"security" in task.labels`},
			"then": map[string]any{"effect": "route_to_provider", "provider": "anthropic"}},
	}}
	code, p := e.REST("POST", "/api/v1/policies", doc)
	if code != 201 {
		t.Fatalf("create: %d %v", code, p)
	}
	pid := p["id"].(string)
	doc["rules"] = append(doc["rules"].([]any), map[string]any{"id": "deploy",
		"when": map[string]any{"actions": []string{"assign"}, "match": `overlaps(task.paths, "deploy/**")`},
		"then": map[string]any{"effect": "require_owner_approval"}})
	if code, p = e.REST("PUT", "/api/v1/policies/"+pid, doc); code != 200 || p["version"] != float64(2) {
		t.Fatalf("replace: %d %v", code, p)
	}
	e.MustFail(lead, "assign", map[string]any{"task_id": "T-2", "agent_id": "codex-1"}, "policy_hold")
	e.Stop()

	e = startPolicyEnv(t, db, clock)
	e.MustFail(lead, "assign", map[string]any{"task_id": "T-1", "agent_id": "codex-1"}, "policy_denied")
	_, vs := e.REST("GET", "/api/v1/policies/"+pid+"/versions", nil)
	if n := len(vs["versions"].([]any)); n != 2 {
		t.Fatalf("versions after restart: %v", vs)
	}
	_, hs := e.REST("GET", "/api/v1/policy-holds?status=pending", nil)
	list := hs["holds"].([]any)
	if len(list) != 1 {
		t.Fatalf("pending holds after restart: %v", hs)
	}
	id := list[0].(map[string]any)["id"].(string)
	if code, h := e.REST("POST", "/api/v1/policy-holds/"+id+"/approve", map[string]any{}); code != 200 || h["status"] != "consumed" {
		t.Fatalf("approve after restart: %d %v", code, h)
	}
	if _, task := e.REST("GET", "/api/v1/tasks/T-2", nil); task["assignee"] != "codex-1" {
		t.Fatalf("task after release: %v", task)
	}
	if code, _ := e.REST("DELETE", "/api/v1/policies/"+pid, nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	e.Stop()

	e = startPolicyEnv(t, db, clock)
	e.Register("claude-1", "worker", "claude-code")
	e.Must(lead, "reassign", map[string]any{"task_id": "T-2", "agent_id": "claude-1", "reason": "x"})
	e.Must(lead, "assign", map[string]any{"task_id": "T-1", "agent_id": "codex-1"})
}

func TestPolicyCRUDValidationAndLint(t *testing.T) {
	e := servertest.New(t)
	code, out := e.REST("POST", "/api/v1/policies", map[string]any{"name": "bad", "rules": []any{map[string]any{"id": "r",
		"when": map[string]any{"actions": []string{"assign"}, "match": `task.labelz == "x"`}, "then": map[string]any{"effect": "deny"}}}})
	if code != 400 || !strings.Contains(out["error"].(map[string]any)["message"].(string), `unknown fact "task.labelz"`) {
		t.Fatalf("unknown fact: %d %v", code, out)
	}
	code, out = e.REST("POST", "/api/v1/policies/lint", map[string]any{"name": "w", "rules": []any{map[string]any{"id": "r",
		"when": map[string]any{"actions": []string{"assign"}, "match": `1 == 1`}, "then": map[string]any{"effect": "warn"}}}})
	if code != 200 || out["ok"] != true || !strings.Contains(fmt.Sprint(out["findings"]), "always_true") {
		t.Fatalf("lint: %d %v", code, out)
	}
	code, out = e.REST("POST", "/api/v1/policies", map[string]any{"name": "w", "rules": []any{map[string]any{"id": "r",
		"when": map[string]any{"actions": []string{"assign"}, "match": `1 == 1`}, "then": map[string]any{"effect": "warn"}}}})
	if code != 201 || out["enabled"] != false || out["version"] != float64(1) || len(out["warnings"].([]any)) == 0 {
		t.Fatalf("create with warnings: %d %v", code, out)
	}
	_, facts := e.REST("GET", "/api/v1/policies/facts", nil)
	if len(facts["facts"].([]any)) < 40 || len(facts["actions"].([]any)) != 9 {
		t.Fatalf("facts: %v", facts)
	}
	if code, _ := e.REST("GET", "/api/v1/policies/PO-404", nil); code != 404 {
		t.Fatalf("missing policy: %d", code)
	}
	_, audit := e.REST("GET", "/api/v1/audit?tool=create_policy", nil)
	if len(audit["events"].([]any)) != 1 {
		t.Fatalf("audit: %v", audit)
	}
}
