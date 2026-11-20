package autopilot

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/model"
)

func brief(id string, providers ...string) *model.Brief {
	return &model.Brief{ID: id, Status: "open", AllowedProviders: providers}
}

func agent(id, role, provider string, load int) *model.Agent {
	return &model.Agent{ID: id, Role: role, Provider: provider, Status: model.AgentActive, AssignedCount: load}
}

func task(id, briefID, status string) *model.Task {
	return &model.Task{ID: id, BriefID: briefID, Role: model.RoleWorker, Status: status}
}

func withAssignee(t *model.Task, a string) *model.Task { t.Assignee = &a; return t }
func withReviewer(t *model.Task, r string) *model.Task { t.Reviewer = &r; return t }
func withRole(t *model.Task, role string) *model.Task  { t.Role = role; return t }

func assign(b, t, a string) Action { return Action{Kind: ActAssign, BriefID: b, TaskID: t, AgentID: a} }
func review(b, t, a string) Action { return Action{Kind: ActReview, BriefID: b, TaskID: t, AgentID: a} }
func spawn(b, role, kind, mdl, prov string, n int) Action {
	return Action{Kind: ActSpawn, BriefID: b, Spawn: &board.SpawnInput{BriefID: b, Kind: kind, Model: mdl, N: n, Role: role, Provider: prov}}
}

var claude = SpawnConfig{Kind: model.KindClaudeCode, Model: "sonnet"}

func TestPlan(t *testing.T) {
	on := map[string]bool{"B-1": true}
	cases := []struct {
		name string
		s    Snapshot
		want []Action
	}{
		{
			name: "paused does nothing",
			s: Snapshot{Paused: true, Enabled: on, Briefs: []*model.Brief{brief("B-1")},
				Tasks:  []*model.Task{task("T-1", "B-1", model.StatusReady)},
				Agents: []*model.Agent{agent("w1", model.RoleWorker, "anthropic", 0)}},
		},
		{
			name: "disabled brief does nothing",
			s: Snapshot{Enabled: map[string]bool{}, Briefs: []*model.Brief{brief("B-1")},
				Tasks:  []*model.Task{task("T-1", "B-1", model.StatusReady)},
				Agents: []*model.Agent{agent("w1", model.RoleWorker, "anthropic", 0)}},
		},
		{
			name: "brief over budget does nothing",
			s: Snapshot{Enabled: on, Briefs: []*model.Brief{{ID: "B-1", Status: "open", CostUsed: 5, Budget: model.BriefBudget{MaxCostEUR: 5}}},
				Tasks:  []*model.Task{task("T-1", "B-1", model.StatusReady)},
				Agents: []*model.Agent{agent("w1", model.RoleWorker, "anthropic", 0)},
				Spawn:  map[string]SpawnConfig{"B-1": claude}},
		},
		{
			name: "ties broken by id then load spreads",
			s: Snapshot{Enabled: on, Briefs: []*model.Brief{brief("B-1")},
				Tasks: []*model.Task{task("T-10", "B-1", model.StatusReady), task("T-2", "B-1", model.StatusReady),
					task("T-3", "B-1", model.StatusReady), task("T-1", "B-1", model.StatusReady)},
				Agents: []*model.Agent{agent("w2", model.RoleWorker, "anthropic", 0), agent("w1", model.RoleWorker, "openai", 0),
					agent("w3", model.RoleWorker, "openai", 1)}},
			want: []Action{assign("B-1", "T-1", "w1"), assign("B-1", "T-2", "w2"), assign("B-1", "T-3", "w1"), assign("B-1", "T-10", "w2")},
		},
		{
			name: "least loaded wins, inactive and wrong role skipped",
			s: Snapshot{Enabled: on, Briefs: []*model.Brief{brief("B-1")},
				Tasks: []*model.Task{task("T-1", "B-1", model.StatusReady), withRole(task("T-2", "B-1", model.StatusReady), model.RoleTester)},
				Agents: []*model.Agent{agent("a", model.RoleWorker, "anthropic", 3), agent("b", model.RoleWorker, "anthropic", 1),
					{ID: "c", Role: model.RoleWorker, Provider: "anthropic", Status: model.AgentPaused},
					agent("d", model.RoleReviewer, "openai", 0), agent("t", model.RoleTester, "deepseek", 2)}},
			want: []Action{assign("B-1", "T-1", "b"), assign("B-1", "T-2", "t")},
		},
		{
			name: "only allowed providers are assigned",
			s: Snapshot{Enabled: on, Briefs: []*model.Brief{brief("B-1", "openai")},
				Tasks:  []*model.Task{task("T-1", "B-1", model.StatusReady)},
				Agents: []*model.Agent{agent("claude-1", model.RoleWorker, "anthropic", 0), agent("codex-1", model.RoleWorker, "openai", 4)}},
			want: []Action{assign("B-1", "T-1", "codex-1")},
		},
		{
			name: "no allowed agent and spawn kind not allowed",
			s: Snapshot{Enabled: on, Briefs: []*model.Brief{brief("B-1", "openai")},
				Tasks:  []*model.Task{task("T-1", "B-1", model.StatusReady)},
				Agents: []*model.Agent{agent("claude-1", model.RoleWorker, "anthropic", 0)},
				Spawn:  map[string]SpawnConfig{"B-1": claude}},
		},
		{
			name: "spawn missing workers per role",
			s: Snapshot{Enabled: on, Briefs: []*model.Brief{brief("B-1")},
				Tasks: []*model.Task{task("T-1", "B-1", model.StatusReady), task("T-2", "B-1", model.StatusReady),
					withRole(task("T-3", "B-1", model.StatusReady), model.RoleTester)},
				Spawn: map[string]SpawnConfig{"B-1": claude}},
			want: []Action{spawn("B-1", model.RoleTester, model.KindClaudeCode, "sonnet", "anthropic", 1),
				spawn("B-1", model.RoleWorker, model.KindClaudeCode, "sonnet", "anthropic", 2)},
		},
		{
			name: "spawn bounded by global and provider capacity",
			s: Snapshot{Enabled: map[string]bool{"B-1": true, "B-2": true}, Briefs: []*model.Brief{brief("B-1"), brief("B-2")},
				Tasks: []*model.Task{task("T-1", "B-1", model.StatusReady), task("T-2", "B-1", model.StatusReady),
					task("T-3", "B-1", model.StatusReady), task("T-4", "B-2", model.StatusReady), task("T-5", "B-2", model.StatusReady)},
				Capacity: board.CapacityView{GlobalMaxAgents: 10, AgentsInUse: 6, Limits: []capacity.Report{
					{Limit: capacity.Limit{Provider: "anthropic", Model: "*", MaxConcurrency: 4}, InUse: 2}}},
				Spawn: map[string]SpawnConfig{"B-1": claude, "B-2": {Kind: model.KindCodex, Model: "gpt"}}},
			want: []Action{spawn("B-1", model.RoleWorker, model.KindClaudeCode, "sonnet", "anthropic", 2),
				spawn("B-2", model.RoleWorker, model.KindCodex, "gpt", "openai", 2)},
		},
		{
			name: "capacity exhausted spawns nothing",
			s: Snapshot{Enabled: on, Briefs: []*model.Brief{brief("B-1")},
				Tasks:    []*model.Task{task("T-1", "B-1", model.StatusReady)},
				Capacity: board.CapacityView{GlobalMaxAgents: 5, AgentsInUse: 5},
				Spawn:    map[string]SpawnConfig{"B-1": claude}},
		},
		{
			name: "spawn bounded by max_parallel_agents",
			s: Snapshot{Enabled: on, Briefs: []*model.Brief{{ID: "B-1", Status: "open", Budget: model.BriefBudget{MaxParallelAgent: 3}}},
				Tasks: []*model.Task{withAssignee(task("T-1", "B-1", model.StatusInProgress), "x"),
					withAssignee(task("T-2", "B-1", model.StatusAssigned), "y"),
					withAssignee(task("T-3", "B-1", model.StatusAssigned), "y"),
					task("T-4", "B-1", model.StatusReady), task("T-5", "B-1", model.StatusReady)},
				Agents: []*model.Agent{{ID: "x", Role: model.RoleWorker, Provider: "anthropic", Status: model.AgentRevoked},
					{ID: "y", Role: model.RoleWorker, Provider: "anthropic", Status: model.AgentPaused}},
				Spawn: map[string]SpawnConfig{"B-1": claude}},
			want: []Action{spawn("B-1", model.RoleWorker, model.KindClaudeCode, "sonnet", "anthropic", 1)},
		},
		{
			name: "reviewer must use another provider",
			s: Snapshot{Enabled: on, Briefs: []*model.Brief{brief("B-1")},
				Tasks: []*model.Task{withAssignee(task("T-1", "B-1", model.StatusSubmitted), "claude-1"),
					withAssignee(task("T-2", "B-1", model.StatusSubmitted), "codex-1"),
					withReviewer(withAssignee(task("T-3", "B-1", model.StatusInReview), "claude-1"), "rev-openai")},
				Agents: []*model.Agent{agent("claude-1", model.RoleWorker, "anthropic", 0), agent("codex-1", model.RoleWorker, "openai", 0),
					agent("rev-anthropic", model.RoleReviewer, "anthropic", 0), agent("rev-openai", model.RoleReviewer, "openai", 0)}},
			want: []Action{review("B-1", "T-1", "rev-openai"), review("B-1", "T-2", "rev-anthropic")},
		},
		{
			name: "reviewer load breaks ties and same provider only is skipped",
			s: Snapshot{Enabled: on, Briefs: []*model.Brief{brief("B-1", "anthropic", "openai")},
				Tasks: []*model.Task{withAssignee(task("T-1", "B-1", model.StatusSubmitted), "deep-1"),
					withAssignee(task("T-2", "B-1", model.StatusSubmitted), "deep-1"),
					withAssignee(task("T-3", "B-1", model.StatusSubmitted), "claude-1"),
					withReviewer(withAssignee(task("T-4", "B-1", model.StatusInReview), "deep-1"), "r1")},
				Agents: []*model.Agent{agent("deep-1", model.RoleWorker, "deepseek", 0), agent("claude-1", model.RoleWorker, "anthropic", 0),
					agent("r1", model.RoleReviewer, "anthropic", 0), agent("r2", model.RoleReviewer, "openai", 0),
					agent("r3", model.RoleReviewer, "deepseek", 0)}},
			want: []Action{review("B-1", "T-1", "r2"), review("B-1", "T-2", "r1"), review("B-1", "T-3", "r2")},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Plan(c.s)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got  %+v\nwant %+v", fmtActs(got), fmtActs(c.want))
			}
			if again := Plan(c.s); !reflect.DeepEqual(again, got) {
				t.Fatalf("plan not deterministic")
			}
		})
	}
}

func fmtActs(as []Action) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		s := a.Kind + " " + a.BriefID + " " + a.TaskID + " " + a.AgentID
		if a.Spawn != nil {
			s += fmt.Sprintf(" %+v", *a.Spawn)
		}
		out = append(out, s)
	}
	return out
}
