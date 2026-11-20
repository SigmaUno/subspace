package threads

import (
	"testing"

	"github.com/SigmaUno/subspace/internal/model"
)

func TestCanAccess(t *testing.T) {
	owner := model.Owner
	leader := model.Actor{ID: "rex", Role: model.RoleLeader}
	worker := model.Actor{ID: "claude-1", Role: model.RoleWorker}
	tester := model.Actor{ID: "tester-1", Role: model.RoleTester}
	reviewer := model.Actor{ID: "rev-1", Role: model.RoleReviewer}
	anon := model.Actor{ID: "", Role: model.RoleWorker}

	agentRef := func(id string) Ref { return Ref{Kind: model.ThreadAgent, ID: id} }
	taskRef := func(id string) Ref { return Ref{Kind: model.ThreadTask, ID: id} }
	briefRef := func(id string) Ref { return Ref{Kind: model.ThreadBrief, ID: id} }

	tests := []struct {
		name     string
		actor    model.Actor
		ref      Ref
		assignee string
		reviewer string
		want     bool
	}{
		{"owner agent", owner, agentRef("x"), "", "", true},
		{"owner task", owner, taskRef("T-1"), "", "", true},
		{"owner brief", owner, briefRef("B-1"), "", "", true},

		{"leader any agent", leader, agentRef("claude-1"), "", "", true},
		{"leader any task", leader, taskRef("T-1"), "other", "", true},
		{"leader brief", leader, briefRef("B-1"), "", "", true},

		{"worker own agent", worker, agentRef("claude-1"), "", "", true},
		{"worker other agent", worker, agentRef("claude-2"), "", "", false},
		{"worker assignee task", worker, taskRef("T-1"), "claude-1", "", true},
		{"worker reviewer task", worker, taskRef("T-1"), "claude-2", "claude-1", true},
		{"worker unrelated task", worker, taskRef("T-1"), "claude-2", "rev-1", false},
		{"worker unassigned task", worker, taskRef("T-1"), "", "", false},
		{"worker brief", worker, briefRef("B-1"), "", "", false},

		{"tester own agent", tester, agentRef("tester-1"), "", "", true},
		{"tester assignee task", tester, taskRef("T-2"), "tester-1", "", true},
		{"tester other task", tester, taskRef("T-2"), "claude-1", "", false},

		{"reviewer reviewer task", reviewer, taskRef("T-3"), "claude-1", "rev-1", true},
		{"reviewer not reviewer task", reviewer, taskRef("T-3"), "claude-1", "rev-2", false},
		{"reviewer brief", reviewer, briefRef("B-1"), "", "", false},

		{"empty actor id unassigned task", anon, taskRef("T-4"), "", "", false},
		{"empty actor id agent thread", anon, agentRef(""), "", "", false},

		{"unknown kind", owner, Ref{Kind: "bogus", ID: "x"}, "", "", true},
		{"worker unknown kind", worker, Ref{Kind: "bogus", ID: "x"}, "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanAccess(tc.actor, tc.ref, tc.assignee, tc.reviewer); got != tc.want {
				t.Fatalf("CanAccess(%+v, %+v, %q, %q) = %v, want %v", tc.actor, tc.ref, tc.assignee, tc.reviewer, got, tc.want)
			}
		})
	}
}
