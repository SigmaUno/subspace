package autopilot

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/SigmaUno/subspace/internal/conflict"
	"github.com/SigmaUno/subspace/internal/model"
)

type simResult struct {
	ticks, maxParallel, overlaps, done int
}

func simulate(t *testing.T, seed uint64, mode conflict.Mode) simResult {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	areas := []string{"internal/api", "internal/store", "internal/board", "web/app", "cmd/subspace", "docs"}
	var tasks []*model.Task
	for i := 1; i <= 40; i++ {
		var paths model.Paths
		for _, a := range rng.Perm(len(areas))[:1+rng.IntN(2)] {
			p := areas[a]
			if rng.IntN(3) == 0 {
				p += fmt.Sprintf("/f%d.go", rng.IntN(3))
			}
			paths = append(paths, p)
		}
		tasks = append(tasks, &model.Task{ID: fmt.Sprintf("T-%d", i), BriefID: "B-1", Repo: "o/r", Role: model.RoleWorker,
			Status: model.StatusReady, Priority: rng.IntN(3), Paths: paths})
	}
	agents := []*model.Agent{agent("w1", model.RoleWorker, "anthropic", 0), agent("w2", model.RoleWorker, "openai", 0),
		agent("w3", model.RoleWorker, "deepseek", 0), agent("w4", model.RoleWorker, "anthropic", 0)}
	remaining := map[string]int{}
	var res simResult
	for res.ticks = 1; res.ticks <= 500; res.ticks++ {
		var live []*model.Task
		load := map[string]int{}
		for _, tk := range tasks {
			if tk.Status == model.StatusDone {
				continue
			}
			live = append(live, tk)
			if tk.Assignee != nil && (tk.Status == model.StatusAssigned || tk.Status == model.StatusInProgress) {
				load[*tk.Assignee]++
			}
		}
		if len(live) == 0 {
			break
		}
		for _, a := range agents {
			a.AssignedCount = load[a.ID]
		}
		snap := Snapshot{Enabled: map[string]bool{"B-1": true}, Briefs: []*model.Brief{brief("B-1")}, Tasks: live,
			Agents: agents, Conflict: mode}
		byID := map[string]*model.Task{}
		for _, tk := range live {
			byID[tk.ID] = tk
		}
		for _, act := range Plan(snap) {
			if act.Kind != ActAssign {
				t.Fatalf("unexpected action %+v", act)
			}
			tk := byID[act.TaskID]
			id := act.AgentID
			tk.Assignee, tk.Status = &id, model.StatusAssigned
		}
		parallel := 0
		for _, a := range agents {
			var cur *model.Task
			for _, tk := range live {
				if tk.Assignee == nil || *tk.Assignee != a.ID {
					continue
				}
				if tk.Status == model.StatusInProgress || tk.Status == model.StatusSubmitted {
					cur = tk
					break
				}
				if tk.Status == model.StatusAssigned && cur == nil {
					cur = tk
				}
			}
			if cur == nil {
				continue
			}
			switch cur.Status {
			case model.StatusAssigned:
				cur.Status, remaining[cur.ID] = model.StatusInProgress, 1+rng.IntN(4)
			case model.StatusInProgress:
				if remaining[cur.ID]--; remaining[cur.ID] <= 0 {
					cur.Status = model.StatusSubmitted
				}
			case model.StatusSubmitted:
				cur.Status = model.StatusDone
				res.done++
			}
			if cur.Status == model.StatusInProgress {
				parallel++
			}
		}
		res.maxParallel = max(res.maxParallel, parallel)
		for _, tk := range live {
			if tk.Assignee != nil && tk.Status != model.StatusDone {
				res.overlaps += len(conflict.Conflicts(tk, *tk.Assignee, live))
			}
		}
	}
	return res
}

func TestAutopilotAvoidsOverlapWhileStayingParallel(t *testing.T) {
	for seed := uint64(1); seed <= 20; seed++ {
		r := simulate(t, seed, conflict.Warn)
		if r.done != 40 {
			t.Fatalf("seed %d: %d/40 tasks done after %d ticks", seed, r.done, r.ticks)
		}
		if r.overlaps != 0 {
			t.Fatalf("seed %d: %d overlapping holds across agents", seed, r.overlaps)
		}
		if r.maxParallel < 3 {
			t.Fatalf("seed %d: max parallelism %d, want at least 3", seed, r.maxParallel)
		}
		if off := simulate(t, seed, conflict.Off); r.ticks > 2*off.ticks {
			t.Fatalf("seed %d: %d ticks avoiding overlaps vs %d without", seed, r.ticks, off.ticks)
		}
		if again := simulate(t, seed, conflict.Warn); again != r {
			t.Fatalf("seed %d: not deterministic: %+v vs %+v", seed, r, again)
		}
	}
	if off := simulate(t, 1, conflict.Off); off.overlaps == 0 {
		t.Fatalf("mode off should produce overlapping holds, got %+v", off)
	}
}

func TestPlanPathOverlap(t *testing.T) {
	on := map[string]bool{"B-1": true}
	pathTask := func(id, status string, paths ...string) *model.Task {
		tk := task(id, "B-1", status)
		tk.Repo, tk.Paths = "o/r", paths
		return tk
	}
	workers := func(loads ...int) []*model.Agent {
		var out []*model.Agent
		for i, l := range loads {
			out = append(out, agent(fmt.Sprintf("w%d", i+1), model.RoleWorker, "anthropic", l))
		}
		return out
	}
	cases := []struct {
		name string
		mode conflict.Mode
		s    Snapshot
		want []Action
	}{
		{
			name: "overlapping task waits while its owner is busy and spawns nothing",
			s: Snapshot{Tasks: []*model.Task{withAssignee(pathTask("T-1", model.StatusInProgress, "internal/api"), "w1"),
				pathTask("T-2", model.StatusReady, "internal/api/x.go"), pathTask("T-3", model.StatusReady, "web")},
				Agents: workers(1, 0), Spawn: map[string]SpawnConfig{"B-1": claude}},
			want: []Action{assign("B-1", "T-3", "w2")},
		},
		{
			name: "overlapping task goes to its idle owner",
			s: Snapshot{Tasks: []*model.Task{withAssignee(pathTask("T-1", model.StatusSubmitted, "internal/**"), "w2"),
				pathTask("T-2", model.StatusReady, "internal/api")},
				Agents: workers(0, 0)},
			want: []Action{assign("B-1", "T-2", "w2")},
		},
		{
			name: "task overlapping two agents waits",
			s: Snapshot{Tasks: []*model.Task{withAssignee(pathTask("T-1", model.StatusSubmitted, "a"), "w1"),
				withAssignee(pathTask("T-2", model.StatusSubmitted, "b"), "w2"),
				pathTask("T-3", model.StatusReady, "a", "b")},
				Agents: workers(0, 0, 0)},
		},
		{
			name: "ready tasks overlapping each other are split across ticks",
			s: Snapshot{Tasks: []*model.Task{pathTask("T-1", model.StatusReady, "a"), pathTask("T-2", model.StatusReady, "a/b"),
				pathTask("T-3", model.StatusReady, "c")},
				Agents: workers(0, 0)},
			want: []Action{assign("B-1", "T-1", "w1"), assign("B-1", "T-3", "w2")},
		},
		{
			name: "mode off ignores paths",
			mode: conflict.Off,
			s: Snapshot{Tasks: []*model.Task{withAssignee(pathTask("T-1", model.StatusInProgress, "a"), "w1"),
				pathTask("T-2", model.StatusReady, "a")},
				Agents: workers(1, 0)},
			want: []Action{assign("B-1", "T-2", "w2")},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.s.Enabled, c.s.Briefs, c.s.Conflict = on, []*model.Brief{brief("B-1")}, c.mode
			if c.mode == "" {
				c.s.Conflict = conflict.Warn
			}
			if got := Plan(c.s); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v\nwant %+v", got, c.want)
			}
		})
	}
}
