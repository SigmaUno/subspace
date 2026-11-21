package main

import (
	"context"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

type taskState struct {
	Title, Status, Assignee string
	Rounds                  int
}

type agentState struct{ Role, Provider, Status string }

type snapshot struct {
	Briefs    []string
	Agents    map[string]agentState
	Tasks     map[string]taskState
	Plans     map[string]int
	Questions map[string]int
	Messages  map[string]int
	Artifacts int
}

func newEnv(t *testing.T) *servertest.Env {
	t.Helper()
	return servertest.Start(t, server.Config{PublicURL: "http://sub.test", FlushEvery: 50 * time.Millisecond, Lease: time.Hour})
}

func seedEnv(t *testing.T, env *servertest.Env, seed int64) *Seeder {
	t.Helper()
	s := NewSeeder(env.TS.URL, servertest.OwnerToken, seed, io.Discard)
	if err := s.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func take(t *testing.T, env *servertest.Env) snapshot {
	t.Helper()
	ctx := context.Background()
	o := client.NewOwner(env.TS.URL, servertest.OwnerToken)
	snap := snapshot{Agents: map[string]agentState{}, Tasks: map[string]taskState{}, Plans: map[string]int{}, Questions: map[string]int{}, Messages: map[string]int{}}
	briefs, err := o.ListBriefs(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	threads := map[string][]string{}
	for _, b := range briefs {
		snap.Briefs = append(snap.Briefs, b.Title)
		threads[model.ThreadBrief] = append(threads[model.ThreadBrief], b.ID)
	}
	agents, err := o.ListAgents(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range agents {
		snap.Agents[a.ID] = agentState{a.Role, a.Provider, a.Status}
		threads[model.ThreadAgent] = append(threads[model.ThreadAgent], a.ID)
	}
	tasks, err := o.ListTasks(ctx, board.TaskQuery{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range tasks {
		snap.Tasks[tk.ID] = taskState{tk.Title, tk.Status, deref(tk.Assignee), tk.ReviewRounds}
		threads[model.ThreadTask] = append(threads[model.ThreadTask], tk.ID)
	}
	plans, err := o.ListPlans(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plans {
		snap.Plans[p.Status]++
	}
	qs, err := o.ListQuestions(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range qs {
		snap.Questions[q.Status]++
	}
	for kind, ids := range threads {
		for _, id := range ids {
			msgs, err := o.ThreadMessages(ctx, kind, id, 0, 1000)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range msgs {
				snap.Messages[kind+"/"+m.Kind]++
			}
		}
	}
	code, out := env.REST("GET", "/api/v1/artifacts?limit=1000", nil)
	if code != 200 {
		t.Fatalf("artifacts: %d %v", code, out)
	}
	snap.Artifacts = len(out["artifacts"].([]any))
	return snap
}

func statusCounts(s snapshot) map[string]int {
	out := map[string]int{}
	for _, tk := range s.Tasks {
		out[tk.Status]++
	}
	return out
}

func TestSeedIsDeterministicAndCoversEveryStatus(t *testing.T) {
	a, b := newEnv(t), newEnv(t)
	seedEnv(t, a, 7)
	seedEnv(t, b, 7)
	sa, sb := take(t, a), take(t, b)
	if !reflect.DeepEqual(sa, sb) {
		t.Fatalf("same seed produced different state:\n%+v\n%+v", sa, sb)
	}
	counts := statusCounts(sa)
	for _, st := range model.AllStatuses {
		if counts[st] == 0 {
			t.Errorf("no task in status %s (counts %v)", st, counts)
		}
	}
	if len(sa.Briefs) != 3 || len(sa.Agents) != 13 || sa.Artifacts == 0 || sa.Plans[model.PlanProposed] != 1 {
		t.Errorf("unexpected shape: briefs %d agents %d artifacts %d plans %v", len(sa.Briefs), len(sa.Agents), sa.Artifacts, sa.Plans)
	}
	if sa.Questions["open"] == 0 || sa.Questions["answered"] == 0 {
		t.Errorf("questions %v", sa.Questions)
	}
}

func TestSeedRefusesPopulatedServer(t *testing.T) {
	env := newEnv(t)
	seedEnv(t, env, 1)
	before := take(t, env)
	s := NewSeeder(env.TS.URL, servertest.OwnerToken, 1, io.Discard)
	if err := s.Run(context.Background()); err == nil {
		t.Fatal("second seed on the same server succeeded")
	}
	if after := take(t, env); !reflect.DeepEqual(before, after) {
		t.Fatal("refused seed changed the server")
	}
}

func TestSimulatorKeepsTasksMoving(t *testing.T) {
	env := newEnv(t)
	s := seedEnv(t, env, 3)
	before := statusCounts(take(t, env))
	sim := NewSim(s, 3)
	for i := 0; i < 80; i++ {
		if err := sim.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	after := take(t, env)
	if reflect.DeepEqual(before, statusCounts(after)) {
		t.Fatal("no task changed status during the simulation")
	}
	for id, a := range after.Agents {
		if a.Status != model.AgentActive {
			t.Errorf("agent %s is %s", id, a.Status)
		}
	}
}
