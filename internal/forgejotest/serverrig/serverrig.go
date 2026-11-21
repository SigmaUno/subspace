package serverrig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/forgejotest"
	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

type Rig struct {
	T        *testing.T
	Inst     *forgejotest.Instance
	Repo     *forgejotest.Repo
	E        *servertest.Env
	Lead     string
	Worker   string
	Reviewer string
	Brief    string

	n int
}

func New(t *testing.T, inst *forgejotest.Instance, repo *forgejotest.Repo, env map[string]string) *Rig {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"interval": "1h",
		"hosts": map[string]any{"forge": map[string]any{
			"kind": "forgejo", "url": inst.URL, "token": inst.Token,
			"rate": map[string]any{"per_minute": 60000, "burst": 10000},
		}},
		"repos": map[string]string{repo.FullName(): "forge"},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "prsync.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUBSPACE_PRSYNC_CONFIG", path)
	for k, v := range env {
		t.Setenv(k, v)
	}
	e := servertest.Start(t, server.Config{PublicURL: "https://sub.test",
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})
	r := &Rig{T: t, Inst: inst, Repo: repo, E: e}
	r.Lead = e.Register("rex", "leader", "other")
	r.Worker = e.Register("w1", "worker", "claude-code")
	r.Reviewer = e.Register("r1", "reviewer", "codex")
	code, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B", "repos": []string{repo.FullName()}, "max_parallel_agents": 50})
	if code != 201 {
		t.Fatalf("create brief: %d %v", code, br)
	}
	r.Brief = br["id"].(string)
	return r
}

func (r *Rig) Task(title string, criteria ...string) string {
	r.T.Helper()
	r.n++
	plan := r.E.Must(r.Lead, "propose_plan", map[string]any{"brief_id": r.Brief, "tasks": []any{
		map[string]any{"title": title, "spec": fmt.Sprintf("spec %d", r.n), "repo": r.Repo.FullName(), "acceptance_criteria": criteria},
	}})["plan"].(map[string]any)
	if code, out := r.E.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		r.T.Fatalf("approve plan: %d %v", code, out)
	}
	return plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
}

func (r *Rig) Submit(id string, pr forgejotest.PR) {
	r.T.Helper()
	r.E.Must(r.Lead, "assign", map[string]any{"task_id": id, "agent_id": "w1"})
	r.Rework(id, pr)
}

func (r *Rig) Rework(id string, pr forgejotest.PR) {
	r.T.Helper()
	r.E.Must(r.Worker, "claim_task", map[string]any{"task_id": id})
	r.E.Must(r.Worker, "submit", map[string]any{"task_id": id, "pr_url": r.Repo.PRURL(pr.Number), "summary": "done"})
}

func (r *Rig) Review(id, verdict, comments string) {
	r.T.Helper()
	r.E.Must(r.Reviewer, "review", map[string]any{"task_id": id, "verdict": verdict, "comments": comments})
}

func (r *Rig) Messages(id string) []map[string]any {
	r.T.Helper()
	_, out := r.E.REST("GET", "/api/v1/threads/task/"+id+"/messages", nil)
	var msgs []map[string]any
	for _, x := range out["messages"].([]any) {
		msgs = append(msgs, x.(map[string]any))
	}
	return msgs
}

func (r *Rig) Get(id string) map[string]any {
	r.T.Helper()
	_, out := r.E.REST("GET", "/api/v1/tasks/"+id, nil)
	return out
}
