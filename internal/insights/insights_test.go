package insights

import (
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

var now = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func errs(agent, tool, code, fp, task string, n int) []ToolError {
	var out []ToolError
	for i := range n {
		out = append(out, ToolError{ID: int64(i + 1), TS: now.Add(-time.Duration(i+1) * time.Minute), AgentID: agent, Tool: tool, Code: code, TaskID: task, Fingerprint: fp})
	}
	return out
}

func audit(id int64, agent, tool, task, status string, note *string, ago time.Duration) *model.AuditEvent {
	return &model.AuditEvent{ID: id, TS: now.Add(-ago), AgentID: agent, Tool: tool, TaskID: &task, After: map[string]any{"status": status}, Note: note}
}

func task(id, assignee, status string, cost float64) *model.Task {
	return &model.Task{ID: id, Assignee: &assignee, Status: status, CostUsed: cost, UpdatedAt: now.Add(-time.Hour)}
}

func agent(id, role string) *model.Agent {
	return &model.Agent{ID: id, Role: role, Status: model.AgentActive}
}

func sys(id int64, agentID, body string, ago time.Duration) *model.Message {
	return &model.Message{ID: id, ThreadKind: model.ThreadAgent, ThreadID: agentID, AuthorID: "system", Kind: model.MsgSystem, Body: body, CreatedAt: now.Add(-ago)}
}

type want struct {
	rule, severity, agent string
	count                 int
	evidence              []Evidence
}

func TestAnalyze(t *testing.T) {
	expired := ptr("lease expired")
	cases := []struct {
		name string
		in   Input
		want []want
	}{
		{
			name: "quiet fleet",
			in:   Input{Agents: []*model.Agent{agent("w1", "worker")}, Errors: errs("w1", "claim_task", "conflict", "a", "T-1", 2)},
		},
		{
			name: "repeated identical errors warn then escalate",
			in:   Input{Errors: append(errs("w1", "submit", "conflict", "a", "T-1", 3), errs("w2", "submit", "conflict", "a", "T-2", 6)...)},
			want: []want{
				{RuleRepeatedErrors, SeverityHigh, "w2", 6, []Evidence{{EvidenceError, "1"}, {EvidenceError, "2"}, {EvidenceError, "3"}, {EvidenceError, "4"}, {EvidenceError, "5"}, {EvidenceError, "6"}}},
				{RuleRepeatedErrors, SeverityWarning, "w1", 3, []Evidence{{EvidenceError, "1"}, {EvidenceError, "2"}, {EvidenceError, "3"}}},
			},
		},
		{
			name: "different error texts are not identical",
			in: Input{Errors: append(append(errs("w1", "submit", "conflict", "a", "T-1", 2),
				errs("w1", "submit", "conflict", "b", "T-1", 2)...), errs("w1", "submit", "conflict", "c", "T-1", 2)...)},
		},
		{
			name: "errors outside the window are ignored",
			in: Input{Errors: []ToolError{
				{ID: 1, TS: now.Add(-25 * time.Hour), AgentID: "w1", Tool: "t", Code: "c", Fingerprint: "a"},
				{ID: 2, TS: now.Add(-26 * time.Hour), AgentID: "w1", Tool: "t", Code: "c", Fingerprint: "a"},
				{ID: 3, TS: now.Add(-27 * time.Hour), AgentID: "w1", Tool: "t", Code: "c", Fingerprint: "a"},
			}},
		},
		{
			name: "claims on unassigned tasks",
			in: Input{Errors: func() []ToolError {
				es := errs("w1", "claim_task", "not_assigned", "", "", 4)
				for i := range es {
					es[i].Fingerprint = strconv.Itoa(i)
				}
				return es
			}()},
			want: []want{{RuleUnassignedClaim, SeverityWarning, "w1", 4, []Evidence{{EvidenceError, "1"}, {EvidenceError, "2"}, {EvidenceError, "3"}, {EvidenceError, "4"}}}},
		},
		{
			name: "lease expiries are attributed to the claimer",
			in: Input{
				Tasks: []*model.Task{task("T-1", "w2", model.StatusAssigned, 0), task("T-2", "w2", model.StatusAssigned, 0)},
				Audit: []*model.AuditEvent{
					audit(1, "w1", "claim_task", "T-1", model.StatusInProgress, nil, 3*time.Hour),
					audit(2, "system", "lease_release", "T-1", model.StatusAssigned, expired, 2*time.Hour),
					audit(3, "w1", "claim_task", "T-2", model.StatusInProgress, nil, 90*time.Minute),
					audit(4, "system", "lease_release", "T-2", model.StatusAssigned, expired, time.Hour),
					audit(5, "system", "lease_release", "T-2", model.StatusAssigned, ptr("released"), time.Minute),
				},
			},
			want: []want{{RuleLeaseExpiries, SeverityWarning, "w1", 2, []Evidence{{EvidenceAudit, "2"}, {EvidenceAudit, "4"}}}},
		},
		{
			name: "silence while in progress",
			in: Input{
				Agents: []*model.Agent{agent("w1", "worker"), agent("w2", "worker"), agent("w3", "worker")},
				Tasks: []*model.Task{
					{ID: "T-1", Assignee: ptr("w1"), Status: model.StatusInProgress, LastHeartbeatAt: ptr(now.Add(-30 * time.Minute)), UpdatedAt: now.Add(-2 * time.Hour)},
					{ID: "T-2", Assignee: ptr("w2"), Status: model.StatusInProgress, LastHeartbeatAt: ptr(now.Add(-2 * time.Minute)), UpdatedAt: now.Add(-2 * time.Hour)},
					{ID: "T-3", Assignee: ptr("w3"), Status: model.StatusInProgress, UpdatedAt: now.Add(-90 * time.Minute)},
				},
			},
			want: []want{
				{RuleSilence, SeverityHigh, "w3", 1, []Evidence{{EvidenceTask, "T-3"}}},
				{RuleSilence, SeverityWarning, "w1", 1, []Evidence{{EvidenceTask, "T-1"}}},
			},
		},
		{
			name: "recent agent contact ends silence",
			in: Input{
				Agents: []*model.Agent{{ID: "w1", Role: "worker", Status: model.AgentActive, LastSeenAt: ptr(now.Add(-time.Minute))}},
				Tasks:  []*model.Task{{ID: "T-1", Assignee: ptr("w1"), Status: model.StatusInProgress, UpdatedAt: now.Add(-3 * time.Hour)}},
			},
		},
		{
			name: "changes requested ratio",
			in: Input{
				Tasks: []*model.Task{task("T-1", "w1", model.StatusInProgress, 0), task("T-2", "w2", model.StatusInProgress, 0)},
				Audit: []*model.AuditEvent{
					audit(1, "r", "review", "T-1", model.StatusChangesRequested, nil, 5*time.Hour),
					audit(2, "r", "review", "T-1", model.StatusChangesRequested, nil, 4*time.Hour),
					audit(3, "r", "review", "T-1", model.StatusChangesRequested, nil, 3*time.Hour),
					audit(4, "r", "review", "T-1", model.StatusApproved, nil, 2*time.Hour),
					audit(5, "r", "review", "T-2", model.StatusChangesRequested, nil, 2*time.Hour),
					audit(6, "r", "review", "T-2", model.StatusApproved, nil, time.Hour),
				},
			},
			want: []want{{RuleChangesRatio, SeverityHigh, "w1", 3, []Evidence{{EvidenceAudit, "1"}, {EvidenceAudit, "2"}, {EvidenceAudit, "3"}}}},
		},
		{
			name: "loop protection trips",
			in: Input{Messages: []*model.Message{
				sys(1, "w1", "Agent paused: loop: same call repeated too often", time.Hour),
				sys(2, "w2", "Agent paused: loop: same call repeated too often", 3*time.Hour),
				sys(3, "w2", "Agent paused: loop: same call repeated too often", time.Hour),
				sys(4, "w3", "Agent paused: flapping", time.Hour),
			}},
			want: []want{
				{RuleLoopTrips, SeverityHigh, "w2", 2, []Evidence{{EvidenceMessage, "2"}, {EvidenceMessage, "3"}}},
				{RuleLoopTrips, SeverityWarning, "w1", 1, []Evidence{{EvidenceMessage, "1"}}},
			},
		},
		{
			name: "cost per task against peers",
			in: Input{
				Agents: []*model.Agent{agent("w1", "worker"), agent("w2", "worker"), agent("w3", "worker"), agent("w4", "worker"), agent("t1", "tester")},
				Tasks: []*model.Task{
					task("T-1", "w1", model.StatusDone, 1), task("T-2", "w1", model.StatusApproved, 1),
					task("T-3", "w2", model.StatusDone, 1), task("T-4", "w2", model.StatusDone, 1),
					task("T-5", "w3", model.StatusDone, 1.2), task("T-6", "w3", model.StatusDone, 0.8),
					task("T-7", "w4", model.StatusDone, 4), task("T-8", "w4", model.StatusDone, 4),
					task("T-9", "t1", model.StatusDone, 50), task("T-10", "t1", model.StatusDone, 50),
					{ID: "T-11", Assignee: ptr("w1"), Status: model.StatusInProgress, CostUsed: 99, UpdatedAt: now},
				},
			},
			want: []want{{RuleHighCost, SeverityHigh, "w4", 2, []Evidence{{EvidenceTask, "T-7"}, {EvidenceTask, "T-8"}}}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := c.in
			in.Now = now
			got := Analyze(in)
			if len(got) != len(c.want) {
				t.Fatalf("findings: %+v", got)
			}
			for i, w := range c.want {
				g := got[i]
				if g.Rule != w.rule || g.Severity != w.severity || g.AgentID != w.agent || g.Count != w.count || !reflect.DeepEqual(g.Evidence, w.evidence) {
					t.Fatalf("finding %d: %+v want %+v", i, g, w)
				}
				if g.ID != w.agent+":"+w.rule || g.Summary == "" || g.TaskIDs == nil || g.Since.IsZero() {
					t.Fatalf("finding %d fields: %+v", i, g)
				}
			}
		})
	}
}

func TestAnalyzeIsDeterministic(t *testing.T) {
	var in Input
	for i := range 20 {
		id := "w" + strconv.Itoa(i)
		in.Errors = append(in.Errors, errs(id, "claim_task", "not_assigned", "x", "T-"+strconv.Itoa(i), 3+i%5)...)
	}
	in.Now = now
	first := Analyze(in)
	for range 10 {
		if got := Analyze(in); !reflect.DeepEqual(got, first) {
			t.Fatalf("analysis differs between runs")
		}
	}
}
