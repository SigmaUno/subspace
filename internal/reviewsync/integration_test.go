package reviewsync_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

const (
	ghToken = "gh-review-" + "token-0123456789"
	fjToken = "fj-review-" + "token-0123456789"
)

type fakeForge struct {
	*httptest.Server
	prefix, auth string

	mu       sync.Mutex
	next     int64
	comments map[int64]string
	targets  map[int64]string
	posts    int
	patches  int
	badAuth  int
}

func newForge(t *testing.T, prefix, auth string) *fakeForge {
	f := &fakeForge{prefix: prefix, auth: auth, next: 700, comments: map[int64]string{}, targets: map[int64]string{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

var (
	createRe = regexp.MustCompile(`^/repos/([^/]+/[^/]+)/issues/(\d+)/comments$`)
	updateRe = regexp.MustCompile(`^/repos/([^/]+/[^/]+)/issues/comments/(\d+)$`)
)

func (f *fakeForge) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != f.auth {
		f.badAuth++
	}
	path := strings.TrimPrefix(r.URL.Path, f.prefix)
	var in struct {
		Body string `json:"body"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && createRe.MatchString(path):
		m := createRe.FindStringSubmatch(path)
		f.next++
		f.posts++
		f.comments[f.next] = in.Body
		f.targets[f.next] = m[1] + "#" + m[2]
		_, _ = fmt.Fprintf(w, `{"id": %d}`, f.next)
	case r.Method == http.MethodPatch && updateRe.MatchString(path):
		id, _ := strconv.ParseInt(updateRe.FindStringSubmatch(path)[2], 10, 64)
		if _, ok := f.comments[id]; !ok {
			http.NotFound(w, r)
			return
		}
		f.patches++
		f.comments[id] = in.Body
		_, _ = w.Write([]byte(`{}`))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeForge) wait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		f.mu.Lock()
		ok := cond()
		f.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (f *fakeForge) only(t *testing.T) (int64, string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.comments) != 1 {
		t.Fatalf("comments = %d, want 1: %v", len(f.comments), f.comments)
	}
	for id, b := range f.comments {
		return id, b
	}
	return 0, ""
}

type rig struct {
	t            *testing.T
	e            *servertest.Env
	db           string
	gh, fj       *fakeForge
	lead, wk, rv string
	bid          string
}

func writeConfig(t *testing.T, gh, fj *fakeForge) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prsync.json")
	raw := fmt.Sprintf(`{
  "interval": "1h",
  "hosts": {
    "gh": {"kind": "github", "api_url": %q, "token": %q},
    "fj": {"kind": "forgejo", "url": %q, "token": %q}
  },
  "repos": {"o/gh": "gh"}
}`, gh.URL, ghToken, fj.URL, fjToken)
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUBSPACE_PRSYNC_CONFIG", path)
}

func start(t *testing.T, db string) *servertest.Env {
	t.Helper()
	return servertest.Start(t, server.Config{PublicURL: "http://sub.test", DBPath: db,
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})
}

func newRig(t *testing.T, mode string) *rig {
	t.Helper()
	gh := newForge(t, "", "Bearer "+ghToken)
	fj := newForge(t, "/api/v1", "token "+fjToken)
	writeConfig(t, gh, fj)
	t.Setenv("SUBSPACE_REVIEWSYNC", mode)
	db := filepath.Join(t.TempDir(), "t.db")
	r := &rig{t: t, db: db, gh: gh, fj: fj, e: start(t, db)}
	r.lead = r.e.Register("rex", "leader", "other")
	r.wk = r.e.Register("w1", "worker", "claude-code")
	r.rv = r.e.Register("r1", "reviewer", "codex")
	_, br := r.e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B", "repos": []string{"o/gh", "o/fj"}, "max_parallel_agents": 50})
	r.bid = br["id"].(string)
	return r
}

func (r *rig) task(title, repo string, criteria ...string) string {
	r.t.Helper()
	plan := r.e.Must(r.lead, "propose_plan", map[string]any{"brief_id": r.bid, "tasks": []any{
		map[string]any{"title": title, "spec": "SECRET-SPEC-TEXT do not leak", "repo": repo, "acceptance_criteria": criteria},
	}})["plan"].(map[string]any)
	if code, out := r.e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		r.t.Fatalf("approve plan: %d %v", code, out)
	}
	return plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
}

func (r *rig) submit(id, prURL string) {
	r.t.Helper()
	r.e.Must(r.lead, "assign", map[string]any{"task_id": id, "agent_id": "w1"})
	r.e.Must(r.wk, "claim_task", map[string]any{"task_id": id})
	r.e.Must(r.wk, "submit", map[string]any{"task_id": id, "pr_url": prURL, "summary": "done"})
}

func (r *rig) review(id, verdict, comments string) {
	r.t.Helper()
	r.e.Must(r.rv, "review", map[string]any{"task_id": id, "verdict": verdict, "comments": comments})
}

func (r *rig) rework(id, prURL string) {
	r.t.Helper()
	r.e.Must(r.wk, "claim_task", map[string]any{"task_id": id})
	r.e.Must(r.wk, "submit", map[string]any{"task_id": id, "pr_url": prURL, "summary": "again"})
}

func TestReviewCommentsFollowRoundsOnTheSamePRComment(t *testing.T) {
	r := newRig(t, "changes_requested")
	id := r.task("Add login", "o/gh", "users can log in", "users can log out")
	r.submit(id, "https://github.com/o/gh/pull/1")

	r.review(id, "changes_requested", "criterion 1: pass\ncriterion 2: fail, logout 500s")
	r.gh.wait(t, "first comment", func() bool { return r.gh.posts == 1 })
	cid, body := r.gh.only(t)
	for _, want := range []string{
		"<!-- subspace:review task=" + id + " -->", "changes requested", "http://sub.test/tasks/" + id,
		`1\. **met**: users can log in`, `2\. **not met**: users can log out`, "logout 500s", "Round 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("first body misses %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "SECRET-SPEC-TEXT") {
		t.Errorf("task spec leaked into the comment:\n%s", body)
	}
	if r.gh.targets[cid] != "o/gh#1" {
		t.Errorf("comment target %q", r.gh.targets[cid])
	}

	r.rework(id, "https://github.com/o/gh/pull/1")
	r.review(id, "changes_requested", "criterion 2: fail, still 500")
	r.gh.wait(t, "second round update", func() bool { return r.gh.patches == 1 })
	cid2, body := r.gh.only(t)
	if cid2 != cid || !strings.Contains(body, "Round 2") || !strings.Contains(body, "still 500") || strings.Contains(body, "logout 500s") {
		t.Fatalf("round 2 did not update in place (%d vs %d):\n%s", cid2, cid, body)
	}

	r.rework(id, "https://github.com/o/gh/pull/1")
	r.review(id, "approve", "all good")
	r.gh.wait(t, "approval update", func() bool { return r.gh.patches == 2 })
	if _, body = r.gh.only(t); !strings.Contains(body, "approved") || strings.Contains(body, "changes requested") {
		t.Fatalf("stale comment after approval:\n%s", body)
	}
	if r.gh.posts != 1 || r.gh.badAuth != 0 || r.fj.posts != 0 {
		t.Fatalf("posts=%d badAuth=%d forgejo posts=%d", r.gh.posts, r.gh.badAuth, r.fj.posts)
	}
}

func TestReviewCommentOnForgejoAndHostileText(t *testing.T) {
	r := newRig(t, "all")
	id := r.task("Fix <b>bold</b> @everyone", "o/fj", "no regressions #1")
	r.submit(id, r.fj.URL+"/o/fj/pulls/2")

	r.review(id, "changes_requested", "@alice please look, see #12 and <img src=x onerror=1>\n[x](http://evil.test)\n```\nbreak\n```")
	r.fj.wait(t, "forgejo comment", func() bool { return r.fj.posts == 1 })
	cid, body := r.fj.only(t)
	if r.fj.targets[cid] != "o/fj#2" || r.fj.badAuth != 0 {
		t.Fatalf("target=%q badAuth=%d", r.fj.targets[cid], r.fj.badAuth)
	}
	scan := strings.Replace(body, "[T-1](http://sub.test/tasks/T-1)", "", 1)
	for _, bad := range []string{"@alice", "<img", "#12", "](http", "\n```", "@everyone"} {
		if strings.Contains(scan, bad) {
			t.Errorf("hostile text %q survived:\n%s", bad, body)
		}
	}
	if strings.Count(body, "<!--") != 1 {
		t.Errorf("extra html comment:\n%s", body)
	}

	r.rework(id, r.fj.URL+"/o/fj/pulls/2")
	r.review(id, "approve", "fine now")
	r.fj.wait(t, "approval", func() bool { return r.fj.patches == 1 })
	if r.gh.posts != 0 {
		t.Fatalf("github host got %d comments", r.gh.posts)
	}
}

func TestCommentIdsPersistAcrossServerRestart(t *testing.T) {
	r := newRig(t, "changes_requested")
	id := r.task("Add login", "o/gh", "users can log in")
	r.submit(id, "https://github.com/o/gh/pull/1")
	r.review(id, "changes_requested", "first round")
	r.gh.wait(t, "first comment", func() bool { return r.gh.posts == 1 })
	cid, _ := r.gh.only(t)
	st := r.e.Srv.Store.(interface {
		ReviewComment(ctx context.Context, taskID string) (string, int64, int64, string, bool, error)
	})
	r.gh.wait(t, "comment id stored", func() bool {
		_, got, _, _, ok, err := st.ReviewComment(context.Background(), id)
		return err == nil && ok && got == cid
	})

	r.e.TS.Close()
	if err := r.e.Srv.Close(); err != nil {
		t.Fatal(err)
	}
	r.e = start(t, r.db)

	r.rework(id, "https://github.com/o/gh/pull/1")
	r.review(id, "changes_requested", "second round")
	r.gh.wait(t, "update after restart", func() bool { return r.gh.patches == 1 })
	got, body := r.gh.only(t)
	if got != cid || r.gh.posts != 1 || !strings.Contains(body, "second round") {
		t.Fatalf("restart created a new comment: id %d -> %d posts=%d\n%s", cid, got, r.gh.posts, body)
	}
}

func TestReviewSyncIsOffUnlessConfigured(t *testing.T) {
	r := newRig(t, "")
	id := r.task("Add login", "o/gh", "users can log in")
	r.submit(id, "https://github.com/o/gh/pull/1")
	r.review(id, "changes_requested", "nothing should be posted")
	time.Sleep(300 * time.Millisecond)
	r.gh.mu.Lock()
	defer r.gh.mu.Unlock()
	if r.gh.posts != 0 {
		t.Fatalf("comments posted while disabled: %d", r.gh.posts)
	}
}
