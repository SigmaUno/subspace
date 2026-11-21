package gates_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/prsync"
	"github.com/SigmaUno/subspace/internal/scope"
	"github.com/SigmaUno/subspace/internal/servertest"
)

type fixture struct {
	*servertest.Env
	lead, wk, rev, tst string
	n                  int
}

func newFixture(t *testing.T) *fixture {
	t.Setenv("SUBSPACE_PRSYNC_CONFIG", filepath.Join(t.TempDir(), "absent.json"))
	e := servertest.New(t)
	return &fixture{Env: e,
		lead: e.Register("rex", "leader", "other"),
		wk:   e.Register("claude-1", "worker", "claude-code"),
		rev:  e.Register("codex-r", "reviewer", "codex"),
		tst:  e.Register("tester-1", "tester", "opencode"),
	}
}

func (f *fixture) brief(body map[string]any) string {
	f.T.Helper()
	body["title"] = "Gated"
	code, br := f.REST("POST", "/api/v1/briefs", body)
	if code != 201 {
		f.T.Fatalf("create brief: %d %v", code, br)
	}
	return br["id"].(string)
}

func (f *fixture) prURL() string {
	f.n++
	return fmt.Sprintf("https://git.example/o/r/pulls/%d", f.n)
}

func (f *fixture) plan(briefID string, tasks ...map[string]any) []string {
	f.T.Helper()
	items := make([]any, len(tasks))
	for i, t := range tasks {
		t["repo"] = "o/r"
		if t["spec"] == nil {
			t["spec"] = "do it"
		}
		items[i] = t
	}
	plan := f.Must(f.lead, "propose_plan", map[string]any{"brief_id": briefID, "tasks": items})["plan"].(map[string]any)
	if code, out := f.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		f.T.Fatalf("approve plan: %d %v", code, out)
	}
	var ids []string
	for _, t := range plan["tasks"].([]any) {
		ids = append(ids, t.(map[string]any)["id"].(string))
	}
	return ids
}

func (f *fixture) work(briefID string, task map[string]any) (string, string) {
	f.T.Helper()
	if task == nil {
		task = map[string]any{}
	}
	task["title"] = "Work"
	id := f.plan(briefID, task)[0]
	f.Must(f.lead, "assign", map[string]any{"task_id": id, "agent_id": "claude-1"})
	f.Must(f.wk, "claim_task", map[string]any{"task_id": id})
	return id, f.prURL()
}

func (f *fixture) submit(id, pr string) {
	f.T.Helper()
	f.Must(f.wk, "submit", map[string]any{"task_id": id, "pr_url": pr, "summary": "done"})
}

func (f *fixture) testerReport(briefID, pr string, passed, failed int) {
	f.T.Helper()
	f.n++
	id := f.Must(f.lead, "create_task", map[string]any{"brief_id": briefID, "title": fmt.Sprintf("Test it %d", f.n), "spec": "run tests", "repo": "o/r", "role": "tester"})["task"].(map[string]any)["id"].(string)
	f.Must(f.lead, "assign", map[string]any{"task_id": id, "agent_id": "tester-1"})
	f.Must(f.tst, "claim_task", map[string]any{"task_id": id})
	f.Must(f.tst, "report_tests", map[string]any{"task_id": id, "passed": passed, "failed": failed})
	f.Must(f.tst, "submit", map[string]any{"task_id": id, "pr_url": pr, "summary": "tests"})
}

func (f *fixture) snapshot(taskID, briefID, pr string, mod func(*prsync.Status)) {
	f.T.Helper()
	now := time.Now().UTC()
	ok := true
	st := prsync.Status{TaskID: taskID, BriefID: briefID, URL: pr, Host: "gh", Repo: "o/r", Number: 1, State: prsync.StateOpen,
		Checks: prsync.ChecksSuccess, Mergeable: &ok, HeadSHA: "0123456789abcdef", Reviews: []prsync.Review{}, ReviewDecision: prsync.DecisionNone,
		Scope: &scope.Report{HeadSHA: "0123456789abcdef", Findings: []scope.Finding{}}, CheckedAt: &now, ChangedAt: now}
	if mod != nil {
		mod(&st)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		f.T.Fatal(err)
	}
	if err := f.Srv.Store.(prsync.Store).SetPRStatus(context.Background(), taskID, raw); err != nil {
		f.T.Fatal(err)
	}
	f.Srv.Hub.Publish([]string{"board", "task:" + taskID}, prsync.EventPR, st)
}

func (f *fixture) task(id string) map[string]any {
	f.T.Helper()
	_, out := f.REST("GET", "/api/v1/tasks/"+id, nil)
	return out
}

func (f *fixture) approve(id, comments string) (map[string]any, bool) {
	f.T.Helper()
	return f.Call(f.rev, "review", map[string]any{"task_id": id, "verdict": "approve", "comments": comments})
}

func failedGates(t *testing.T, out map[string]any) []string {
	t.Helper()
	e, _ := out["error"].(map[string]any)
	if e == nil || e["code"] != "gates_failed" {
		t.Fatalf("want gates_failed, got %v", out)
	}
	d := e["details"].(map[string]any)
	var names []string
	for _, g := range d["gates"].([]any) {
		names = append(names, g.(map[string]any)["gate"].(string))
	}
	return names
}

func TestReviewBlockedByEachGate(t *testing.T) {
	cases := []struct {
		gate   string
		cfg    map[string]any
		brief  map[string]any
		task   map[string]any
		block  func(f *fixture, briefID, id, pr string)
		fix    func(f *fixture, briefID, id, pr string)
		review string
	}{
		{gate: "tests", cfg: map[string]any{"tests": true, "min_tests_passed": 3},
			block: func(f *fixture, briefID, id, pr string) { f.testerReport(briefID, pr, 5, 1) },
			fix:   func(f *fixture, briefID, id, pr string) { f.testerReport(briefID, pr, 5, 0) }},
		{gate: "checks", cfg: map[string]any{"checks": true},
			block: func(f *fixture, briefID, id, pr string) {
				f.snapshot(id, briefID, pr, func(s *prsync.Status) { s.Checks = prsync.ChecksFailure })
			},
			fix: func(f *fixture, briefID, id, pr string) { f.snapshot(id, briefID, pr, nil) }},
		{gate: "scope", cfg: map[string]any{"scope": true},
			block: func(f *fixture, briefID, id, pr string) {
				f.snapshot(id, briefID, pr, func(s *prsync.Status) {
					s.Scope.Findings = []scope.Finding{{Rule: scope.RuleProtectedPath, Severity: scope.SeverityError, Message: "ci"}}
				})
			},
			fix: func(f *fixture, briefID, id, pr string) {
				f.snapshot(id, briefID, pr, func(s *prsync.Status) {
					s.Scope.Findings = []scope.Finding{{Rule: scope.RuleDiffTooLarge, Severity: scope.SeverityWarning, Message: "big"}}
				})
			}},
		{gate: "questions", cfg: map[string]any{"questions": true},
			block: func(f *fixture, briefID, id, pr string) {},
			fix: func(f *fixture, briefID, id, pr string) {
				_, qs := f.REST("GET", "/api/v1/questions?status=open", nil)
				for _, q := range qs["questions"].([]any) {
					qid := q.(map[string]any)["id"].(string)
					if code, out := f.REST("POST", "/api/v1/questions/"+qid+"/answer", map[string]any{"answer": "yes"}); code != 200 {
						f.T.Fatalf("answer: %d %v", code, out)
					}
				}
			}},
		{gate: "review", cfg: map[string]any{"review": true}, task: map[string]any{"acceptance_criteria": []string{"works", "tested"}},
			review: "criterion 1: pass\ncriterion 2: fail no test",
			block:  func(f *fixture, briefID, id, pr string) {},
			fix:    func(f *fixture, briefID, id, pr string) {}},
		{gate: "budget", cfg: map[string]any{"budget": true}, brief: map[string]any{"max_cost_eur": 10},
			block: func(f *fixture, briefID, id, pr string) {
				if code, out := f.REST("PATCH", "/api/v1/briefs/"+briefID, map[string]any{"max_cost_eur": 1}); code != 200 {
					f.T.Fatalf("patch: %d %v", code, out)
				}
			},
			fix: func(f *fixture, briefID, id, pr string) {
				f.REST("PATCH", "/api/v1/briefs/"+briefID, map[string]any{"max_cost_eur": 10})
			}},
		{gate: "pr_age", cfg: map[string]any{"max_pr_age_hours": 24},
			block: func(f *fixture, briefID, id, pr string) {
				f.snapshot(id, briefID, pr, func(s *prsync.Status) { s.ChangedAt = time.Now().Add(-48 * time.Hour) })
			},
			fix: func(f *fixture, briefID, id, pr string) {
				f.snapshot(id, briefID, pr, func(s *prsync.Status) { s.HeadSHA = "fedcba9876543210" })
			}},
	}
	for _, tc := range cases {
		t.Run(tc.gate, func(t *testing.T) {
			f := newFixture(t)
			body := map[string]any{"quality_gates": tc.cfg}
			for k, v := range tc.brief {
				body[k] = v
			}
			briefID := f.brief(body)
			id, pr := f.work(briefID, tc.task)
			if tc.gate == "questions" {
				f.Must(f.wk, "ask_owner", map[string]any{"task_id": id, "question": "which db?"})
			}
			if tc.gate == "budget" {
				f.Must(f.wk, "update_status", map[string]any{"task_id": id, "status": "in_progress", "cost_eur": 2})
			}
			f.submit(id, pr)
			tc.block(f, briefID, id, pr)

			out, isErr := f.approve(id, tc.review)
			if !isErr {
				t.Fatalf("approve must be held: %v", out)
			}
			if got := failedGates(t, out); len(got) != 1 || got[0] != tc.gate {
				t.Fatalf("failed gates %v, want [%s]", got, tc.gate)
			}
			if st := f.task(id)["status"]; st != "in_review" {
				t.Fatalf("status %v, want in_review", st)
			}
			_, rep := f.REST("GET", "/api/v1/tasks/"+id+"/gates", nil)
			if rep["passed"] != false || len(rep["gates"].([]any)) != 7 {
				t.Fatalf("gate report %v", rep)
			}

			tc.fix(f, briefID, id, pr)
			comments := "looks good"
			if tc.gate == "review" {
				comments = "criterion 1: pass\ncriterion 2: met"
			}
			if out := f.Must(f.rev, "review", map[string]any{"task_id": id, "verdict": "approve", "comments": comments}); out["task"].(map[string]any)["status"] != "approved" {
				t.Fatalf("approve after fix: %v", out)
			}
		})
	}
}

func TestOverrideUnblocksApproval(t *testing.T) {
	f := newFixture(t)
	briefID := f.brief(map[string]any{"quality_gates": map[string]any{"tests": true, "questions": true}})
	id, pr := f.work(briefID, nil)
	f.submit(id, pr)
	out, _ := f.approve(id, "ok")
	if got := failedGates(t, out); len(got) != 1 || got[0] != "tests" {
		t.Fatalf("failed %v", got)
	}

	viewer := func() string {
		_, tok := f.REST("POST", "/api/v1/tokens", map[string]any{"name": "ro", "scope": "viewer"})
		return tok["secret"].(string)
	}()
	if code, _ := f.RESTAs(viewer, "POST", "/api/v1/tasks/"+id+"/gates/override", map[string]any{"reason": "x"}); code != 403 {
		t.Fatalf("viewer override: %d", code)
	}
	if code, _ := f.RESTAs(viewer, "GET", "/api/v1/tasks/"+id+"/gates", nil); code != 200 {
		t.Fatalf("viewer read: %d", code)
	}
	for _, bad := range []map[string]any{{}, {"reason": "  "}, {"reason": "x", "gates": []string{"nope"}}, {"reason": "x", "extra": 1}} {
		if code, _ := f.REST("POST", "/api/v1/tasks/"+id+"/gates/override", bad); code != 400 {
			t.Fatalf("override %v: %d", bad, code)
		}
	}
	if code, _ := f.REST("POST", "/api/v1/tasks/T-999/gates/override", map[string]any{"reason": "x"}); code != 404 {
		t.Fatalf("unknown task: %d", code)
	}

	code, rep := f.REST("POST", "/api/v1/tasks/"+id+"/gates/override", map[string]any{"reason": "tests run in another repo", "gates": []string{"tests"}})
	if code != 200 || rep["passed"] != true || rep["override"].(map[string]any)["reason"] != "tests run in another repo" {
		t.Fatalf("override: %d %v", code, rep)
	}
	for _, g := range rep["gates"].([]any) {
		g := g.(map[string]any)
		if g["gate"] == "tests" && (g["overridden"] != true || g["passed"] != false) {
			t.Fatalf("tests gate %v", g)
		}
	}
	_, audit := f.REST("GET", "/api/v1/audit?tool=override_gates", nil)
	evs := audit["events"].([]any)
	if len(evs) != 1 || evs[0].(map[string]any)["task_id"] != id || !strings.Contains(evs[0].(map[string]any)["note"].(string), "another repo") {
		t.Fatalf("audit %v", evs)
	}
	f.Must(f.wk, "ask_owner", map[string]any{"task_id": id, "question": "still?"})
	out, _ = f.approve(id, "ok")
	if got := failedGates(t, out); len(got) != 1 || got[0] != "questions" {
		t.Fatalf("override must not cover questions: %v", got)
	}
	f.REST("POST", "/api/v1/tasks/"+id+"/gates/override", map[string]any{"reason": "owner accepts"})
	if out := f.Must(f.rev, "review", map[string]any{"task_id": id, "verdict": "approve", "comments": "ok"}); out["task"].(map[string]any)["status"] != "approved" {
		t.Fatalf("approve: %v", out)
	}
}

func TestSubmitWithoutCrossReviewWaitsForGates(t *testing.T) {
	f := newFixture(t)
	briefID := f.brief(map[string]any{"cross_review": false, "quality_gates": map[string]any{"checks": true}})
	id, pr := f.work(briefID, nil)
	out, isErr := f.Call(f.wk, "submit", map[string]any{"task_id": id, "pr_url": pr, "summary": "done"})
	if !isErr {
		t.Fatalf("submit must be held: %v", out)
	}
	if got := failedGates(t, out); len(got) != 1 || got[0] != "checks" {
		t.Fatalf("failed %v", got)
	}
	if st := f.task(id)["status"]; st != "submitted" {
		t.Fatalf("status %v", st)
	}
	f.snapshot(id, briefID, pr, func(s *prsync.Status) { s.Checks = prsync.ChecksPending })
	time.Sleep(100 * time.Millisecond)
	if st := f.task(id)["status"]; st != "submitted" {
		t.Fatalf("pending checks promoted the task: %v", st)
	}
	f.snapshot(id, briefID, pr, nil)
	deadline := time.Now().Add(5 * time.Second)
	for f.task(id)["status"] != "approved" {
		if time.Now().After(deadline) {
			t.Fatalf("task not approved after checks passed: %v", f.task(id))
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, audit := f.REST("GET", "/api/v1/audit?tool=gates_passed", nil)
	if evs := audit["events"].([]any); len(evs) != 1 {
		t.Fatalf("audit %v", evs)
	}
}

func TestMergeReadyNeedsGates(t *testing.T) {
	f := newFixture(t)
	briefID := f.brief(map[string]any{"max_cost_eur": 10, "quality_gates": map[string]any{"budget": true}})
	id, pr := f.work(briefID, nil)
	f.Must(f.wk, "update_status", map[string]any{"task_id": id, "status": "in_progress", "cost_eur": 2})
	f.submit(id, pr)
	f.Must(f.rev, "review", map[string]any{"task_id": id, "verdict": "approve", "comments": "ok"})
	f.snapshot(id, briefID, pr, func(s *prsync.Status) { s.MergeReady = true })
	mergeReady := func() bool {
		_, out := f.REST("GET", "/api/v1/decisions", nil)
		for _, d := range out["decisions"].([]any) {
			if d.(map[string]any)["id"] == "merge-ready:"+id {
				return true
			}
		}
		return false
	}
	if !mergeReady() {
		t.Fatal("merge-ready decision missing while gates pass")
	}
	f.REST("PATCH", "/api/v1/briefs/"+briefID, map[string]any{"max_cost_eur": 1})
	if mergeReady() {
		t.Fatal("merge-ready decision must disappear when a gate fails")
	}
}

func TestBriefGateSettings(t *testing.T) {
	f := newFixture(t)
	code, br := f.REST("POST", "/api/v1/briefs", map[string]any{"title": "Plain"})
	if code != 201 {
		t.Fatal(code)
	}
	if g := br["quality_gates"].(map[string]any); g["tests"] != false || g["max_pr_age_hours"] != 0.0 {
		t.Fatalf("defaults %v", g)
	}
	id := br["id"].(string)
	for _, bad := range []map[string]any{
		{"min_tests_passed": 3}, {"tests": true, "min_tests_passed": -1}, {"max_pr_age_hours": 9000},
	} {
		if code, _ := f.REST("PATCH", "/api/v1/briefs/"+id, map[string]any{"quality_gates": bad}); code != 400 {
			t.Fatalf("patch %v: %d", bad, code)
		}
	}
	code, br = f.REST("PATCH", "/api/v1/briefs/"+id, map[string]any{"quality_gates": map[string]any{"tests": true, "min_tests_passed": 3, "checks": true}})
	if code != 200 || br["quality_gates"].(map[string]any)["min_tests_passed"] != 3.0 {
		t.Fatalf("patch: %d %v", code, br)
	}
	_, br = f.REST("PATCH", "/api/v1/briefs/"+id, map[string]any{"max_parallel_agents": 4})
	if br["quality_gates"].(map[string]any)["checks"] != true {
		t.Fatalf("unrelated patch dropped gates: %v", br)
	}
	if got := f.Must(f.lead, "get_brief", map[string]any{"brief_id": id})["brief"].(map[string]any)["quality_gates"].(map[string]any); got["tests"] != true {
		t.Fatalf("get_brief: %v", got)
	}

	doc := "---\ntitle: Imported\nquality_gates:\n  tests: true\n  min_tests_passed: 2\n  review: true\n  max_pr_age_hours: 48\n---\nBody\n"
	code, imp := f.importDoc(doc)
	if code != 201 {
		t.Fatalf("import: %d %v", code, imp)
	}
	g := imp["brief"].(map[string]any)["quality_gates"].(map[string]any)
	if g["tests"] != true || g["min_tests_passed"] != 2.0 || g["review"] != true || g["max_pr_age_hours"] != 48.0 || g["checks"] != false {
		t.Fatalf("imported gates %v", g)
	}
	bad := "---\ntitle: Bad\nquality_gates:\n  coverage: true\n---\n"
	if code, out := f.importDoc(bad); code != 400 || !strings.Contains(out["error"].(map[string]any)["message"].(string), "coverage") {
		t.Fatalf("bad import: %d %v", code, out)
	}
}

func (f *fixture) importDoc(doc string) (int, map[string]any) {
	f.T.Helper()
	req, _ := http.NewRequest("POST", f.TS.URL+"/api/v1/briefs/import", strings.NewReader(doc))
	req.Header.Set("Authorization", "Bearer "+servertest.OwnerToken)
	req.Header.Set("Content-Type", "text/markdown")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.T.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}
