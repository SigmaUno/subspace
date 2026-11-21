package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

type okGit struct{}

func (okGit) Run(_ context.Context, _, name string, args ...string) (string, error) {
	if name == "git" && args[0] == "rev-parse" {
		return "1111111111111111111111111111111111111111\n", nil
	}
	return "", nil
}

func TestRepositoryRegistryChangesApplyWithoutRestart(t *testing.T) {
	var polls atomic.Int64
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/api/v1/repos/o/r")
		switch {
		case r.Method == http.MethodGet && p == "/pulls/2":
			polls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 2, "state": "open", "head": map[string]string{"ref": "b", "sha": "0a1b2c3d4e5f60718293a4b5c6d7e8f901234567"}})
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/status"):
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "success", "statuses": []any{}})
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte("[]"))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	t.Cleanup(forge.Close)
	t.Setenv("SUBSPACE_MQ_CONFIG", "")
	t.Setenv("SUBSPACE_PRSYNC_CONFIG", "")
	t.Setenv("SUBSPACE_MQ_INTERVAL", "10ms")
	t.Setenv("SUBSPACE_PRSYNC_INTERVAL", "20ms")
	t.Cleanup(server.SetMergeQueueRunner(okGit{}))
	e := servertest.Start(t, server.Config{PublicURL: "https://sub.example",
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})

	if code, out := e.REST("POST", "/api/v1/repos", map[string]any{"name": "o/r", "host": "forgejo", "base_url": forge.URL,
		"merge_test_command": []string{"make", "test"}}); code != 201 {
		t.Fatalf("create repo: %d %v", code, out)
	}
	lead := e.Register("rex", "leader", "other")
	wk := e.Register("claude-1", "worker", "claude-code")
	rev := e.Register("codex-r", "reviewer", "codex")
	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "MQ", "repos": []string{"o/r"}, "max_parallel_agents": 5})
	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": br["id"], "tasks": []any{
		map[string]any{"title": "First", "spec": "s", "repo": "o/r"},
		map[string]any{"title": "Second", "spec": "s", "repo": "o/r"},
	}})["plan"].(map[string]any)
	if code, out := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	var ids []string
	for i, x := range plan["tasks"].([]any) {
		id := x.(map[string]any)["id"].(string)
		ids = append(ids, id)
		e.Must(lead, "assign", map[string]any{"task_id": id, "agent_id": "claude-1"})
		e.Must(wk, "claim_task", map[string]any{"task_id": id})
		e.Must(wk, "submit", map[string]any{"task_id": id, "pr_url": forge.URL + "/o/r/pulls/" + string(rune('1'+i))})
		e.Must(rev, "review", map[string]any{"task_id": id, "verdict": "approve", "comments": "lgtm"})
	}

	if code, out := e.REST("POST", "/api/v1/mergequeue/candidates", map[string]any{"task_id": ids[0]}); code != 201 {
		t.Fatalf("enqueue after registering the repo: %d %v", code, out)
	}
	eventually(t, "the first task is merged", func() bool {
		_, tk := e.REST("GET", "/api/v1/tasks/"+ids[0], nil)
		return tk["status"] == "done"
	})
	eventually(t, "the poller fetches the pull request of the new repo", func() bool { return polls.Load() > 0 })

	if code, out := e.REST("DELETE", "/api/v1/repos/o/r", nil); code != 204 {
		t.Fatalf("delete repo: %d %v", code, out)
	}
	code, out := e.REST("POST", "/api/v1/mergequeue/candidates", map[string]any{"task_id": ids[1]})
	if msg, _ := out["error"].(map[string]any); code != 409 || !strings.Contains(msg["message"].(string), "merge queue disabled for repo o/r") {
		t.Fatalf("enqueue after deleting the repo: %d %v", code, out)
	}
	time.Sleep(200 * time.Millisecond)
	settled := polls.Load()
	time.Sleep(300 * time.Millisecond)
	if got := polls.Load(); got != settled {
		t.Fatalf("the poller kept fetching a deleted repo: %d -> %d", settled, got)
	}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
