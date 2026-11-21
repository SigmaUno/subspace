package playbooks_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/servertest"
)

const path = "/api/v1/playbooks"

func rest(t *testing.T, e *servertest.Env, want int, method, path string, body any) map[string]any {
	t.Helper()
	code, out := e.REST(method, path, body)
	if code != want {
		t.Fatalf("%s %s: %d, want %d: %v", method, path, code, want, out)
	}
	return out
}

func names(t *testing.T, e *servertest.Env) []string {
	t.Helper()
	var out []string
	for _, p := range rest(t, e, 200, "GET", path, nil)["playbooks"].([]any) {
		out = append(out, p.(map[string]any)["name"].(string))
	}
	return out
}

func errorMessage(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	msg, _ := e["message"].(string)
	return msg
}

func TestBuiltinPlaybooksCannotBeDeletedButCanBeEditedAndCloned(t *testing.T) {
	e := servertest.New(t)
	want := []string{"feature", "hotfix", "performance", "refactor", "security-audit"}
	if got := names(t, e); !slices.Equal(got, want) {
		t.Fatalf("playbooks = %v, want %v", got, want)
	}
	for _, name := range want {
		out := rest(t, e, 409, "DELETE", path+"/"+name, nil)
		if !strings.Contains(errorMessage(out), "built-in") {
			t.Errorf("delete %s: %v", name, out)
		}
	}

	hotfix := rest(t, e, 200, "GET", path+"/hotfix", nil)
	if hotfix["builtin"] != true || hotfix["coverage"] != "tester" || hotfix["brief_cost_multiplier"] != 0.5 {
		t.Fatalf("hotfix = %v", hotfix)
	}
	hotfix["description"] = "Edited hotfix"
	hotfix["allowed_sizes"] = []string{"s"}
	hotfix["builtin"] = false
	edited := rest(t, e, 200, "PUT", path+"/hotfix", hotfix)
	if edited["description"] != "Edited hotfix" || edited["builtin"] != true {
		t.Fatalf("edited = %v", edited)
	}
	rest(t, e, 409, "DELETE", path+"/hotfix", nil)

	clone := rest(t, e, 201, "POST", path+"/hotfix/clone", map[string]any{"name": "hotfix-strict", "description": "Strict"})
	if clone["builtin"] != false || clone["description"] != "Strict" || fmt.Sprint(clone["allowed_sizes"]) != "[s]" {
		t.Fatalf("clone = %v", clone)
	}
	rest(t, e, 409, "POST", path+"/hotfix/clone", map[string]any{"name": "hotfix-strict"})
	rest(t, e, 404, "POST", path+"/missing/clone", map[string]any{"name": "x"})
	rest(t, e, 400, "POST", path+"/hotfix/clone", map[string]any{"name": "Bad Name"})
	rest(t, e, 204, "DELETE", path+"/hotfix-strict", nil)
	rest(t, e, 404, "GET", path+"/hotfix-strict", nil)
	rest(t, e, 404, "DELETE", path+"/hotfix-strict", nil)

	audit := rest(t, e, 200, "GET", "/api/v1/audit", nil)["events"].([]any)
	var tools []string
	for _, ev := range audit {
		tool := ev.(map[string]any)["tool"].(string)
		if strings.HasSuffix(tool, "_playbook") {
			tools = append(tools, tool)
		}
	}
	for _, want := range []string{"create_playbook", "update_playbook", "delete_playbook"} {
		if !slices.Contains(tools, want) {
			t.Errorf("no %s audit event in %v", want, tools)
		}
	}
}

func TestPlaybookValidation(t *testing.T) {
	e := servertest.New(t)
	with := func(k string, v any) map[string]any { return map[string]any{"name": "mine", k: v} }
	for name, body := range map[string]map[string]any{
		"bad name":        {"name": "Bad Name"},
		"missing name":    {},
		"bad coverage":    with("coverage", "all"),
		"bad pattern":     with("criteria_rules", []any{map[string]any{"name": "x", "pattern": "("}}),
		"bad severity":    with("required_tasks", []any{map[string]any{"name": "x", "pattern": "x", "severity": "fatal"}}),
		"bad size":        with("allowed_sizes", []string{"giant"}),
		"bad multiplier":  with("brief_cost_multiplier", 100),
		"negative budget": with("default_task_budget", map[string]any{"max_minutes": -5}),
		"unknown field":   with("flavour", "x"),
	} {
		if out := rest(t, e, 400, "POST", path, body); out["error"] == nil {
			t.Errorf("%s: %v", name, out)
		}
	}
	rest(t, e, 400, "PUT", path+"/feature", map[string]any{"name": "other"})
	rest(t, e, 404, "PUT", path+"/missing", map[string]any{"name": "missing"})
	created := rest(t, e, 201, "POST", path, map[string]any{"name": "mine", "required_tasks": []any{map[string]any{"name": "docs", "pattern": "docs"}}})
	if created["builtin"] != false || created["coverage"] != "" {
		t.Fatalf("created = %v", created)
	}
	if rt := created["required_tasks"].([]any)[0].(map[string]any); rt["severity"] != "error" {
		t.Fatalf("severity not defaulted: %v", rt)
	}
	rest(t, e, 409, "POST", path, map[string]any{"name": "mine"})
}

type fixture struct {
	*servertest.Env
	lead string
}

func start(t *testing.T) *fixture {
	t.Helper()
	e := servertest.New(t)
	return &fixture{Env: e, lead: e.Register("rex", "leader", "other")}
}

func (f *fixture) brief(t *testing.T, playbook string, extra map[string]any) map[string]any {
	t.Helper()
	body := map[string]any{"title": "Fix it", "repos": []string{"o/r"}, "max_cost_eur": 100}
	if playbook != "" {
		body["playbook"] = playbook
	}
	for k, v := range extra {
		body[k] = v
	}
	return rest(t, f.Env, 201, "POST", "/api/v1/briefs", body)
}

func task(ref, title string, role string, minutes int, deps []string, criteria ...string) map[string]any {
	t := map[string]any{"ref": ref, "title": title, "spec": "Implement " + title + " and run the tests", "repo": "o/r", "role": role,
		"acceptance_criteria": criteria, "depends_on": deps}
	if minutes > 0 {
		t["budget"] = map[string]any{"max_minutes": minutes}
	}
	return t
}

func TestBriefPlaybookSelectionAndCrossReview(t *testing.T) {
	f := start(t)
	plain := f.brief(t, "", nil)
	if plain["playbook"] != "" || plain["cross_review"] != true {
		t.Fatalf("plain brief = %v", plain)
	}
	hot := f.brief(t, "hotfix", nil)
	if hot["playbook"] != "hotfix" || hot["cross_review"] != true || hot["budget"].(map[string]any)["max_cost_eur"] != 50.0 {
		t.Fatalf("hotfix brief = %v", hot)
	}
	again := rest(t, f.Env, 200, "GET", "/api/v1/briefs/"+hot["id"].(string), nil)
	if again["playbook"] != "hotfix" {
		t.Fatalf("stored brief = %v", again)
	}
	audit := rest(t, f.Env, 200, "GET", "/api/v1/audit", nil)["events"].([]any)
	if !strings.Contains(fmt.Sprint(audit), "playbook:hotfix") {
		t.Errorf("create_brief audit lacks the playbook: %v", audit)
	}

	rest(t, f.Env, 400, "POST", "/api/v1/briefs", map[string]any{"title": "x", "playbook": "nope"})
	out := rest(t, f.Env, 400, "POST", "/api/v1/briefs", map[string]any{"title": "x", "playbook": "hotfix", "cross_review": false})
	if !strings.Contains(errorMessage(out), "cross_review") {
		t.Fatalf("cross_review false: %v", out)
	}
	rest(t, f.Env, 409, "PATCH", "/api/v1/briefs/"+hot["id"].(string), map[string]any{"cross_review": false})
	rest(t, f.Env, 200, "PATCH", "/api/v1/briefs/"+plain["id"].(string), map[string]any{"cross_review": false})

	loose := rest(t, f.Env, 201, "POST", path, map[string]any{"name": "loose", "coverage": "tester_or_reviewer"})
	if loose["cross_review"] != false {
		t.Fatalf("loose = %v", loose)
	}
	lb := f.brief(t, "loose", map[string]any{"cross_review": false})
	if lb["cross_review"] != false || lb["budget"].(map[string]any)["max_cost_eur"] != 100.0 {
		t.Fatalf("loose brief = %v", lb)
	}
	rest(t, f.Env, 409, "DELETE", path+"/loose", nil)
	rest(t, f.Env, 200, "PATCH", "/api/v1/briefs/"+lb["id"].(string), map[string]any{"status": "closed"})
	rest(t, f.Env, 409, "DELETE", path+"/loose", nil)
}

func TestProposePlanEnforcesPlaybook(t *testing.T) {
	f := start(t)
	bid := f.brief(t, "hotfix", nil)["id"].(string)
	regression := "A regression test reproduces the crash"

	f.MustFail(f.lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{
		task("fix", "fix crash", "worker", 60, nil, "Crash no longer happens"),
	}}, "bad_request")
	out, isErr := f.Call(f.lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{
		task("fix", "fix crash", "worker", 400, nil, "Crash no longer happens"),
	}})
	msg := errorMessage(out)
	if !isErr {
		t.Fatalf("plan accepted: %v", out)
	}
	for _, want := range []string{"playbook hotfix", "playbook_required_task", "playbook_coverage [fix]", "playbook_criteria [fix]", "playbook_task_size [fix]"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %s", want, msg)
		}
	}
	if plans := rest(t, f.Env, 200, "GET", "/api/v1/plans?brief_id="+bid, nil)["plans"].([]any); len(plans) != 0 {
		t.Fatalf("rejected plan was stored: %v", plans)
	}

	plan := f.Must(f.lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{
		task("fix", "fix crash", "worker", 0, nil, regression),
		task("undo", "rollback of the fix", "worker", 30, nil, "Regression test covers the rollback"),
		task("t1", "test fix", "tester", 30, []string{"fix"}, "Report lists failures"),
		task("t2", "test rollback", "tester", 30, []string{"undo"}, "Report lists failures"),
	}})["plan"].(map[string]any)
	tasks := plan["tasks"].([]any)
	first := tasks[0].(map[string]any)["budget"].(map[string]any)
	if first["max_minutes"] != 60.0 || first["max_cost_eur"] != 3.0 {
		t.Errorf("default budget not applied: %v", first)
	}
	if second := tasks[1].(map[string]any)["budget"].(map[string]any); second["max_minutes"] != 30.0 || second["max_cost_eur"] != 3.0 {
		t.Errorf("explicit minutes lost: %v", second)
	}
	if plan["status"] != "proposed" {
		t.Fatalf("plan = %v", plan)
	}
}

func TestPlaybookWarningsAreRecordedOnThePlan(t *testing.T) {
	f := start(t)
	bid := f.brief(t, "security-audit", nil)["id"].(string)
	plan := f.Must(f.lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{
		task("a", "review authz checks", "worker", 90, nil, "Every authz bypass is listed"),
	}})["plan"].(map[string]any)
	var rules []string
	for _, w := range plan["warnings"].([]any) {
		m := w.(map[string]any)
		if strings.HasPrefix(m["rule"].(string), "playbook_") {
			rules = append(rules, m["rule"].(string)+"/"+m["severity"].(string))
		}
	}
	if fmt.Sprint(rules) != "[playbook_required_task/warning]" {
		t.Fatalf("playbook warnings = %v (%v)", rules, plan["warnings"])
	}
	stored := rest(t, f.Env, 200, "GET", "/api/v1/plans/"+plan["id"].(string), nil)
	if len(stored["warnings"].([]any)) != len(plan["warnings"].([]any)) {
		t.Fatalf("stored warnings = %v", stored["warnings"])
	}
	rest(t, f.Env, 200, "POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{})
}

func TestCreateTaskGetsDefaultBudget(t *testing.T) {
	f := start(t)
	bid := f.brief(t, "performance", nil)["id"].(string)
	got := f.Must(f.lead, "create_task", map[string]any{"brief_id": bid, "title": "profile hot path", "spec": "profile the hot path", "repo": "o/r",
		"acceptance_criteria": []string{"p95 latency drops by 20%"}})["task"].(map[string]any)
	if b := got["budget"].(map[string]any); b["max_minutes"] != 120.0 || b["max_cost_eur"] != 6.0 {
		t.Fatalf("budget = %v", b)
	}
	plain := f.brief(t, "", nil)["id"].(string)
	got = f.Must(f.lead, "create_task", map[string]any{"brief_id": plain, "title": "profile hot path", "spec": "profile the hot path", "repo": "o/r"})["task"].(map[string]any)
	if b := got["budget"].(map[string]any); b["max_minutes"] != 0.0 {
		t.Fatalf("plain budget = %v", b)
	}
}

func TestCreateAndUpdateTaskEnforcePlaybookAfterApproval(t *testing.T) {
	f := start(t)
	bid := f.brief(t, "hotfix", nil)["id"].(string)
	regression := "A regression test reproduces the crash"
	plan := f.Must(f.lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{
		task("fix", "fix crash", "worker", 60, nil, regression),
		task("undo", "rollback of the fix", "worker", 30, nil, "Regression test covers the rollback"),
		task("t1", "test fix", "tester", 30, []string{"fix", "undo"}, "Report lists failures"),
	}})["plan"].(map[string]any)
	rest(t, f.Env, 200, "POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{})

	out, isErr := f.Call(f.lead, "create_task", map[string]any{"brief_id": bid, "title": "rewrite the parser", "spec": "rewrite it",
		"repo": "o/r", "acceptance_criteria": []string{"Parser is faster"}, "budget": map[string]any{"max_minutes": 900}})
	msg := errorMessage(out)
	if !isErr {
		t.Fatalf("create_task bypassed the playbook: %v", out)
	}
	for _, want := range []string{"playbook hotfix", "playbook_criteria", "playbook_task_size"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %s", want, msg)
		}
	}

	ok := f.Must(f.lead, "create_task", map[string]any{"brief_id": bid, "title": "guard nil config", "spec": "guard it", "repo": "o/r",
		"acceptance_criteria": []string{regression}})["task"].(map[string]any)
	id := ok["id"].(string)
	f.MustFail(f.lead, "update_task", map[string]any{"task_id": id, "acceptance_criteria": []string{"Looks fine"}}, "bad_request")
	f.Must(f.lead, "update_task", map[string]any{"task_id": id, "acceptance_criteria": []string{regression, "Docs updated"}})

	got := rest(t, f.Env, 200, "GET", "/api/v1/tasks/"+id, nil)
	if fmt.Sprint(got["acceptance_criteria"]) != "["+regression+" Docs updated]" {
		t.Fatalf("criteria = %v", got["acceptance_criteria"])
	}
}

func TestLeaderSeesPlaybookInBriefAndInstructions(t *testing.T) {
	f := start(t)
	hot := f.brief(t, "hotfix", nil)["id"].(string)
	plain := f.brief(t, "", nil)["id"].(string)

	got := f.Must(f.lead, "get_brief", map[string]any{"brief_id": hot})
	pb, _ := got["playbook"].(map[string]any)
	if pb["name"] != "hotfix" || pb["coverage"] != "tester" || !strings.Contains(pb["instructions"].(string), "rollback") {
		t.Fatalf("get_brief playbook = %v", got["playbook"])
	}
	if _, has := f.Must(f.lead, "get_brief", map[string]any{"brief_id": plain})["playbook"]; has {
		t.Fatal("brief without playbook returned one")
	}

	text := instructions(t, f)
	if !strings.Contains(text, "--- Playbook hotfix ("+hot+") ---") || !strings.Contains(text, "Hotfix: keep the change minimal") {
		t.Fatalf("instructions lack the playbook block:\n%s", text)
	}
	rest(t, f.Env, 200, "PATCH", "/api/v1/briefs/"+hot, map[string]any{"status": "closed"})
	if text := instructions(t, f); strings.Contains(text, "Playbook hotfix") {
		t.Fatalf("closed brief still in instructions:\n%s", text)
	}
	worker := f.Register("w1", "worker", "claude-code")
	f.brief(t, "feature", nil)
	if got := f.Must(worker, "get_instructions", map[string]any{})["instructions"].(string); strings.Contains(got, "Playbook") {
		t.Fatalf("worker instructions carry a playbook block:\n%s", got)
	}
}

func instructions(t *testing.T, f *fixture) string {
	t.Helper()
	return f.Must(f.lead, "get_instructions", map[string]any{})["instructions"].(string)
}

func importDoc(t *testing.T, e *servertest.Env, want int, query, doc string) map[string]any {
	t.Helper()
	req, err := http.NewRequest("POST", e.TS.URL+"/api/v1/briefs/import"+query, strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+servertest.OwnerToken)
	req.Header.Set("Content-Type", "text/markdown")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != want {
		t.Fatalf("import: %d, want %d: %v", resp.StatusCode, want, out)
	}
	return out
}

func TestImportReadsPlaybookFrontmatter(t *testing.T) {
	e := servertest.New(t)
	doc := "---\ntitle: \"Patch\"\nrepos: [o/r]\nmax_cost_eur: 40\nplaybook: hotfix\n---\nbody\n"
	preview := importDoc(t, e, 200, "?dry_run=true", doc)["preview"].(map[string]any)
	if preview["playbook"] != "hotfix" {
		t.Fatalf("preview = %v", preview)
	}
	br := importDoc(t, e, 201, "", doc)["brief"].(map[string]any)
	if br["playbook"] != "hotfix" || br["budget"].(map[string]any)["max_cost_eur"] != 20.0 {
		t.Fatalf("imported brief = %v", br)
	}
	importDoc(t, e, 400, "", strings.Replace(doc, "hotfix", "nope", 1))
	importDoc(t, e, 400, "", strings.Replace(doc, "playbook: hotfix", "playbook: \"\"", 1))
	importDoc(t, e, 400, "", strings.Replace(doc, "---\nbody", "cross_review: false\n---\nbody", 1))
}
