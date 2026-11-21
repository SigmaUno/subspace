package autopilot

import (
	"reflect"
	"testing"

	"github.com/SigmaUno/subspace/internal/breaker"
	"github.com/SigmaUno/subspace/internal/model"
)

func stateOf(provider string, st breaker.State) func(string, string) breaker.State {
	return func(p, _ string) breaker.State {
		if p == provider {
			return st
		}
		return breaker.Closed
	}
}

func TestPlanRespectsBreaker(t *testing.T) {
	on := map[string]bool{"B-1": true}
	base := func(st breaker.State, load int) Snapshot {
		return Snapshot{
			Enabled: on, Briefs: []*model.Brief{brief("B-1")},
			Tasks: []*model.Task{task("T-1", "B-1", model.StatusReady), task("T-2", "B-1", model.StatusReady)},
			Agents: []*model.Agent{agent("a1", model.RoleWorker, "anthropic", load), agent("a2", model.RoleWorker, "anthropic", 0),
				agent("o1", model.RoleWorker, "openai", 0)},
			Spawn:   map[string]SpawnConfig{"B-1": claude},
			Breaker: stateOf("anthropic", st),
		}
	}
	cases := []struct {
		name string
		s    Snapshot
		want []Action
	}{
		{"closed spreads over all agents", base(breaker.Closed, 0),
			[]Action{assign("B-1", "T-1", "a1"), assign("B-1", "T-2", "a2")}},
		{"open provider gets nothing", base(breaker.Open, 0),
			[]Action{assign("B-1", "T-1", "o1"), assign("B-1", "T-2", "o1")}},
		{"half-open admits one probe assignment", base(breaker.HalfOpen, 0),
			[]Action{assign("B-1", "T-1", "a1"), assign("B-1", "T-2", "o1")}},
		{"half-open with work in flight admits none", base(breaker.HalfOpen, 1),
			[]Action{assign("B-1", "T-1", "o1"), assign("B-1", "T-2", "o1")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Plan(c.s); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v want %+v", got, c.want)
			}
		})
	}
}

func TestPlanDoesNotSpawnOnOpenProvider(t *testing.T) {
	s := Snapshot{
		Enabled: map[string]bool{"B-1": true}, Briefs: []*model.Brief{brief("B-1")},
		Tasks:   []*model.Task{task("T-1", "B-1", model.StatusReady)},
		Spawn:   map[string]SpawnConfig{"B-1": claude},
		Breaker: stateOf("anthropic", breaker.Open),
	}
	if got := Plan(s); len(got) != 0 {
		t.Fatalf("spawned on an open provider: %+v", got)
	}
	s.Breaker = stateOf("anthropic", breaker.HalfOpen)
	s.Tasks = append(s.Tasks, task("T-2", "B-1", model.StatusReady))
	got := Plan(s)
	if len(got) != 1 || got[0].Kind != ActSpawn || got[0].Spawn.N != 1 {
		t.Fatalf("half-open must spawn exactly one probe: %+v", got)
	}
}
