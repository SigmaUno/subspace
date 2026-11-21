package server

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/repos"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

type recordingGit struct {
	*fakeGit
	mu   sync.Mutex
	dirs map[string]bool
	cmds []string
}

func (r *recordingGit) Run(ctx context.Context, dir, name string, args ...string) (string, error) {
	r.mu.Lock()
	r.dirs[dir] = true
	r.cmds = append(r.cmds, name+" "+strings.Join(args, " "))
	r.mu.Unlock()
	return r.fakeGit.Run(ctx, dir, name, args...)
}

func TestMergeQueueReadsTheRepositoryRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	work := filepath.Join(t.TempDir(), "work")
	st, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []repos.Repo{
		{Name: "o/r", Host: repos.HostForgejo, BaseURL: "https://forge.example.com", DefaultBranch: "trunk",
			MergeTestCommand: []string{"make", "test"}, MergeWorkDir: work, Enabled: true},
		{Name: "o/untested", Host: repos.HostForgejo, BaseURL: "https://forge.example.com", Enabled: true},
	} {
		if _, err := (&repos.Store{St: st}).Create(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUBSPACE_MQ_CONFIG", "")
	t.Setenv("SUBSPACE_MQ_INTERVAL", "10ms")
	rg := &recordingGit{fakeGit: &fakeGit{fail: map[string]bool{}}, dirs: map[string]bool{}}
	mqRunner = rg
	t.Cleanup(func() { mqRunner = nil })

	e := newFlowEnv(t, func(c *Config) { c.DBPath = path })
	lead := e.register("rex", "leader", "other")
	wk := e.register("claude-1", "worker", "claude-code")
	rev := e.register("codex-r", "reviewer", "codex")
	_, br := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "MQ", "repos": []string{"o/r"}, "max_parallel_agents": 5})
	plan := e.must(lead, "propose_plan", map[string]any{"brief_id": br["id"], "tasks": []any{map[string]any{"title": "Good", "spec": "s", "repo": "o/r"}}})["plan"].(map[string]any)
	e.restWant(200, "POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{})
	id := plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
	e.must(lead, "assign", map[string]any{"task_id": id, "agent_id": "claude-1"})
	e.must(wk, "claim_task", map[string]any{"task_id": id})
	e.must(wk, "submit", map[string]any{"task_id": id, "pr_url": "https://forge.example.com/o/r/pulls/1"})
	e.must(rev, "review", map[string]any{"task_id": id, "verdict": "approve", "comments": "lgtm"})

	if c := e.restWant(201, "POST", "/api/v1/mergequeue/candidates", map[string]any{"task_id": id}); c["base"] != "trunk" {
		t.Fatalf("the base branch is the registry default branch: %v", c)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, tk := e.rest("GET", "/api/v1/tasks/"+id, nil); tk["status"] == "done" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the task was not merged")
		}
		time.Sleep(10 * time.Millisecond)
	}

	rg.mu.Lock()
	defer rg.mu.Unlock()
	for dir := range rg.dirs {
		if !strings.HasPrefix(dir, work+string(filepath.Separator)) {
			t.Fatalf("git ran in %s, outside the repository work dir %s", dir, work)
		}
	}
	all := strings.Join(rg.cmds, "\n")
	if !strings.Contains(all, "https://forge.example.com/o/r.git") || !strings.Contains(all, "make test") {
		t.Fatalf("the clone URL and merge test command come from the registry: %q", rg.cmds)
	}
}
