package server_test

import (
	"testing"
)

func TestReportUsageRespectsTaskVisibility(t *testing.T) {
	e := lsNew(t)
	mine := e.brief("mine", "rex")
	theirs := e.brief("theirs", "max")
	e.approved(e.rex, mine, "a")
	hidden := e.approved(e.max, theirs, "b")[0]

	for name, tok := range map[string]string{"other leader": e.rex, "unassigned worker": e.w1} {
		for _, args := range []map[string]any{
			{"model": "claude-sonnet-4-5", "task_id": hidden, "tokens_in": 10},
			{"model": "claude-sonnet-4-5", "task_id": "T-999", "tokens_in": 10},
		} {
			code, msg := e.fail(tok, "report_usage", args)
			if code != "not_found" || msg != "not found" {
				t.Fatalf("%s reporting on %v: %s %q", name, args["task_id"], code, msg)
			}
		}
	}
	if code, _ := e.fail(e.rex, "report_usage", map[string]any{"model": "claude-sonnet-4-5", "brief_id": theirs, "cost_usd": 5}); code != "not_found" {
		t.Fatalf("leader attributing cost to another leader's brief: %s", code)
	}

	out := e.Must(e.max, "report_usage", map[string]any{"model": "claude-sonnet-4-5", "task_id": hidden, "tokens_in": 10})
	if rec := out["record"].(map[string]any); rec["brief_id"] != theirs {
		t.Fatalf("owning leader: %v", rec)
	}
	costs := e.want(200, "GET", "/api/v1/costs?group_by=brief", nil)
	for _, g := range costs["groups"].([]any) {
		if m := g.(map[string]any); m["key"] == theirs && m["requests"] != 1.0 {
			t.Fatalf("brief %s costs: %v", theirs, m)
		}
	}
}
