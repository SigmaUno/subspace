package server_test

import (
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/servertest"
)

type replayTask struct {
	status, assignee, reviewer string
	progress, cost             float64
}

func replayTasks(t *testing.T, columns any) map[string]replayTask {
	t.Helper()
	out := map[string]replayTask{}
	for _, col := range columns.(map[string]any) {
		for _, x := range col.([]any) {
			m := x.(map[string]any)
			s := func(k string) string { v, _ := m[k].(string); return v }
			out[s("id")] = replayTask{s("status"), s("assignee"), s("reviewer"), m["progress_pct"].(float64), m["cost_used"].(float64)}
		}
	}
	return out
}

func TestReplayOverHTTP(t *testing.T) {
	e := servertest.New(t)
	lead := e.Register("rex", "leader", "other")
	w1 := e.Register("w1", "worker", "claude-code")
	w2 := e.Register("w2", "worker", "claude-code")
	rev := e.Register("rev", "reviewer", "codex")

	brief := func() string {
		code, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B", "body": "b", "repos": []string{"o/r"}, "max_cost_eur": 50, "max_parallel_agents": 5})
		if code != 201 {
			t.Fatalf("brief: %d %v", code, br)
		}
		return br["id"].(string)
	}
	bid, other := brief(), brief()
	path := "/api/v1/briefs/" + bid

	type capture struct {
		id    string
		tasks map[string]replayTask
	}
	var caps []capture
	snap := func() {
		t.Helper()
		_, v := e.REST("GET", "/api/v1/audit/verify", nil)
		code, b := e.REST("GET", path+"/board", nil)
		if code != 200 {
			t.Fatalf("board: %d %v", code, b)
		}
		caps = append(caps, capture{fmt.Sprint(v["head"].(map[string]any)["id"]), replayTasks(t, b["columns"])})
	}
	ok := func(code int, out map[string]any) {
		t.Helper()
		if code != 200 && code != 201 {
			t.Fatalf("%d %v", code, out)
		}
		snap()
	}
	spec := func(title string) map[string]any {
		return map[string]any{"title": title, "spec": "s", "repo": "o/r", "acceptance_criteria": []string{"ok"}}
	}
	ids := func(plan map[string]any) []string {
		var out []string
		for _, x := range plan["tasks"].([]any) {
			out = append(out, x.(map[string]any)["id"].(string))
		}
		return out
	}

	snap()
	e.Must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{spec("old a"), spec("old b")}})
	snap()
	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{spec("a"), spec("b"), spec("c")}})["plan"].(map[string]any)
	snap()
	pid := plan["id"].(string)
	ok(e.REST("POST", "/api/v1/plans/"+pid+"/tasks", spec("d")))
	tasks := ids(plan)
	_, p := e.REST("GET", "/api/v1/plans/"+pid, nil)
	extra := ids(p)[3]
	ok(e.REST("DELETE", "/api/v1/plans/"+pid+"/tasks/"+extra, nil))
	ok(e.REST("POST", "/api/v1/plans/"+pid+"/approve", map[string]any{}))
	e.Must(lead, "propose_plan", map[string]any{"brief_id": other, "tasks": []any{spec("x")}})
	snap()

	a, b, c := tasks[0], tasks[1], tasks[2]
	step := func(tok, tool string, args map[string]any) { t.Helper(); e.Must(tok, tool, args); snap() }
	step(lead, "assign", map[string]any{"task_id": a, "agent_id": "w1"})
	step(w1, "claim_task", map[string]any{"task_id": a})
	step(w1, "update_status", map[string]any{"task_id": a, "status": "in_progress", "progress_pct": 40, "cost_eur": 1.25})
	step(w1, "submit", map[string]any{"task_id": a, "pr_url": "https://example.com/pr/1", "summary": "s"})
	step(lead, "assign", map[string]any{"task_id": a, "agent_id": "rev", "as": "reviewer"})
	step(rev, "review", map[string]any{"task_id": a, "verdict": "changes_requested", "comments": "fix"})
	step(lead, "reassign", map[string]any{"task_id": a, "agent_id": "w2", "reason": "handoff"})
	step(w2, "claim_task", map[string]any{"task_id": a})
	step(w2, "submit", map[string]any{"task_id": a, "pr_url": "https://example.com/pr/1", "summary": "s"})
	step(rev, "review", map[string]any{"task_id": a, "verdict": "approve", "comments": "ok"})
	ok(e.REST("POST", "/api/v1/tasks/"+a+"/done", map[string]any{"note": "merged"}))
	step(lead, "assign", map[string]any{"task_id": b, "agent_id": "w1"})
	step(w1, "claim_task", map[string]any{"task_id": b})
	step(w1, "update_status", map[string]any{"task_id": b, "status": "blocked", "note": "stuck", "cost_eur": 2})
	ok(e.REST("POST", "/api/v1/tasks/"+b+"/reopen", map[string]any{"status": "assigned"}))
	ok(e.REST("POST", "/api/v1/tasks/"+c+"/cancel", map[string]any{"reason": "dup"}))

	for i, cp := range caps {
		code, out := e.REST("GET", path+"/replay?at="+cp.id, nil)
		if code != 200 {
			t.Fatalf("replay at %s: %d %v", cp.id, code, out)
		}
		got := replayTasks(t, out["columns"])
		if fmt.Sprint(got) != fmt.Sprint(cp.tasks) {
			t.Fatalf("step %d replay at audit %s:\n got  %v\n want %v", i, cp.id, got, cp.tasks)
		}
	}
	final := caps[len(caps)-1].tasks
	_, latest := e.REST("GET", path+"/replay", nil)
	if fmt.Sprint(replayTasks(t, latest["columns"])) != fmt.Sprint(final) {
		t.Fatalf("latest replay differs from the live board: %v", latest)
	}
	future := url.QueryEscape(time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	if _, out := e.REST("GET", path+"/replay?at="+future, nil); fmt.Sprint(replayTasks(t, out["columns"])) != fmt.Sprint(final) {
		t.Fatalf("replay at a future time differs from the live board: %v", out)
	}
	past := url.QueryEscape(time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))
	if _, out := e.REST("GET", path+"/replay?at="+past, nil); out["total"] != float64(0) || out["audit_id"] != float64(0) {
		t.Fatalf("replay before the brief existed: %v", out)
	}
	agents := map[string]bool{}
	for _, x := range latest["agents"].([]any) {
		agents[x.(map[string]any)["id"].(string)] = true
	}
	for _, id := range []string{"rex", "w1", "w2", "rev"} {
		if !agents[id] {
			t.Fatalf("agent %s missing from %v", id, latest["agents"])
		}
	}

	code, tl := e.REST("GET", path+"/replay/timeline", nil)
	if code != 200 {
		t.Fatalf("timeline: %d %v", code, tl)
	}
	points := tl["points"].([]any)
	if len(points) == 0 || len(points) != int(tl["events"].(float64)) {
		t.Fatalf("timeline points %d for %v events", len(points), tl["events"])
	}
	last := points[len(points)-1].(map[string]any)["counts"].(map[string]any)
	if last["done"] != float64(1) || last["assigned"] != float64(1) || last["cancelled"] != float64(4) {
		t.Fatalf("last timeline point %v", last)
	}
	seen := map[string]bool{}
	for _, k := range tl["key_events"].([]any) {
		m := k.(map[string]any)
		seen[m["tool"].(string)+":"+m["to"].(string)] = true
	}
	for _, k := range []string{"propose_plan:", "approve_plan:", "review:changes_requested", "review:approved", "mark_done:done", "block:blocked"} {
		if !seen[k] {
			t.Fatalf("key event %s missing: %v", k, tl["key_events"])
		}
	}

	from := caps[len(caps)-6].id
	code, d := e.REST("GET", path+"/replay/diff?from="+from+"&to="+caps[len(caps)-1].id, nil)
	if code != 200 {
		t.Fatalf("diff: %d %v", code, d)
	}
	var got []string
	for _, x := range d["transitions"].([]any) {
		m := x.(map[string]any)
		if s, ok := m["changes"].(map[string]any)["status"].(map[string]any); ok {
			got = append(got, fmt.Sprintf("%s %s %v→%v", m["task_id"], m["tool"], s["from"], s["to"]))
		}
	}
	want := []string{
		b + " assign ready→assigned", b + " claim_task assigned→in_progress", b + " block in_progress→blocked",
		b + " reopen blocked→assigned", c + " cancel ready→cancelled",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("diff transitions:\n got  %v\n want %v", got, want)
	}

	for _, tc := range []struct {
		path string
		code int
	}{
		{"/api/v1/briefs/B-999/replay", 404},
		{"/api/v1/briefs/B-999/replay/timeline", 404},
		{path + "/replay?at=yesterday", 400},
		{path + "/replay/diff?from=" + caps[len(caps)-1].id + "&to=" + from, 400},
	} {
		if code, out := e.REST("GET", tc.path, nil); code != tc.code {
			t.Fatalf("%s: %d %v, want %d", tc.path, code, out, tc.code)
		}
	}
}
