package server_test

import (
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/servertest"
)

type closeFixture struct {
	*servertest.Env
	lead, rev string
	tok       map[string]string
}

func (f *closeFixture) restOK(method, path string, body any) map[string]any {
	f.T.Helper()
	code, out := f.REST(method, path, body)
	if code != 200 {
		f.T.Fatalf("%s %s: %d %v", method, path, code, out)
	}
	return out
}

func (f *closeFixture) columns(bid string) map[string][]string {
	f.T.Helper()
	out := map[string][]string{}
	for status, ts := range f.restOK("GET", "/api/v1/briefs/"+bid+"/board", nil)["columns"].(map[string]any) {
		for _, t := range ts.([]any) {
			out[status] = append(out[status], t.(map[string]any)["id"].(string))
		}
	}
	return out
}

func (f *closeFixture) task(id string) map[string]any {
	f.T.Helper()
	return f.restOK("GET", "/api/v1/tasks/"+id, nil)
}

func (f *closeFixture) messages(thread string) string {
	f.T.Helper()
	var b strings.Builder
	for _, m := range f.restOK("GET", "/api/v1/threads/"+thread+"/messages", nil)["messages"].([]any) {
		b.WriteString(m.(map[string]any)["body"].(string) + "\n")
	}
	return b.String()
}

func (f *closeFixture) tools(taskID string) []string {
	f.T.Helper()
	var out []string
	for _, ev := range f.restOK("GET", "/api/v1/audit?task_id="+taskID, nil)["events"].([]any) {
		out = append(out, ev.(map[string]any)["tool"].(string))
	}
	return out
}

func (f *closeFixture) auditCount(tool string) int {
	f.T.Helper()
	return len(f.restOK("GET", "/api/v1/audit?tool="+tool, nil)["events"].([]any))
}

func TestBriefCloseAndReopenWithAgentsMidWork(t *testing.T) {
	f := &closeFixture{Env: servertest.New(t), tok: map[string]string{}}
	f.lead = f.Register("rex", "leader", "other")
	f.rev = f.Register("codex-r", "reviewer", "codex")
	for _, id := range []string{"w1", "w2", "w3", "w4", "w5", "w6"} {
		f.tok[id] = f.Register(id, "worker", "claude-code")
	}

	code, br := f.REST("POST", "/api/v1/briefs", map[string]any{"title": "Demo", "body": "b", "repos": []string{"o/r"}, "max_cost_eur": 50, "max_parallel_agents": 10})
	if code != 201 {
		t.Fatalf("brief: %d %v", code, br)
	}
	bid := br["id"].(string)

	var specs []any
	for _, name := range []string{"active-1", "active-2", "idle", "assigned", "submitted", "approved", "changes"} {
		specs = append(specs, map[string]any{"title": name, "spec": "s", "repo": "o/r", "acceptance_criteria": []string{"ok"}})
	}
	plan := f.Must(f.lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": specs})["plan"].(map[string]any)
	pid := plan["id"].(string)
	var ids []string
	for _, x := range plan["tasks"].([]any) {
		ids = append(ids, x.(map[string]any)["id"].(string))
	}
	f.restOK("POST", "/api/v1/plans/"+pid+"/approve", map[string]any{})
	active1, active2, idle, assigned, submitted, approved, changes := ids[0], ids[1], ids[2], ids[3], ids[4], ids[5], ids[6]

	for task, agent := range map[string]string{active1: "w1", active2: "w2", assigned: "w3", submitted: "w4", approved: "w5", changes: "w6"} {
		f.Must(f.lead, "assign", map[string]any{"task_id": task, "agent_id": agent})
	}
	for task, agent := range map[string]string{active1: "w1", active2: "w2", submitted: "w4", approved: "w5", changes: "w6"} {
		f.Must(f.tok[agent], "claim_task", map[string]any{"task_id": task})
	}
	f.Must(f.tok["w1"], "update_status", map[string]any{"task_id": active1, "status": "in_progress", "progress_pct": 40, "cost_eur": 0.4})
	for task, agent := range map[string]string{submitted: "w4", approved: "w5", changes: "w6"} {
		f.Must(f.tok[agent], "submit", map[string]any{"task_id": task, "pr_url": "https://git.test/o/r/pulls/1", "summary": "done"})
	}
	f.Must(f.rev, "review", map[string]any{"task_id": approved, "verdict": "approve", "comments": "ok"})
	f.Must(f.rev, "review", map[string]any{"task_id": changes, "verdict": "changes_requested", "comments": "fix"})

	f.restOK("PATCH", "/api/v1/briefs/"+bid, map[string]any{"status": "closed"})

	cols := f.columns(bid)
	want := map[string][]string{
		"cancelled": {idle, assigned}, "blocked": {active1, active2},
		"submitted": {submitted}, "approved": {approved}, "changes_requested": {changes},
	}
	for status, w := range want {
		got := map[string]bool{}
		for _, id := range cols[status] {
			got[id] = true
		}
		if len(got) != len(w) {
			t.Fatalf("column %s = %v, want %v", status, cols[status], w)
		}
		for _, id := range w {
			if !got[id] {
				t.Fatalf("column %s = %v, want %v", status, cols[status], w)
			}
		}
	}
	for _, id := range []string{active1, active2} {
		task := f.task(id)
		if task["blocked_reason"] != "brief_closed" || task["lease_expires_at"] != nil {
			t.Fatalf("%s after close: %v", id, task)
		}
		if got := f.tools(id); got[len(got)-1] != "close_brief" {
			t.Fatalf("%s audit tools %v", id, got)
		}
	}
	if got := f.tools(idle); got[len(got)-1] != "close_brief" {
		t.Fatalf("idle audit tools %v", got)
	}
	for _, id := range []string{idle, assigned, active1} {
		if m := f.messages("task/" + id); !strings.Contains(m, "Brief "+bid+" was closed") {
			t.Fatalf("task %s thread: %q", id, m)
		}
	}
	summary := f.messages("brief/" + bid)
	for _, part := range []string{"Brief closed: 2 cancelled, 2 blocked", "approved 1", "submitted 1", "Cost used: 0.40 EUR"} {
		if !strings.Contains(summary, part) {
			t.Fatalf("brief thread missing %q: %q", part, summary)
		}
	}
	if f.auditCount("close_brief") != 5 {
		t.Fatalf("close_brief audit events = %d", f.auditCount("close_brief"))
	}

	f.MustFail(f.tok["w6"], "claim_task", map[string]any{"task_id": changes}, "conflict")
	f.MustFail(f.tok["w1"], "update_status", map[string]any{"task_id": active1, "status": "in_progress"}, "conflict")
	f.MustFail(f.lead, "request_spawn", map[string]any{"brief_id": bid, "worker_kind": "claude-code", "model": "m", "n": 1}, "conflict")
	f.MustFail(f.lead, "create_task", map[string]any{"brief_id": bid, "title": "x", "spec": "s", "repo": "o/r"}, "conflict")

	f.restOK("PATCH", "/api/v1/briefs/"+bid, map[string]any{"status": "closed"})
	if f.auditCount("close_brief") != 5 {
		t.Fatal("closing a closed brief must not repeat the cascade")
	}

	if f.restOK("POST", "/api/v1/tasks/"+approved+"/done", map[string]any{"note": "merged"})["status"] != "done" {
		t.Fatal("owner must be able to finish an approved task on a closed brief")
	}

	got := f.restOK("PATCH", "/api/v1/briefs/"+bid, map[string]any{"status": "open"})
	if got["status"] != "open" {
		t.Fatalf("reopen: %v", got)
	}
	if f.auditCount("reopen_brief") != 1 {
		t.Fatalf("reopen_brief audit events = %d", f.auditCount("reopen_brief"))
	}
	if m := f.messages("brief/" + bid); !strings.Contains(m, "Brief reopened") {
		t.Fatalf("brief thread after reopen: %q", m)
	}
	cols = f.columns(bid)
	if len(cols["cancelled"]) != 2 || len(cols["blocked"]) != 2 || len(cols["ready"]) != 0 || len(cols["assigned"]) != 0 {
		t.Fatalf("board after reopen: %v", cols)
	}

	f.Must(f.tok["w6"], "claim_task", map[string]any{"task_id": changes})
	f.Must(f.lead, "update_task", map[string]any{"task_id": active1, "status": "ready"})
	f.Must(f.tok["w1"], "claim_task", map[string]any{"task_id": active1})
	if f.task(active1)["status"] != "in_progress" {
		t.Fatalf("task after reopen and claim: %v", f.task(active1))
	}
}
