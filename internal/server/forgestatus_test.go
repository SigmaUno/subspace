package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/githost"
	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

type forgeFake struct {
	srv    *httptest.Server
	prefix string
	sha    string
	fail   int
	mu     sync.Mutex
	posts  []githost.CommitStatus
	tries  int
}

func newForgeFake(t *testing.T, prefix, sha string, fail int) *forgeFake {
	t.Helper()
	f := &forgeFake{prefix: prefix, sha: sha, fail: fail}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, f.prefix)
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/o/r/pulls/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 1, "state": "open", "head": map[string]string{"ref": "b", "sha": f.sha}})
		case r.Method == http.MethodPost && path == "/repos/o/r/statuses/"+f.sha:
			f.tries++
			if f.fail != 0 {
				w.WriteHeader(f.fail)
				return
			}
			var st githost.CommitStatus
			_ = json.NewDecoder(r.Body).Decode(&st)
			f.posts = append(f.posts, st)
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *forgeFake) statuses() []githost.CommitStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]githost.CommitStatus(nil), f.posts...)
}

func (f *forgeFake) attempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tries
}

func (f *forgeFake) await(t *testing.T, n int) []githost.CommitStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := f.statuses(); len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("got %d statuses, want %d", len(f.statuses()), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func forgeEnv(t *testing.T, kind string, f *forgeFake) *servertest.Env {
	t.Helper()
	cfg := fmt.Sprintf(`{"hosts":{"h":{"kind":%q,"url":%q,"api_url":%q,"token":"tok"}},"repos":{"o/r":"h"}}`,
		kind, f.srv.URL, f.srv.URL+f.prefix)
	path := filepath.Join(t.TempDir(), "prsync.json")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUBSPACE_PRSYNC_CONFIG", path)
	t.Setenv("SUBSPACE_PRSYNC_INTERVAL", "1h")
	return servertest.Start(t, server.Config{PublicURL: "https://sub.example",
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})
}

type forgeFlow struct {
	e             *servertest.Env
	lead, wk, rev string
	taskID, prURL string
}

func startFlow(t *testing.T, e *servertest.Env, prBase string) *forgeFlow {
	t.Helper()
	fl := &forgeFlow{e: e, prURL: prBase + "/o/r/pulls/1"}
	fl.lead = e.Register("rex", "leader", "other")
	fl.wk = e.Register("claude-1", "worker", "claude-code")
	fl.rev = e.Register("codex-r", "reviewer", "codex")
	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "Demo", "body": "b", "repos": []string{"o/r"}, "max_cost_eur": 10, "max_parallel_agents": 5})
	plan := e.Must(fl.lead, "propose_plan", map[string]any{"brief_id": br["id"], "tasks": []any{
		map[string]any{"title": "Add login", "spec": "build it", "repo": "o/r", "acceptance_criteria": []string{"works"}},
	}})["plan"].(map[string]any)
	if code, out := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	fl.taskID = plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
	e.Must(fl.lead, "assign", map[string]any{"task_id": fl.taskID, "agent_id": "claude-1"})
	e.Must(fl.wk, "claim_task", map[string]any{"task_id": fl.taskID})
	return fl
}

func (fl *forgeFlow) submit() {
	fl.e.Must(fl.wk, "submit", map[string]any{"task_id": fl.taskID, "pr_url": fl.prURL, "summary": "done"})
}

func TestForgeStatusFollowsReviewStateOnBothHosts(t *testing.T) {
	const sha = "0a1b2c3d4e5f60718293a4b5c6d7e8f901234567"
	for _, tc := range []struct{ kind, prefix string }{{"forgejo", "/api/v1"}, {"github", ""}} {
		t.Run(tc.kind, func(t *testing.T) {
			f := newForgeFake(t, tc.prefix, sha, 0)
			e := forgeEnv(t, tc.kind, f)
			fl := startFlow(t, e, f.srv.URL)
			fl.submit()
			target := "https://sub.example/tasks/" + fl.taskID

			got := f.await(t, 1)
			if got[0].State != "pending" || got[0].Context != "subspace/review" || got[0].TargetURL != target {
				t.Fatalf("submitted: %+v", got[0])
			}

			e.Must(fl.rev, "review", map[string]any{"task_id": fl.taskID, "verdict": "changes_requested", "comments": "fix"})
			got = f.await(t, 2)
			if got[1].State != "failure" {
				t.Fatalf("changes_requested: %+v", got[1])
			}

			e.Must(fl.wk, "claim_task", map[string]any{"task_id": fl.taskID})
			fl.submit()
			e.Must(fl.rev, "review", map[string]any{"task_id": fl.taskID, "verdict": "approve", "comments": "lgtm"})
			got = f.await(t, 4)
			if got[2].State != "pending" || got[3].State != "success" {
				t.Fatalf("resubmit and approve: %+v", got)
			}

			if code, out := e.REST("POST", "/api/v1/tasks/"+fl.taskID+"/done", map[string]any{"note": "merged"}); code != 200 {
				t.Fatalf("done: %d %v", code, out)
			}
			time.Sleep(100 * time.Millisecond)
			if n := len(f.statuses()); n != 4 {
				t.Fatalf("done on an already successful head must not post again: %d statuses", n)
			}
		})
	}
}

func TestForgeStatusFailuresNeverBlockTheBoard(t *testing.T) {
	const sha = "0a1b2c3d4e5f60718293a4b5c6d7e8f901234567"
	f := newForgeFake(t, "", sha, http.StatusInternalServerError)
	e := forgeEnv(t, "github", f)
	fl := startFlow(t, e, f.srv.URL)

	started := time.Now()
	fl.submit()
	if r := e.Must(fl.rev, "review", map[string]any{"task_id": fl.taskID, "verdict": "approve", "comments": "ok"}); r["task"].(map[string]any)["status"] != "approved" {
		t.Fatalf("review: %v", r)
	}
	if d := time.Since(started); d > 2*time.Second {
		t.Fatalf("board calls took %s while the host was failing", d)
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.attempts() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no status attempt reached the host")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if code, out := e.REST("POST", "/api/v1/tasks/"+fl.taskID+"/done", map[string]any{"note": "merged"}); code != 200 {
		t.Fatalf("done: %d %v", code, out)
	}
}
