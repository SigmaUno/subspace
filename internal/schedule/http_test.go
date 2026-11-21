package schedule_test

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

type httpRig struct {
	*servertest.Env
	clock *clock
}

func startServer(t *testing.T) *httpRig {
	t.Helper()
	t.Setenv("SUBSPACE_SCHEDULE_INTERVAL", "20ms")
	c := &clock{t: time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)}
	e := servertest.Start(t, server.Config{PublicURL: "http://sub.test", Now: c.now,
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})
	return &httpRig{Env: e, clock: c}
}

func (h *httpRig) want(code int, method, path string, body any) map[string]any {
	h.T.Helper()
	got, out := h.REST(method, path, body)
	if got != code {
		h.T.Fatalf("%s %s: %d %v, want %d", method, path, got, out, code)
	}
	return out
}

func (h *httpRig) runs(id string) []any {
	h.T.Helper()
	out, _ := h.want(200, "GET", "/api/v1/schedules/"+id+"/runs", nil)["runs"].([]any)
	return out
}

func (h *httpRig) waitRuns(id string, n int) []any {
	h.T.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		rs := h.runs(id)
		if len(rs) >= n {
			time.Sleep(60 * time.Millisecond)
			if again := h.runs(id); len(again) == n {
				return again
			}
			h.T.Fatalf("schedule %s has %d runs, want %d", id, len(h.runs(id)), n)
		}
		if time.Now().After(deadline) {
			h.T.Fatalf("schedule %s has %d runs after 10s, want %d", id, len(rs), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func field(m any, key string) any { return m.(map[string]any)[key] }

var bugfix = map[string]any{
	"template_id": "bugfix",
	"vars":        map[string]any{"title": "nightly check", "repo": "o/r", "symptom": "s", "reproduction": "r", "expected": "e"},
}

func (h *httpRig) createBrief(body map[string]any) map[string]any {
	h.T.Helper()
	body["action"] = "create_brief"
	if body["brief"] == nil {
		body["brief"] = bugfix
	}
	return h.want(201, "POST", "/api/v1/schedules", body)
}

func TestSchedulesRESTValidationAndCRUD(t *testing.T) {
	h := startServer(t)
	brief := h.want(201, "POST", "/api/v1/briefs", map[string]any{"title": "ops", "body": "b", "repos": []string{"o/r"}})["id"].(string)

	task := map[string]any{"template_id": "fix-bug-with-regression-test", "repo": "o/r", "vars": map[string]any{"bug": "x", "symptom": "y", "area": "z"}}
	bad := []struct {
		name string
		body map[string]any
	}{
		{"empty body", map[string]any{}},
		{"unknown field", map[string]any{"name": "n", "cron": "* * * * *", "action": "create_brief", "brief": bugfix, "nope": 1}},
		{"cron", map[string]any{"name": "n", "cron": "61 * * * *", "action": "create_brief", "brief": bugfix}},
		{"timezone", map[string]any{"name": "n", "cron": "* * * * *", "timezone": "Mars/Base", "action": "create_brief", "brief": bugfix}},
		{"action", map[string]any{"name": "n", "cron": "* * * * *", "action": "explode"}},
		{"no brief", map[string]any{"name": "n", "cron": "* * * * *", "action": "create_brief"}},
		{"template and markdown", map[string]any{"name": "n", "cron": "* * * * *", "action": "create_brief", "brief": map[string]any{"template_id": "bugfix", "markdown": "---\ntitle: x\n---\n"}}},
		{"missing variables", map[string]any{"name": "n", "cron": "* * * * *", "action": "create_brief", "brief": map[string]any{"template_id": "bugfix", "vars": map[string]any{"title": "t"}}}},
		{"broken document", map[string]any{"name": "n", "cron": "* * * * *", "action": "create_brief", "brief": map[string]any{"markdown": "---\ntitle: a\ntitle: b\n---\n"}}},
		{"catch up", map[string]any{"name": "n", "cron": "* * * * *", "action": "create_brief", "brief": bugfix, "catch_up": "later"}},
		{"max active", map[string]any{"name": "n", "cron": "* * * * *", "action": "create_brief", "brief": bugfix, "max_active": 101}},
		{"no target brief", map[string]any{"name": "n", "cron": "* * * * *", "action": "add_tasks", "tasks": []any{task}}},
		{"no tasks", map[string]any{"name": "n", "cron": "* * * * *", "action": "add_tasks", "brief_id": brief}},
		{"repo outside the brief", map[string]any{"name": "n", "cron": "* * * * *", "action": "add_tasks", "brief_id": brief,
			"tasks": []any{map[string]any{"template_id": "fix-bug-with-regression-test", "repo": "o/other", "vars": task["vars"]}}}},
		{"task variables", map[string]any{"name": "n", "cron": "* * * * *", "action": "add_tasks", "brief_id": brief,
			"tasks": []any{map[string]any{"template_id": "fix-bug-with-regression-test", "repo": "o/r", "vars": map[string]any{"bug": "x"}}}}},
	}
	for _, c := range bad {
		if code, out := h.REST("POST", "/api/v1/schedules", c.body); code != 400 {
			t.Errorf("%s: %d %v, want 400", c.name, code, out)
		}
	}
	h.want(404, "POST", "/api/v1/schedules", map[string]any{"name": "n", "cron": "* * * * *", "action": "create_brief", "brief": map[string]any{"template_id": "nope"}})
	h.want(404, "POST", "/api/v1/schedules", map[string]any{"name": "n", "cron": "* * * * *", "action": "add_tasks", "brief_id": "B-99", "tasks": []any{task}})
	h.want(404, "POST", "/api/v1/schedules", map[string]any{"name": "n", "cron": "* * * * *", "action": "add_tasks", "brief_id": brief,
		"tasks": []any{map[string]any{"template_id": "nope", "repo": "o/r"}}})
	if rs := h.want(200, "GET", "/api/v1/schedules", nil)["schedules"].([]any); len(rs) != 0 {
		t.Fatalf("rejected requests created schedules: %v", rs)
	}

	s := h.createBrief(map[string]any{"name": " Weekly audit ", "cron": "0  9 * * 1", "timezone": "Europe/Berlin"})
	id := s["id"].(string)
	if id != "SC-1" || s["name"] != "Weekly audit" || s["cron"] != "0 9 * * 1" || s["enabled"] != true || s["max_active"] != 1.0 ||
		s["catch_up"] != "skip" || s["next_run_at"] != "2026-11-23T08:00:00Z" || s["last_run_at"] != nil || s["tasks"] == nil {
		t.Fatalf("created schedule = %v", s)
	}
	h.want(201, "POST", "/api/v1/schedules", map[string]any{"name": "tasks", "cron": "*/5 * * * *", "action": "add_tasks", "brief_id": brief,
		"tasks": []any{task}, "max_active": 0, "catch_up": "run-once", "enabled": false})

	all := h.want(200, "GET", "/api/v1/schedules", nil)["schedules"].([]any)
	if len(all) != 2 || field(all[0], "id") != id || field(all[1], "next_run_at") != nil || field(all[1], "brief") != nil {
		t.Fatalf("list = %v", all)
	}
	got := h.want(200, "GET", "/api/v1/schedules/"+id, nil)
	if got["next_run_at"] != "2026-11-23T08:00:00Z" {
		t.Fatalf("get = %v", got)
	}

	h.want(400, "PATCH", "/api/v1/schedules/"+id, map[string]any{"cron": "nope"})
	h.want(400, "PATCH", "/api/v1/schedules/"+id, map[string]any{"unknown": true})
	h.want(404, "PATCH", "/api/v1/schedules/SC-99", map[string]any{"enabled": false})
	off := h.want(200, "PATCH", "/api/v1/schedules/"+id, map[string]any{"enabled": false, "name": "Audit"})
	if off["enabled"] != false || off["next_run_at"] != nil || off["name"] != "Audit" || off["cron"] != "0 9 * * 1" {
		t.Fatalf("patched = %v", off)
	}
	moved := h.want(200, "PATCH", "/api/v1/schedules/"+id, map[string]any{"enabled": true, "cron": "30 6 * * *", "timezone": "UTC"})
	if moved["next_run_at"] != "2026-11-22T06:30:00Z" {
		t.Fatalf("moved = %v", moved)
	}
	h.want(200, "PATCH", "/api/v1/schedules/"+id, map[string]any{"action": "add_tasks", "brief_id": brief, "tasks": []any{task}})
	if sc := h.want(200, "GET", "/api/v1/schedules/"+id, nil); sc["action"] != "add_tasks" || sc["brief"] != nil || sc["brief_id"] != brief {
		t.Fatalf("switched = %v", sc)
	}

	if len(h.runs(id)) != 0 {
		t.Fatal("a schedule without due instants has runs")
	}
	h.want(204, "DELETE", "/api/v1/schedules/"+id, nil)
	h.want(404, "GET", "/api/v1/schedules/"+id, nil)
	h.want(404, "GET", "/api/v1/schedules/"+id+"/runs", nil)
	h.want(404, "DELETE", "/api/v1/schedules/"+id, nil)

	for tool, n := range map[string]int{"create_schedule": 2, "update_schedule": 3, "delete_schedule": 1} {
		evs := h.want(200, "GET", "/api/v1/audit?tool="+tool, nil)["events"].([]any)
		if len(evs) != n {
			t.Errorf("%s: %d audit events, want %d", tool, len(evs), n)
		}
	}
}

func TestSchedulesPreview(t *testing.T) {
	h := startServer(t)
	q := func(cron, tz, count string) string {
		return "/api/v1/schedules/preview?cron=" + url.QueryEscape(cron) + "&timezone=" + url.QueryEscape(tz) + "&count=" + count
	}
	out := h.want(200, "GET", q("30 2 * * *", "America/New_York", "3"), nil)
	next := out["next"].([]any)
	if len(next) != 3 || next[0] != "2026-11-22T07:30:00Z" || next[1] != "2026-11-23T07:30:00Z" || out["timezone"] != "America/New_York" {
		t.Fatalf("preview = %v", out)
	}
	if def := h.want(200, "GET", "/api/v1/schedules/preview?cron="+url.QueryEscape("*/10 * * * *"), nil); len(def["next"].([]any)) != 5 || def["timezone"] != "UTC" {
		t.Fatalf("default preview = %v", def)
	}
	for _, p := range []string{q("", "UTC", "3"), q("61 * * * *", "UTC", "3"), q("* * * * *", "Nowhere/X", "3"), q("* * * * *", "UTC", "0"), q("* * * * *", "UTC", "21"), q("* * * * *", "UTC", "x")} {
		h.want(400, "GET", p, nil)
	}
}

func TestSchedulesViewerTokenCannotChange(t *testing.T) {
	h := startServer(t)
	secret := h.want(201, "POST", "/api/v1/tokens", map[string]any{"name": "viewer", "scope": "viewer"})["secret"].(string)
	if code, _ := h.RESTAs(secret, "GET", "/api/v1/schedules", nil); code != 200 {
		t.Fatalf("viewer GET = %d", code)
	}
	if code, _ := h.RESTAs(secret, "POST", "/api/v1/schedules", map[string]any{}); code != 403 {
		t.Fatalf("viewer POST = %d", code)
	}
	if code, _ := h.RESTAs("", "GET", "/api/v1/schedules", nil); code != 401 {
		t.Fatalf("anonymous GET = %d", code)
	}
}

func TestSchedulesCreateBriefsOnTimetable(t *testing.T) {
	h := startServer(t)
	s := h.createBrief(map[string]any{"name": "nightly", "cron": "0 9 * * *"})
	id := s["id"].(string)

	h.clock.advance(23 * time.Hour)
	rs := h.waitRuns(id, 1)
	run := rs[0]
	if field(run, "status") != "created" || field(run, "scheduled_for") != "2026-11-22T09:00:00Z" || field(run, "brief_id") != "B-1" || field(run, "missed") != 0.0 {
		t.Fatalf("run = %v", run)
	}
	br := h.want(200, "GET", "/api/v1/briefs/B-1", nil)
	if br["title"] != "Fix: nightly check" || br["status"] != "open" {
		t.Fatalf("brief = %v", br)
	}
	var named bool
	for _, m := range h.want(200, "GET", "/api/v1/threads/brief/B-1/messages", nil)["messages"].([]any) {
		body, _ := field(m, "body").(string)
		named = named || (field(m, "kind") == "system" && strings.Contains(body, "SC-1") && strings.Contains(body, "nightly") && strings.Contains(body, "2026-11-22T09:00:00Z"))
	}
	if !named {
		t.Fatal("the brief thread has no system message naming the schedule")
	}
	if got := h.want(200, "GET", "/api/v1/schedules/"+id, nil); got["last_run_at"] != "2026-11-22T09:00:00Z" || got["next_run_at"] != "2026-11-23T09:00:00Z" {
		t.Fatalf("schedule after run = %v", got)
	}

	h.clock.advance(24 * time.Hour)
	rs = h.waitRuns(id, 2)
	if field(rs[0], "status") != "skipped" || field(rs[0], "detail") != "max_active" || field(rs[0], "brief_id") != "" {
		t.Fatalf("run beyond max_active = %v", rs[0])
	}

	h.want(200, "PATCH", "/api/v1/briefs/B-1", map[string]any{"status": "closed"})
	h.clock.advance(24 * time.Hour)
	rs = h.waitRuns(id, 3)
	if field(rs[0], "status") != "created" || field(rs[0], "brief_id") != "B-2" {
		t.Fatalf("run after closing = %v", rs[0])
	}

	audit := h.want(200, "GET", "/api/v1/audit?tool=run_schedule", nil)["events"].([]any)
	if len(audit) != 2 || field(audit[0], "agent_id") != "schedule" || field(audit[0], "role") != "system" {
		t.Fatalf("audit = %v", audit)
	}
	var feed bool
	for _, it := range h.want(200, "GET", "/api/v1/activity?limit=200", nil)["items"].([]any) {
		text, _ := field(it, "text").(string)
		feed = feed || (field(it, "tool") == "run_schedule" && strings.Contains(text, "SC-1"))
	}
	if !feed {
		t.Fatal("the activity feed does not show the run")
	}

	h.want(204, "DELETE", "/api/v1/schedules/"+id, nil)
	h.clock.advance(24 * time.Hour)
	time.Sleep(150 * time.Millisecond)
	if bs := h.want(200, "GET", "/api/v1/briefs", nil)["briefs"].([]any); len(bs) != 2 {
		t.Fatalf("a deleted schedule created a brief: %d briefs", len(bs))
	}
}

func TestSchedulesAddTasksToABrief(t *testing.T) {
	h := startServer(t)
	brief := h.want(201, "POST", "/api/v1/briefs", map[string]any{"title": "ops", "body": "b", "repos": []string{"o/r"}})["id"].(string)
	s := h.want(201, "POST", "/api/v1/schedules", map[string]any{
		"name": "chores", "cron": "0 * * * *", "action": "add_tasks", "brief_id": brief,
		"tasks": []any{map[string]any{"template_id": "fix-bug-with-regression-test", "repo": "o/r", "priority": 3,
			"vars": map[string]any{"bug": "flaky cache", "symptom": "sometimes stale", "area": "cache"}}},
	})
	id := s["id"].(string)

	h.clock.advance(time.Hour)
	rs := h.waitRuns(id, 1)
	if field(rs[0], "status") != "created" || field(rs[0], "brief_id") != brief {
		t.Fatalf("run = %v", rs[0])
	}
	ids, _ := field(rs[0], "task_ids").([]any)
	if len(ids) != 1 {
		t.Fatalf("task ids = %v", ids)
	}
	task := h.want(200, "GET", "/api/v1/tasks/"+ids[0].(string), nil)
	if task["title"] != "Fix flaky cache" || task["brief_id"] != brief || task["repo"] != "o/r" || task["priority"] != 3.0 {
		t.Fatalf("task = %v", task)
	}
	var named bool
	for _, m := range h.want(200, "GET", "/api/v1/threads/brief/"+brief+"/messages", nil)["messages"].([]any) {
		body, _ := field(m, "body").(string)
		named = named || (field(m, "kind") == "system" && strings.Contains(body, id) && strings.Contains(body, ids[0].(string)))
	}
	if !named {
		t.Fatal("the brief thread has no system message naming the schedule")
	}

	h.clock.advance(time.Hour)
	rs = h.waitRuns(id, 2)
	if field(rs[0], "status") != "skipped" || field(rs[0], "detail") != "max_active" {
		t.Fatalf("run while the task is open = %v", rs[0])
	}
	h.want(200, "POST", "/api/v1/tasks/"+ids[0].(string)+"/cancel", map[string]any{"reason": "obsolete"})
	h.clock.advance(time.Hour)
	if rs = h.waitRuns(id, 3); field(rs[0], "status") != "created" {
		t.Fatalf("run after the task closed = %v", rs[0])
	}

	h.want(200, "PATCH", "/api/v1/briefs/"+brief, map[string]any{"status": "closed"})
	h.want(200, "PATCH", "/api/v1/schedules/"+id, map[string]any{"max_active": 0})
	h.clock.advance(time.Hour)
	rs = h.waitRuns(id, 4)
	if field(rs[0], "status") != "failed" || !strings.Contains(field(rs[0], "detail").(string), "closed") {
		t.Fatalf("run on a closed brief = %v", rs[0])
	}
}

func TestSchedulesMissedRunsAfterDowntime(t *testing.T) {
	h := startServer(t)
	skip := h.createBrief(map[string]any{"name": "skip", "cron": "0 9 * * *", "catch_up": "skip", "max_active": 0})["id"].(string)
	once := h.createBrief(map[string]any{"name": "once", "cron": "0 9 * * *", "catch_up": "run-once", "max_active": 0})["id"].(string)

	h.clock.advance(3*24*time.Hour + 5*time.Hour)
	rs := h.waitRuns(skip, 1)
	if field(rs[0], "status") != "skipped" || field(rs[0], "detail") != "missed" || field(rs[0], "missed") != 3.0 {
		t.Fatalf("skip run = %v", rs[0])
	}
	rs = h.waitRuns(once, 1)
	if field(rs[0], "status") != "created" || field(rs[0], "missed") != 2.0 || field(rs[0], "scheduled_for") != "2026-11-24T09:00:00Z" {
		t.Fatalf("run-once run = %v", rs[0])
	}
	if bs := h.want(200, "GET", "/api/v1/briefs", nil)["briefs"].([]any); len(bs) != 1 {
		t.Fatalf("downtime created %d briefs, want 1", len(bs))
	}

	h.clock.advance(18 * time.Hour)
	h.waitRuns(skip, 2)
	h.waitRuns(once, 2)
	if bs := h.want(200, "GET", "/api/v1/briefs", nil)["briefs"].([]any); len(bs) != 3 {
		t.Fatalf("after the next instant: %d briefs, want 3", len(bs))
	}
}
