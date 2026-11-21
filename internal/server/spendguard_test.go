package server_test

import (
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/servertest"
)

func TestOrgBudgetGuardOverHTTP(t *testing.T) {
	e := servertest.New(t)
	lead := e.Register("rex", "leader", "other")
	wk := e.Register("claude-1", "worker", "claude-code")

	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "Demo", "body": "b", "repos": []string{"o/r"}, "max_cost_eur": 1000, "max_parallel_agents": 4})
	bid := br["id"].(string)
	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{
		map[string]any{"title": "Work", "spec": "s", "repo": "o/r", "acceptance_criteria": []string{"ok"}, "budget": map[string]any{"max_minutes": 60, "max_cost_eur": 1000}},
	}})["plan"].(map[string]any)
	tid := plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
	if code, out := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	e.Must(lead, "assign", map[string]any{"task_id": tid, "agent_id": "claude-1"})
	e.Must(wk, "claim_task", map[string]any{"task_id": tid})

	events := e.Srv.Hub.Subscribe([]string{"control"})
	defer events.Close()

	if code, _ := e.REST("PUT", "/api/v1/limits", map[string]any{"daily_cost_eur": -1}); code != 400 {
		t.Fatalf("negative cap: %d", code)
	}
	if code, _ := e.REST("PUT", "/api/v1/limits", map[string]any{"daily": 5}); code != 400 {
		t.Fatalf("unknown field: %d", code)
	}
	code, out := e.REST("PUT", "/api/v1/limits", map[string]any{"daily_cost_eur": 10, "weekly_cost_eur": 100})
	if code != 200 || out["daily_cost_eur"] != 10.0 || out["weekly_cost_eur"] != 100.0 || out["org_budget_paused"] != false {
		t.Fatalf("put limits: %d %v", code, out)
	}
	if got := window(t, out, "daily"); got["level"] != "ok" || got["cap_eur"] != 10.0 {
		t.Fatalf("daily window %v", got)
	}

	e.Must(wk, "update_status", map[string]any{"task_id": tid, "status": "in_progress", "cost_eur": 8.5})
	waitFor(t, "warning level", func() bool {
		_, out := e.REST("GET", "/api/v1/limits", nil)
		return window(t, out, "daily")["level"] == "warning"
	})
	waitFor(t, "budget decision", func() bool {
		_, out := e.REST("GET", "/api/v1/decisions", nil)
		for _, d := range out["decisions"].([]any) {
			if d.(map[string]any)["id"] == "budget:org-daily" {
				return true
			}
		}
		return false
	})
	waitFor(t, "spend.warning event", func() bool {
		select {
		case ev := <-events.C():
			return ev.Type == "spend.warning"
		default:
			return false
		}
	})
	if _, st := e.REST("GET", "/api/v1/state", nil); st["paused"] != false {
		t.Fatalf("paused at 85%%: %v", st)
	}

	e.Must(wk, "update_status", map[string]any{"task_id": tid, "status": "in_progress", "cost_eur": 10.5})
	waitFor(t, "org_budget pause", func() bool {
		_, st := e.REST("GET", "/api/v1/state", nil)
		return st["paused"] == true && st["pause_reason"] == "org_budget"
	})
	e.MustFail(lead, "request_spawn", map[string]any{"brief_id": bid, "worker_kind": "claude-code", "model": "m", "n": 1}, "paused")
	time.Sleep(300 * time.Millisecond)
	if n := auditTools(t, e, "pause_all"); n != 1 {
		t.Fatalf("pause_all audited %d times, want exactly 1", n)
	}
	_, out = e.REST("GET", "/api/v1/limits", nil)
	if out["org_budget_paused"] != true || window(t, out, "daily")["level"] != "exceeded" {
		t.Fatalf("limits while paused: %v", out)
	}
	if n := auditTools(t, e, "set_limits"); n != 1 {
		t.Fatalf("set_limits audited %d times, want 1", n)
	}

	code, out = e.REST("PUT", "/api/v1/limits", map[string]any{"daily_cost_eur": 50, "weekly_cost_eur": 100})
	if code != 200 || out["org_budget_paused"] != false {
		t.Fatalf("raise cap: %d %v", code, out)
	}
	if _, st := e.REST("GET", "/api/v1/state", nil); st["paused"] != false {
		t.Fatalf("still paused after raising the cap: %v", st)
	}

	code, out = e.REST("PUT", "/api/v1/limits", map[string]any{"daily_cost_eur": 5})
	if code != 200 || out["org_budget_paused"] != true {
		t.Fatalf("lowered cap below spend must pause again: %d %v", code, out)
	}
	if n := auditTools(t, e, "pause_all"); n != 2 {
		t.Fatalf("pause_all audited %d times, want 2", n)
	}
}

func window(t *testing.T, limits map[string]any, name string) map[string]any {
	t.Helper()
	for _, w := range limits["windows"].([]any) {
		if m := w.(map[string]any); m["window"] == name {
			return m
		}
	}
	t.Fatalf("no %s window in %v", name, limits)
	return nil
}

func auditTools(t *testing.T, e *servertest.Env, tool string) int {
	t.Helper()
	_, out := e.REST("GET", "/api/v1/audit?tool="+tool, nil)
	return len(out["events"].([]any))
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
