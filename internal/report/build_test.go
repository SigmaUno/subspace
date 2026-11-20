package report

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func at(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

type fixture struct {
	tasks  []*model.Task
	audit  []*model.AuditEvent
	nextID int64
}

func (f *fixture) task(id, assignee, status string, cost float64, deps ...string) *model.Task {
	t := &model.Task{ID: id, Title: "task " + id, Status: status, CostUsed: cost, DependsOn: deps, CreatedAt: t0, UpdatedAt: t0}
	if assignee != "" {
		t.Assignee = &assignee
	}
	f.tasks = append(f.tasks, t)
	return t
}

func (f *fixture) ev(task string, min int, tool, from, to, by string) {
	f.nextID++
	id := task
	f.audit = append(f.audit, &model.AuditEvent{
		ID: f.nextID, TS: at(min), AgentID: by, Tool: tool, TaskID: &id,
		Before: map[string]any{"status": from}, After: map[string]any{"status": to},
	})
}

func (f *fixture) finished(task string, start int, by string) {
	f.ev(task, start, "claim_task", "assigned", "in_progress", by)
	f.ev(task, start+5, "submit", "in_progress", "submitted", by)
	f.ev(task, start+8, "review", "submitted", "approved", "r1")
	f.ev(task, start+10, "done", "approved", "done", "owner")
}

func scenario() Snapshot {
	f := &fixture{}
	f.task("T-1", "a1", model.StatusDone, 2)
	f.ev("T-1", 10, "claim_task", "assigned", "in_progress", "a1")
	f.ev("T-1", 20, "block", "in_progress", "blocked", "a1")
	f.ev("T-1", 30, "update_task", "blocked", "in_progress", "rex")
	f.ev("T-1", 50, "submit", "in_progress", "submitted", "a1")
	f.ev("T-1", 60, "review", "submitted", "changes_requested", "r1")
	f.ev("T-1", 65, "claim_task", "changes_requested", "in_progress", "a1")
	f.ev("T-1", 80, "submit", "in_progress", "submitted", "a1")
	f.ev("T-1", 90, "review", "submitted", "approved", "r1")
	f.ev("T-1", 100, "done", "approved", "done", "owner")

	f.task("T-2", "a2", model.StatusDone, 1, "T-1")
	f.ev("T-2", 110, "claim_task", "assigned", "in_progress", "a2")
	f.ev("T-2", 120, "submit", "in_progress", "submitted", "a2")
	f.ev("T-2", 140, "review", "submitted", "approved", "r1")
	f.ev("T-2", 150, "done", "approved", "done", "owner")

	f.task("T-3", "a1", model.StatusInProgress, 0.5, "T-1", "T-2")
	f.ev("T-3", 160, "claim_task", "assigned", "in_progress", "a1")

	f.task("T-4", "", model.StatusCancelled, 0)
	f.ev("T-4", 5, "cancel", "ready", "cancelled", "owner")

	for i := 5; i <= 11; i++ {
		id := fmt.Sprintf("T-%d", i)
		f.task(id, "a2", model.StatusDone, 0.1)
		f.finished(id, 0, "a2")
	}
	f.task("T-12", "a1", model.StatusInProgress, 5)
	f.ev("T-12", 100, "claim_task", "assigned", "in_progress", "a1")

	return Snapshot{
		Brief:  model.Brief{ID: "B-1", Title: "Demo"},
		Tasks:  f.tasks,
		Agents: map[string]string{"a1": "anthropic", "a2": "openai"},
		Audit:  f.audit,
		Now:    at(300),
	}
}

func TestBuildScenario(t *testing.T) {
	r := Build(scenario())

	if r.Total != 12 || !reflect.DeepEqual(r.Counts, map[string]int{"done": 9, "in_progress": 2, "cancelled": 1}) {
		t.Fatalf("counts: %d %v", r.Total, r.Counts)
	}

	t1 := r.Tasks[0]
	want := map[string]int64{
		"queue": 600, "work": 2700, "review": 1200, "blocked": 600, "active": 5400, "lead": 6000,
	}
	got := map[string]int64{
		"queue": t1.QueueSeconds, "work": t1.WorkSeconds, "review": t1.ReviewSeconds,
		"blocked": t1.BlockedSeconds, "active": t1.ActiveSeconds, "lead": t1.LeadSeconds,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("T-1 durations: got %v want %v", got, want)
	}
	if t1.Claims != 2 || t1.ReviewRounds != 1 || len(t1.Reviews) != 2 {
		t.Fatalf("T-1 rounds: claims=%d rounds=%d reviews=%v", t1.Claims, t1.ReviewRounds, t1.Reviews)
	}
	if t1.Reviews[0].Verdict != "changes_requested" || t1.Reviews[0].WaitSeconds != 600 || t1.Reviews[1].Verdict != "approve" {
		t.Fatalf("T-1 reviews: %+v", t1.Reviews)
	}
	if len(t1.Timeline) != 9 || t1.Timeline[0].ElapsedSeconds != 600 || t1.Timeline[1].ElapsedSeconds != 600 {
		t.Fatalf("T-1 timeline: %+v", t1.Timeline)
	}
	if t1.ClaimedAt == nil || !t1.ClaimedAt.Equal(at(10)) || t1.SubmittedAt == nil || !t1.SubmittedAt.Equal(at(50)) || t1.DoneAt == nil || !t1.DoneAt.Equal(at(100)) {
		t.Fatalf("T-1 milestones: %v %v %v", t1.ClaimedAt, t1.SubmittedAt, t1.DoneAt)
	}
	if t1.TimeInStatus["changes_requested"] != 300 {
		t.Fatalf("T-1 time in status: %v", t1.TimeInStatus)
	}

	if t3 := r.Tasks[2]; t3.ActiveSeconds != 8400 || t3.QueueSeconds != 9600 || t3.DoneAt != nil {
		t.Fatalf("T-3 open task: %+v", t3)
	}
	if t4 := r.Tasks[3]; t4.LeadSeconds != 300 || t4.ClaimedAt != nil {
		t.Fatalf("T-4 cancelled task: %+v", t4)
	}

	if r.BlockedSeconds != 600 {
		t.Fatalf("blocked: %d", r.BlockedSeconds)
	}

	wantCost := Cost{
		TotalEUR:   9.2,
		ByAgent:    map[string]float64{"a1": 7.5, "a2": 1.7, "unassigned": 0},
		ByProvider: map[string]float64{"anthropic": 7.5, "openai": 1.7, "unknown": 0},
	}
	if r.Cost.TotalEUR != wantCost.TotalEUR || !reflect.DeepEqual(r.Cost.ByAgent, wantCost.ByAgent) || !reflect.DeepEqual(r.Cost.ByProvider, wantCost.ByProvider) {
		t.Fatalf("cost: %+v", r.Cost)
	}
	if r.Cost.ByTask["T-12"] != 5 || r.Cost.ByTask["T-1"] != 2 {
		t.Fatalf("cost by task: %v", r.Cost.ByTask)
	}

	th := r.Throughput
	var hourly []int
	for _, b := range th.Hourly {
		hourly = append(hourly, b.Done)
	}
	if th.Done != 9 || th.WindowHours != 5 || th.PerHour != 1.8 || !reflect.DeepEqual(hourly, []int{7, 1, 1, 0, 0, 0}) {
		t.Fatalf("throughput: %+v", th)
	}

	if !reflect.DeepEqual(r.CriticalPath.TaskIDs, []string{"T-1", "T-2", "T-3"}) || r.CriticalPath.Seconds != 5400+2400+8400 {
		t.Fatalf("critical path: %+v", r.CriticalPath)
	}

	if len(r.Outliers) != 3 {
		t.Fatalf("outliers: %+v", r.Outliers)
	}
	if o := r.Outliers[0]; o.TaskID != "T-12" || o.Metric != "active_seconds" || o.Value != 12000 || o.Threshold != 8850 {
		t.Fatalf("duration outlier: %+v", o)
	}
	if o := r.Outliers[1]; o.TaskID != "T-12" || o.Metric != "cost_eur" {
		t.Fatalf("cost outlier: %+v", o)
	}
	if o := r.Outliers[2]; o.TaskID != "T-1" || o.Metric != "cost_eur" {
		t.Fatalf("cost outlier: %+v", o)
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	s := scenario()
	a := Build(s)
	for i, j := 0, len(s.Tasks)-1; i < j; i, j = i+1, j-1 {
		s.Tasks[i], s.Tasks[j] = s.Tasks[j], s.Tasks[i]
	}
	for i, j := 0, len(s.Audit)-1; i < j; i, j = i+1, j-1 {
		s.Audit[i], s.Audit[j] = s.Audit[j], s.Audit[i]
	}
	if b := Build(s); !reflect.DeepEqual(a, b) || Markdown(a) != Markdown(b) {
		t.Fatal("report depends on input order")
	}
}

func TestCriticalPathSurvivesCyclesAndDanglingDeps(t *testing.T) {
	f := &fixture{}
	f.task("T-1", "a1", model.StatusInProgress, 0, "T-2")
	f.task("T-2", "a1", model.StatusInProgress, 0, "T-1", "T-99")
	f.ev("T-1", 0, "claim_task", "assigned", "in_progress", "a1")
	f.ev("T-2", 10, "claim_task", "assigned", "in_progress", "a1")
	r := Build(Snapshot{Tasks: f.tasks, Audit: f.audit, Now: at(60)})
	if len(r.CriticalPath.TaskIDs) == 0 || r.CriticalPath.Seconds == 0 {
		t.Fatalf("critical path: %+v", r.CriticalPath)
	}
}

func TestEmptyBriefReport(t *testing.T) {
	r := Build(Snapshot{Brief: model.Brief{ID: "B-1", Title: "Empty"}, Now: t0})
	if r.Total != 0 || len(r.Tasks) != 0 || len(r.CriticalPath.TaskIDs) != 0 || len(r.Throughput.Hourly) != 0 || r.Outliers == nil {
		t.Fatalf("empty report: %+v", r)
	}
	if md := Markdown(r); !strings.Contains(md, "# B-1") {
		t.Fatal(md)
	}
}

func TestMarkdownSections(t *testing.T) {
	md := Markdown(Build(scenario()))
	for _, want := range []string{
		"# B-1 — Demo",
		"| done | 9 |",
		"| **total** | **12** |",
		"### T-1 task T-1",
		"`submitted → changes_requested` by r1 via review",
		"| anthropic | 7.50 |",
		"Critical path: T-1 → T-2 → T-3 (4h30m00s)",
		"T-12 active_seconds: 12000 (threshold 8850)",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
}
