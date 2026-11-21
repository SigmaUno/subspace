package agentstats

import (
	"reflect"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/usage"
)

var base = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

type fixture struct {
	id     int64
	events []*model.AuditEvent
}

func (f *fixture) ev(task, agent, tool, from, to string, at time.Duration, note string) {
	f.id++
	e := &model.AuditEvent{ID: f.id, TS: base.Add(at), AgentID: agent, Tool: tool, TaskID: &task,
		Before: map[string]any{"status": from}, After: map[string]any{"status": to}}
	if note != "" {
		e.Note = &note
	}
	f.events = append(f.events, e)
}

func (f *fixture) work(task, agent string, start, end time.Duration, to string) {
	f.ev(task, agent, "claim_task", model.StatusAssigned, model.StatusInProgress, start, "")
	f.ev(task, agent, "submit", model.StatusInProgress, to, end, "")
}

func task(id, assignee, status string, cost float64) *model.Task {
	return &model.Task{ID: id, Assignee: &assignee, Status: status, CostUsed: cost, UpdatedAt: base}
}

func agent(id string) *model.Agent {
	return &model.Agent{ID: id, Role: model.RoleWorker, Provider: "anthropic", Status: model.AgentActive}
}

func TestBuildAggregates(t *testing.T) {
	var f fixture
	f.work("T-1", "a", -4*time.Hour, -4*time.Hour+10*time.Minute, model.StatusSubmitted)
	f.ev("T-1", "r", "review", model.StatusSubmitted, model.StatusApproved, -3*time.Hour, "")
	f.ev("T-1", "owner", "done", model.StatusApproved, model.StatusDone, -2*time.Hour, "")

	f.work("T-2", "a", -30*time.Hour, -30*time.Hour+30*time.Minute, model.StatusSubmitted)
	f.ev("T-2", "r", "review", model.StatusSubmitted, model.StatusChangesRequested, -29*time.Hour, "")
	f.ev("T-2", "a", "claim_task", model.StatusChangesRequested, model.StatusInProgress, -28*time.Hour, "")
	f.ev("T-2", "a", "submit", model.StatusInProgress, model.StatusSubmitted, -28*time.Hour+20*time.Minute, "")
	f.ev("T-2", "r", "review", model.StatusSubmitted, model.StatusApproved, -27*time.Hour, "")

	f.ev("T-3", "b", "claim_task", model.StatusAssigned, model.StatusInProgress, -50*time.Minute, "")
	f.ev("T-3", "system", "lease_release", model.StatusInProgress, model.StatusAssigned, -35*time.Minute, "lease expired")
	f.ev("T-3", "a", "claim_task", model.StatusAssigned, model.StatusInProgress, -30*time.Minute, "")
	f.ev("T-3", "a", "block", model.StatusInProgress, model.StatusBlocked, -20*time.Minute, "stuck")

	f.work("T-4", "a", -10*time.Hour, -10*time.Hour+60*time.Minute, model.StatusApproved)

	snap := Snapshot{
		Now:    base,
		Agents: []*model.Agent{agent("a"), agent("b"), agent("c")},
		Tasks: []*model.Task{task("T-1", "a", model.StatusDone, 1), task("T-2", "a", model.StatusApproved, 2),
			task("T-3", "a", model.StatusBlocked, 9), task("T-4", "a", model.StatusApproved, 3)},
		Audit: f.events,
		Usage: []usage.Record{{TaskID: "T-1", TokensIn: 1000, TokensOut: 200}, {TaskID: "T-2", TokensIn: 300},
			{TaskID: "T-3", TokensIn: 99999}, {TaskID: "T-1", TokensOut: 100}},
	}
	got := Build(snap)

	byID := map[string]Stats{}
	for _, s := range got {
		byID[s.AgentID] = s
	}
	a := byID["a"]
	rate := 0.5
	if a.TasksCompleted != 3 || a.CostPerTask != 2 || a.ReviewRoundsPerTask != 0.33 ||
		a.FirstPassApprovalRate == nil || *a.FirstPassApprovalRate != rate {
		t.Fatalf("a: %+v", a)
	}
	if a.AvgDurationSeconds != 2400 || a.P90DurationSeconds != 3600 {
		t.Fatalf("a durations: avg %v p90 %v", a.AvgDurationSeconds, a.P90DurationSeconds)
	}
	if a.TokensPerTask != 533.3 {
		t.Fatalf("a tokens: %v", a.TokensPerTask)
	}
	if a.Blocked != 1 || a.Expired != 0 {
		t.Fatalf("a blocked/expired: %d/%d", a.Blocked, a.Expired)
	}
	if b := byID["b"]; b.Expired != 1 || b.TasksCompleted != 0 || b.Score >= 0.5 {
		t.Fatalf("b: %+v", b)
	}
	if c := byID["c"]; c.Score != 0.5 || c.FirstPassApprovalRate != nil || c.AvgDurationSeconds != 0 {
		t.Fatalf("c: %+v", c)
	}

	want := []TrendPoint{{"2026-11-14", 0}, {"2026-11-15", 0}, {"2026-11-16", 0}, {"2026-11-17", 0},
		{"2026-11-18", 0}, {"2026-11-19", 1}, {"2026-11-20", 2}}
	if !reflect.DeepEqual(a.Trend, want) {
		t.Fatalf("trend: %+v", a.Trend)
	}
	if !reflect.DeepEqual(Build(snap), got) {
		t.Fatal("Build is not deterministic")
	}
}

func TestScoreOrdering(t *testing.T) {
	rate := func(v float64) *float64 { return &v }
	cases := []struct {
		name   string
		lo, hi Stats
	}{
		{"clean first pass beats rework", Stats{TasksCompleted: 6, FirstPassApprovalRate: rate(0.3), ReviewRoundsPerTask: 1.5},
			Stats{TasksCompleted: 6, FirstPassApprovalRate: rate(1)}},
		{"blocked and expired hurt", Stats{TasksCompleted: 5, Blocked: 3, Expired: 2, FirstPassApprovalRate: rate(1)},
			Stats{TasksCompleted: 5, FirstPassApprovalRate: rate(1)}},
		{"failures below neutral", Stats{Expired: 4}, Stats{}},
		{"track record beats unproven", Stats{}, Stats{TasksCompleted: 10, FirstPassApprovalRate: rate(0.9)}},
	}
	for _, c := range cases {
		if lo, hi := Score(c.lo), Score(c.hi); lo >= hi {
			t.Errorf("%s: %v >= %v", c.name, lo, hi)
		}
	}
	if s := Score(Stats{TasksCompleted: 1, FirstPassApprovalRate: rate(1)}); s >= Score(Stats{TasksCompleted: 20, FirstPassApprovalRate: rate(1)}) {
		t.Errorf("confidence should grow with evidence: %v", s)
	}
}
