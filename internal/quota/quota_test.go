package quota_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

type fixture struct {
	*servertest.Env
	clk   *clock
	lead  string
	tasks []string
}

func newFixture(t *testing.T, tasks int) *fixture {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)}
	env := servertest.Start(t, server.Config{PublicURL: "http://sub.test", Lease: 72 * time.Hour, Now: clk.Now,
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond})
	f := &fixture{Env: env, clk: clk, lead: env.Register("lead", "leader", "other")}
	code, brief := env.REST("POST", "/api/v1/briefs", map[string]any{"title": "B"})
	if code != 201 {
		t.Fatalf("brief: %d %v", code, brief)
	}
	var plan []any
	for i := range tasks {
		plan = append(plan, map[string]any{"title": fmt.Sprintf("task %d", i), "spec": "s", "repo": "o/r", "acceptance_criteria": []string{"works"}})
	}
	p := env.Must(f.lead, "propose_plan", map[string]any{"brief_id": brief["id"], "tasks": plan})["plan"].(map[string]any)
	if code, out := env.REST("POST", "/api/v1/plans/"+p["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	for _, x := range p["tasks"].([]any) {
		f.tasks = append(f.tasks, x.(map[string]any)["id"].(string))
	}
	return f
}

func (f *fixture) worker(id string) string { return f.Register(id, "worker", "claude-code") }

func (f *fixture) setQuota(id string, q map[string]any) {
	f.T.Helper()
	if code, out := f.REST("PUT", "/api/v1/agents/"+id+"/quota", q); code != 200 {
		f.T.Fatalf("set quota: %d %v", code, out)
	}
}

func (f *fixture) assign(task, agent string) {
	f.T.Helper()
	f.Must(f.lead, "assign", map[string]any{"task_id": task, "agent_id": agent})
}

func (f *fixture) assignFails(task, agent string) {
	f.T.Helper()
	f.MustFail(f.lead, "assign", map[string]any{"task_id": task, "agent_id": agent}, "quota_exceeded")
}

func (f *fixture) usage(id string) map[string]any {
	f.T.Helper()
	code, out := f.REST("GET", "/api/v1/agents/"+id, nil)
	if code != 200 {
		f.T.Fatalf("get agent: %d %v", code, out)
	}
	return out["quota_usage"].(map[string]any)
}

func (f *fixture) submit(tok, task string) {
	f.T.Helper()
	f.Must(tok, "submit", map[string]any{"task_id": task, "pr_url": "https://github.com/o/r/pull/1", "summary": "done"})
}

func TestDailyTaskQuotaRollsOverAtUTCMidnight(t *testing.T) {
	f := newFixture(t, 2)
	tok := f.worker("w1")
	f.setQuota("w1", map[string]any{"max_tasks_per_day": 1})

	f.assign(f.tasks[0], "w1")
	f.assignFails(f.tasks[1], "w1")
	f.Must(tok, "claim_task", map[string]any{"task_id": f.tasks[0]})
	f.submit(tok, f.tasks[0])
	f.assignFails(f.tasks[1], "w1")

	if u := f.usage("w1"); u["tasks"] != float64(1) || u["level"] != "exceeded" || u["resets_at"] != "2026-11-22T00:00:00Z" {
		t.Fatalf("usage before rollover: %v", u)
	}

	f.clk.Set(time.Date(2026, 11, 21, 23, 59, 59, 0, time.UTC))
	f.assignFails(f.tasks[1], "w1")

	f.clk.Set(time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC))
	if u := f.usage("w1"); u["tasks"] != float64(0) || u["level"] != "ok" || u["day_starts_at"] != "2026-11-22T00:00:00Z" {
		t.Fatalf("usage after rollover: %v", u)
	}
	f.assign(f.tasks[1], "w1")
	f.Must(tok, "claim_task", map[string]any{"task_id": f.tasks[1]})
	if u := f.usage("w1"); u["tasks"] != float64(1) {
		t.Fatalf("usage after the next claim: %v", u)
	}
}

func TestReclaimingACountedTaskIsAllowed(t *testing.T) {
	f := newFixture(t, 2)
	tok := f.worker("w1")
	f.setQuota("w1", map[string]any{"max_tasks_per_day": 1})
	f.assign(f.tasks[0], "w1")
	f.Must(tok, "claim_task", map[string]any{"task_id": f.tasks[0]})
	f.Must(tok, "release_task", map[string]any{"task_id": f.tasks[0]})
	f.Must(tok, "claim_task", map[string]any{"task_id": f.tasks[0]})
	if u := f.usage("w1"); u["tasks"] != float64(1) {
		t.Fatalf("a re-claim must not count twice: %v", u)
	}
}

func TestConcurrentTaskQuota(t *testing.T) {
	f := newFixture(t, 4)
	capped := f.worker("capped")
	late := f.worker("late")
	f.worker("free")

	f.setQuota("capped", map[string]any{"max_concurrent_tasks": 1})
	f.assign(f.tasks[0], "capped")
	f.assignFails(f.tasks[1], "capped")
	f.assign(f.tasks[1], "free")

	f.assign(f.tasks[2], "late")
	f.assign(f.tasks[3], "late")
	f.setQuota("late", map[string]any{"max_concurrent_tasks": 1})
	f.Must(late, "claim_task", map[string]any{"task_id": f.tasks[2]})
	f.MustFail(late, "claim_task", map[string]any{"task_id": f.tasks[3]}, "quota_exceeded")
	f.submit(late, f.tasks[2])
	f.Must(late, "claim_task", map[string]any{"task_id": f.tasks[3]})

	f.Must(capped, "claim_task", map[string]any{"task_id": f.tasks[0]})
}

func TestDailyCostQuotaWarnsAtNinetyPercent(t *testing.T) {
	f := newFixture(t, 2)
	tok := f.worker("w1")
	f.setQuota("w1", map[string]any{"max_cost_eur_per_day": 10})
	f.assign(f.tasks[0], "w1")
	f.assign(f.tasks[1], "w1")
	f.Must(tok, "claim_task", map[string]any{"task_id": f.tasks[0]})

	decision := func() map[string]any {
		_, out := f.REST("GET", "/api/v1/decisions", nil)
		for _, d := range out["decisions"].([]any) {
			if d := d.(map[string]any); d["id"] == "quota:w1" {
				return d
			}
		}
		return nil
	}
	f.Must(tok, "update_status", map[string]any{"task_id": f.tasks[0], "status": "in_progress", "cost_eur": 8.9})
	if decision() != nil {
		t.Fatal("decision raised below 90%")
	}
	f.Must(tok, "update_status", map[string]any{"task_id": f.tasks[0], "status": "in_progress", "cost_eur": 9.1})
	d := decision()
	if d == nil || d["kind"] != "budget" || d["agent_id"] != "w1" {
		t.Fatalf("decision at 91%%: %v", d)
	}
	if u := f.usage("w1"); u["level"] != "warning" || u["cost_eur"].(float64) < 9.09 {
		t.Fatalf("usage: %v", u)
	}

	f.Must(tok, "update_status", map[string]any{"task_id": f.tasks[0], "status": "in_progress", "cost_eur": 10.5})
	f.Must(tok, "release_task", map[string]any{"task_id": f.tasks[0]})
	f.MustFail(tok, "claim_task", map[string]any{"task_id": f.tasks[1]}, "quota_exceeded")

	f.clk.Set(time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC))
	if decision() != nil {
		t.Fatal("decision survived the window reset")
	}
	f.Must(tok, "claim_task", map[string]any{"task_id": f.tasks[1]})
}

func TestQuotaEndpoints(t *testing.T) {
	f := newFixture(t, 1)
	f.worker("w1")
	f.worker("w2")
	f.Register("old", "worker", "claude-code")
	f.REST("POST", "/api/v1/agents/old/revoke", map[string]any{})

	for name, body := range map[string]map[string]any{
		"negative cost":       {"max_cost_eur_per_day": -1},
		"negative tasks":      {"max_tasks_per_day": -1},
		"fraction":            {"max_concurrent_tasks": 1.5},
		"unknown field":       {"max_tasks": 3},
		"cost too high":       {"max_cost_eur_per_day": 1000000.5},
		"tasks too high":      {"max_tasks_per_day": 1000001},
		"concurrent too high": {"max_concurrent_tasks": 1000001},
		"negative concurrent": {"max_concurrent_tasks": -1},
	} {
		if code, _ := f.REST("PUT", "/api/v1/agents/w1/quota", body); code != 400 {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
	if code, _ := f.REST("PUT", "/api/v1/agents/nobody/quota", map[string]any{}); code != 404 {
		t.Errorf("unknown agent: %d", code)
	}
	if code, _ := f.REST("PUT", "/api/v1/agents/old/quota", map[string]any{}); code != 409 {
		t.Errorf("revoked agent: %d", code)
	}

	if code, out := f.REST("PUT", "/api/v1/agents/w1/quota", map[string]any{"max_cost_eur_per_day": 1e6, "max_tasks_per_day": 1000000, "max_concurrent_tasks": 1000000}); code != 200 {
		t.Fatalf("upper bounds: %d %v", code, out)
	}
	var many []string
	for i := range 501 {
		many = append(many, fmt.Sprintf("x%d", i))
	}
	if code, out := f.REST("POST", "/api/v1/agents/quota-bulk", map[string]any{"ids": many[:500], "quota": map[string]any{}}); code != 200 || out["failed"] != float64(500) {
		t.Fatalf("bulk of 500: %d %v", code, out["failed"])
	}
	if code, _ := f.REST("POST", "/api/v1/agents/quota-bulk", map[string]any{"ids": many, "quota": map[string]any{}}); code != 400 {
		t.Fatalf("bulk of 501: %d", code)
	}

	code, out := f.REST("PUT", "/api/v1/agents/w1/quota", map[string]any{"max_cost_eur_per_day": 2.5, "max_tasks_per_day": 4, "max_concurrent_tasks": 2})
	if code != 200 || out["quota"].(map[string]any)["max_tasks_per_day"] != float64(4) || out["quota_usage"].(map[string]any)["level"] != "ok" {
		t.Fatalf("put: %d %v", code, out)
	}

	code, out = f.REST("POST", "/api/v1/agents/quota-bulk", map[string]any{"ids": []string{"w1", "w2", "w2", "nobody", "old"}, "quota": map[string]any{"max_tasks_per_day": 7}})
	if code != 200 || out["succeeded"] != float64(2) || out["failed"] != float64(2) {
		t.Fatalf("bulk: %d %v", code, out)
	}
	results := out["results"].([]any)
	if len(results) != 4 || results[2].(map[string]any)["error"].(map[string]any)["code"] != "not_found" || results[3].(map[string]any)["error"].(map[string]any)["code"] != "conflict" {
		t.Fatalf("bulk results: %v", results)
	}
	for _, body := range []map[string]any{
		{"ids": []string{}, "quota": map[string]any{}},
		{"ids": []string{"w1"}},
		{"ids": []string{"w1"}, "quota": map[string]any{"max_tasks_per_day": -2}},
		{"ids": []string{""}, "quota": map[string]any{}},
	} {
		if code, _ := f.REST("POST", "/api/v1/agents/quota-bulk", body); code != 400 {
			t.Errorf("bulk %v: %d, want 400", body, code)
		}
	}

	_, list := f.REST("GET", "/api/v1/agents", nil)
	for _, a := range list["agents"].([]any) {
		a := a.(map[string]any)
		want := float64(7)
		if a["id"] == "old" || a["id"] == "lead" {
			want = 0
		}
		if got := a["quota"].(map[string]any)["max_tasks_per_day"]; got != want {
			t.Errorf("agent %v max_tasks_per_day %v, want %v", a["id"], got, want)
		}
		if a["quota_usage"] == nil {
			t.Errorf("agent %v has no quota_usage", a["id"])
		}
	}

	_, audit := f.REST("GET", "/api/v1/audit?tool=set_agent_quota", nil)
	if n := len(audit["events"].([]any)); n != 4 {
		t.Errorf("%d set_agent_quota audit events, want 3", n)
	}
}

func TestQuotaWarningIsPublishedOncePerDay(t *testing.T) {
	f := newFixture(t, 2)
	sub := f.Srv.Hub.Subscribe([]string{"control"})
	defer sub.Close()
	warnings := func(wait time.Duration) []map[string]any {
		var out []map[string]any
		timeout := time.After(wait)
		for {
			select {
			case ev := <-sub.C():
				if ev.Type == "quota.warning" {
					out = append(out, ev.Data.(map[string]any))
				}
			case <-timeout:
				return out
			}
		}
	}
	tok := f.worker("w1")
	f.setQuota("w1", map[string]any{"max_cost_eur_per_day": 10})
	f.assign(f.tasks[0], "w1")
	f.Must(tok, "claim_task", map[string]any{"task_id": f.tasks[0]})
	cost := func(eur float64) {
		f.Must(tok, "update_status", map[string]any{"task_id": f.tasks[0], "status": "in_progress", "cost_eur": eur})
	}

	other := f.worker("w2")
	f.setQuota("w2", map[string]any{"max_tasks_per_day": 1})
	f.Must(f.lead, "assign", map[string]any{"task_id": f.tasks[1], "agent_id": "w2"})
	f.Must(other, "claim_task", map[string]any{"task_id": f.tasks[1]})
	if w := warnings(time.Second); len(w) != 1 || w[0]["agent_id"] != "w2" || w[0]["level"] != "exceeded" {
		t.Fatalf("warning on claim: %v", w)
	}

	cost(8.9)
	if w := warnings(150 * time.Millisecond); len(w) != 0 {
		t.Fatalf("warning below 90%%: %v", w)
	}
	cost(9.1)
	w := warnings(time.Second)
	if len(w) != 1 || w[0]["agent_id"] != "w1" || w[0]["level"] != "warning" || w[0]["tasks"] != 1 || w[0]["concurrent_tasks"] != 1 {
		t.Fatalf("warning at 91%%: %v", w)
	}
	cost(9.5)
	if w := warnings(150 * time.Millisecond); len(w) != 0 {
		t.Fatalf("warned twice in one day: %v", w)
	}
	f.setQuota("w1", map[string]any{"max_cost_eur_per_day": 20})
	if w := warnings(150 * time.Millisecond); len(w) != 0 {
		t.Fatalf("warning under a raised quota: %v", w)
	}
	f.setQuota("w1", map[string]any{"max_cost_eur_per_day": 10})
	if w := warnings(time.Second); len(w) != 1 || w[0]["level"] != "warning" {
		t.Fatalf("no warning after the quota was lowered again: %v", w)
	}
	cost(10.5)
	if w := warnings(150 * time.Millisecond); len(w) != 0 {
		t.Fatalf("warned again on exceeding: %v", w)
	}

	if u := f.usage("w1"); u["concurrent_tasks"] != float64(1) || u["tasks"] != float64(1) || u["level"] != "exceeded" {
		t.Fatalf("usage: %v", u)
	}
	_, out := f.REST("GET", "/api/v1/decisions", nil)
	var summary, w2summary string
	for _, d := range out["decisions"].([]any) {
		switch d := d.(map[string]any); d["id"] {
		case "quota:w1":
			summary = d["summary"].(string)
		case "quota:w2":
			w2summary = d["summary"].(string)
		}
	}
	if !strings.Contains(w2summary, ", 1 of 1 tasks started,") {
		t.Fatalf("w2 summary %q", w2summary)
	}
	if summary != "€10.50 of 10 spent, 1 of unlimited tasks started, 1 of unlimited running; the window resets 2026-11-22T00:00:00Z" {
		t.Fatalf("summary %q", summary)
	}

	f.setQuota("w1", map[string]any{})
	f.clk.Set(time.Date(2026, 11, 21, 11, 0, 0, 0, time.UTC))
	cost(30)
	if w := warnings(150 * time.Millisecond); len(w) != 0 {
		t.Fatalf("warning without a quota: %v", w)
	}
}
