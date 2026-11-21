package server_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

func TestCreateTaskFromTemplateOverMCP(t *testing.T) {
	dir := t.TempDir()
	custom := "---\nid: custom-chore\ntitle: Custom chore\nvariables: [thing]\nkind: task\ntask_title: Chore {{thing}}\nrole: tester\nbudget_minutes: 15\nacceptance_criteria:\n  - {{thing}} is done\n---\nTidy {{thing}}.\n"
	if err := os.WriteFile(filepath.Join(dir, "custom.md"), []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	e := servertest.Start(t, server.Config{PublicURL: "http://sub.test", PromptsDir: dir,
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})
	lead := e.Register("rex", "leader", "other")
	worker := e.Register("w1", "worker", "claude-code")
	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B", "repos": []string{"o/r"}})
	bid := br["id"].(string)

	code, out := e.REST("GET", "/api/v1/task-templates", nil)
	if code != 200 {
		t.Fatalf("task-templates: %d", code)
	}
	var ids []string
	for _, x := range out["templates"].([]any) {
		ids = append(ids, x.(map[string]any)["id"].(string))
	}
	want := []string{"add-feature-with-tests", "custom-chore", "fix-bug-with-regression-test", "perf-hillclimb-task", "refactor-keep-behavior", "security-fix", "write-integration-tests"}
	if !slices.Equal(ids, want) {
		t.Fatalf("templates %v want %v", ids, want)
	}

	base := e.Must(lead, "create_task", map[string]any{"brief_id": bid, "title": "base", "spec": "s", "repo": "o/r"})["task"].(map[string]any)

	got := e.Must(lead, "create_task_from_template", map[string]any{
		"brief_id": bid, "template_id": "fix-bug-with-regression-test", "repo": "o/r",
		"vars":       map[string]any{"bug": "lease sweeper skips tasks", "symptom": "tasks stay in_progress", "area": "internal/board"},
		"depends_on": []string{base["id"].(string)}, "priority": 7,
	})["task"].(map[string]any)
	if got["title"] != "Fix lease sweeper skips tasks" || got["role"] != "worker" || got["priority"] != float64(7) || got["status"] != "backlog" {
		t.Fatalf("task: %v", got)
	}
	if !strings.Contains(got["spec"].(string), "Observed symptom: tasks stay in_progress") || strings.Contains(got["spec"].(string), "{{") {
		t.Fatalf("spec: %v", got["spec"])
	}
	if b := got["budget"].(map[string]any); b["max_minutes"] != float64(120) || b["max_cost_eur"] != float64(5) {
		t.Fatalf("budget: %v", b)
	}
	if ac := got["acceptance_criteria"].([]any); len(ac) != 4 || !strings.Contains(ac[2].(string), "internal/board") {
		t.Fatalf("criteria: %v", ac)
	}
	if deps := got["depends_on"].([]any); len(deps) != 1 || deps[0] != base["id"] {
		t.Fatalf("depends_on: %v", deps)
	}

	tester := e.Must(lead, "create_task_from_template", map[string]any{
		"brief_id": bid, "template_id": "custom-chore", "repo": "o/r", "vars": map[string]any{"thing": "fixtures"},
	})["task"].(map[string]any)
	if tester["role"] != "tester" || tester["title"] != "Chore fixtures" || tester["budget"].(map[string]any)["max_minutes"] != float64(15) {
		t.Fatalf("custom task: %v", tester)
	}

	_, board := e.REST("GET", "/api/v1/briefs/"+bid+"/board", nil)
	if n := len(board["columns"].(map[string]any)["backlog"].([]any)); n != 3 {
		t.Fatalf("backlog has %d tasks, want 3", n)
	}

	fail := func(args map[string]any, code, contains string) {
		t.Helper()
		out, isErr := e.Call(lead, "create_task_from_template", args)
		if !isErr {
			t.Fatalf("%v should fail, got %v", args, out)
		}
		er := out["error"].(map[string]any)
		if er["code"] != code || !strings.Contains(er["message"].(string), contains) {
			t.Fatalf("%v: %v, want %s containing %q", args, er, code, contains)
		}
	}
	fail(map[string]any{"brief_id": bid, "template_id": "fix-bug-with-regression-test", "repo": "o/r", "vars": map[string]any{"bug": "x"}},
		"bad_request", "missing variables: symptom, area")
	fail(map[string]any{"brief_id": bid, "template_id": "fix-bug-with-regression-test", "repo": "o/r"},
		"bad_request", "missing variables: bug, symptom, area")
	fail(map[string]any{"brief_id": bid, "template_id": "nope", "repo": "o/r", "vars": map[string]any{}}, "not_found", "available: add-feature-with-tests")
	fail(map[string]any{"brief_id": bid, "template_id": "make-10x-faster", "repo": "o/r", "vars": map[string]any{"target": "x", "command": "y"}}, "not_found", "not found")
	fail(map[string]any{"brief_id": "B-99", "template_id": "security-fix", "repo": "o/r", "vars": map[string]any{"issue": "i", "impact": "m", "area": "a"}}, "not_found", "")
	fail(map[string]any{"brief_id": bid, "template_id": "security-fix", "repo": "other/repo", "vars": map[string]any{"issue": "i", "impact": "m", "area": "a"}}, "bad_request", "not part of brief")
	fail(map[string]any{"brief_id": bid, "template_id": "security-fix", "repo": "o/r", "vars": map[string]any{"issue": 1, "impact": "m", "area": "a"}}, "bad_request", "invalid arguments")

	e.MustFail(worker, "create_task_from_template", map[string]any{"brief_id": bid, "template_id": "security-fix"}, "")
	listed := e.RPC(lead, "tools/list", map[string]any{})["result"].(map[string]any)["tools"].([]any)
	if !slices.ContainsFunc(listed, func(x any) bool { return x.(map[string]any)["name"] == "create_task_from_template" }) {
		t.Fatal("create_task_from_template missing from the leader's tools/list")
	}
}
