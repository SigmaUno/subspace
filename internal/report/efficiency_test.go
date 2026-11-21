package report

import (
	"reflect"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/model"
)

func (f *fixture) evc(task string, min int, tool, from, to, by string, cost float64) {
	f.ev(task, min, tool, from, to, by)
	f.audit[len(f.audit)-1].After["cost_used"] = cost
}

func efficiencySnapshot() Snapshot {
	f := &fixture{}
	f.task("T-1", "a1", model.StatusDone, 4)
	f.evc("T-1", 0, "claim_task", "assigned", "in_progress", "a1", 0)
	f.evc("T-1", 10, "submit", "in_progress", "submitted", "a1", 1)
	f.evc("T-1", 20, "review", "submitted", "changes_requested", "r1", 1)
	f.evc("T-1", 25, "claim_task", "changes_requested", "in_progress", "a1", 1)
	f.evc("T-1", 40, "submit", "in_progress", "submitted", "a1", 3)
	f.evc("T-1", 50, "review", "submitted", "changes_requested", "r1", 3)
	f.evc("T-1", 55, "claim_task", "changes_requested", "in_progress", "a1", 3)
	f.evc("T-1", 65, "submit", "in_progress", "submitted", "a1", 4)
	f.evc("T-1", 70, "review", "submitted", "approved", "r1", 4)
	f.evc("T-1", 80, "mark_done", "approved", "done", "owner", 4)
	f.task("T-2", "a2", model.StatusDone, 2)
	f.finished("T-2", 0, "a2")
	f.task("T-3", "a2", model.StatusCancelled, 1.5)
	f.task("T-4", "a1", model.StatusApproved, 1)
	f.task("T-5", "a1", model.StatusInProgress, 0.5)
	return Snapshot{
		Brief: model.Brief{ID: "B-1"}, Tasks: f.tasks, Audit: f.audit, Now: at(300),
		Agents: map[string]string{"a1": "anthropic", "a2": "openai"},
		PRs: map[string]PR{
			"T-2": {State: "merged", Additions: 30, Deletions: 10, HasDiff: true},
			"T-4": {State: "open", Additions: 5, Deletions: 5, HasDiff: true},
			"T-5": {State: "merged"},
		},
	}
}

func TestEfficiency(t *testing.T) {
	e := Build(efficiencySnapshot()).Efficiency
	perDone, perMerged, perLine := 3.0, 6.5/3, 0.06
	if e.DoneTasks != 2 || e.MergedTasks != 3 || *e.CostPerDoneTask != perDone || *e.CostPerMergedTask != round(perMerged) {
		t.Fatalf("task ratios: %+v", e)
	}
	if e.AcceptedLines != 50 || *e.CostPerAcceptedLine != perLine {
		t.Fatalf("line ratio: %d %v", e.AcceptedLines, *e.CostPerAcceptedLine)
	}
	if e.ReviewOverheadEUR != 3 || e.ReviewOverheadPct != round(100.0/3) {
		t.Fatalf("review overhead: %v %v", e.ReviewOverheadEUR, e.ReviewOverheadPct)
	}
	if e.CancelledEUR != 1.5 || e.ReworkedEUR != 3 || e.WastedEUR != 4.5 {
		t.Fatalf("wasted: %+v", e)
	}
	want := map[string]float64{"done": 6, "cancelled": 1.5, "approved": 1, "in_progress": 0.5}
	if !reflect.DeepEqual(e.CostByStatus, want) {
		t.Fatalf("cost by status: %v", e.CostByStatus)
	}
}

func TestEfficiencyWithoutPRStatsOrCompletedTasks(t *testing.T) {
	f := &fixture{}
	f.task("T-1", "a1", model.StatusInProgress, 2)
	e := Build(Snapshot{Tasks: f.tasks, Audit: f.audit, Now: at(10)}).Efficiency
	if e.CostPerDoneTask != nil || e.CostPerMergedTask != nil || e.CostPerAcceptedLine != nil || e.WastedEUR != 0 || e.ReviewOverheadPct != 0 {
		t.Fatalf("efficiency: %+v", e)
	}
	md := Markdown(Build(Snapshot{Tasks: f.tasks, Audit: f.audit, Now: at(10)}))
	if !strings.Contains(md, "Cost per accepted line: n/a (0 lines)") {
		t.Fatal(md)
	}
}
