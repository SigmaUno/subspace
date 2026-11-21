package mergequeue_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/forgejotest"
	"github.com/SigmaUno/subspace/internal/githost/forgejo"
	"github.com/SigmaUno/subspace/internal/mergequeue"
)

type recorder struct {
	mu       sync.Mutex
	approved bool
	notes    []string
	changes  []string
}

func (r *recorder) Notify(_ context.Context, taskID, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notes = append(r.notes, taskID+": "+body)
	return nil
}

func (r *recorder) Approved(context.Context, mergequeue.Candidate) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.approved, nil
}

func (r *recorder) RequestChanges(_ context.Context, taskID, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changes = append(r.changes, taskID+": "+reason)
	return nil
}

func liveQueue(t *testing.T, repo *forgejotest.Repo, verify []string, auto bool) (*mergequeue.Queue, *recorder) {
	t.Helper()
	rec := &recorder{}
	q, err := mergequeue.New(mergequeue.Config{
		Repos: map[string]mergequeue.RepoConfig{repo.FullName(): {
			Remote: repo.CloneURL(), Verify: verify, Auto: auto,
		}},
		Finisher: mergequeue.ForgejoFinisher{strings.ToLower(repo.FullName()): forgejo.New(repo.I.URL, repo.I.Token, nil)},
		WorkDir:  t.TempDir(),
		Store:    mergequeue.NewMemStore(),
		Notifier: rec,
		Gate:     rec,
		Tasks:    rec,
	})
	if err != nil {
		t.Fatal(err)
	}
	return q, rec
}

func waitMerged(t *testing.T, repo *forgejotest.Repo, n int64) forgejotest.PR {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		pr := repo.GetPR(t, n)
		if pr.Merged {
			return pr
		}
		if time.Now().After(deadline) {
			t.Fatalf("pull request %d is %s and not merged in Forgejo", n, pr.State)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func branchSHA(t *testing.T, inst *forgejotest.Instance, repo *forgejotest.Repo, branch string) string {
	t.Helper()
	var b struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	inst.Do(t, "", "GET", repo.Path("/branches/"+branch), nil, &b)
	return b.Commit.ID
}

func TestLiveRunnerMergesRealPullRequestsOverHTTP(t *testing.T) {
	inst := forgejotest.Start(t)
	repo := inst.NewRepo(t)
	repo.AllowManualMerge(t)
	repo.Commit(t, "sub/t-1-a", "main", "a.txt", "a\n")
	repo.Commit(t, "sub/t-2-b", "main", "b.txt", "b\n")
	first := repo.OpenPR(t, "sub/t-1-a", "main", "Add a")
	second := repo.OpenPR(t, "sub/t-2-b", "main", "Add b")
	q, rec := liveQueue(t, repo, []string{"sh", "-c", "test -f a.txt || test -f b.txt"}, true)
	ctx := context.Background()

	for _, c := range []mergequeue.Candidate{
		{Repo: repo.FullName(), PR: int(first.Number), Head: "sub/t-1-a", Base: "main", TaskID: "T-1"},
		{Repo: repo.FullName(), PR: int(second.Number), Head: "sub/t-2-b", Base: "main", TaskID: "T-2"},
	} {
		if _, err := q.Enqueue(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	c, err := q.Process(ctx, repo.FullName())
	if err != nil || c == nil || c.State != mergequeue.StateMerged {
		t.Fatalf("first process: %+v %v", c, err)
	}
	if got := branchSHA(t, inst, repo, "main"); got != c.MergedSHA {
		t.Fatalf("main = %s, merged sha %s", got, c.MergedSHA)
	}
	waitMerged(t, repo, first.Number)

	c, err = q.Process(ctx, repo.FullName())
	if err != nil || c == nil || c.State != mergequeue.StateMerged {
		t.Fatalf("second process (rebase onto the merged first): %+v %v\n%s", c, err, c.Log)
	}
	if got := branchSHA(t, inst, repo, "main"); got != c.MergedSHA {
		t.Fatalf("main = %s, merged sha %s", got, c.MergedSHA)
	}
	if branchSHA(t, inst, repo, "sub/t-2-b") != c.MergedSHA {
		t.Fatal("the PR branch was not moved to the rebased commit")
	}
	waitMerged(t, repo, second.Number)

	files := repo.Git(t, repo.Clone(t), "ls-files")
	for _, want := range []string{"a.txt", "b.txt"} {
		if !strings.Contains(files, want) {
			t.Fatalf("main misses %s: %q", want, files)
		}
	}
	if len(rec.notes) != 2 || len(rec.changes) != 0 {
		t.Fatalf("notes %v changes %v", rec.notes, rec.changes)
	}
	for _, n := range rec.notes {
		if strings.Contains(n, inst.Token) {
			t.Fatal("token leaked into a note")
		}
	}
}

func TestLiveRunnerEjectsFailingAndConflictingPullRequests(t *testing.T) {
	inst := forgejotest.Start(t)
	repo := inst.NewRepo(t)
	repo.Commit(t, "sub/t-3-bad", "main", "bad.txt", "bad\n")
	badPR := repo.OpenPR(t, "sub/t-3-bad", "main", "Fails verification")
	q, rec := liveQueue(t, repo, []string{"sh", "-c", "test ! -f bad.txt"}, true)
	ctx := context.Background()
	mainBefore := branchSHA(t, inst, repo, "main")

	if _, err := q.Enqueue(ctx, mergequeue.Candidate{Repo: repo.FullName(), PR: int(badPR.Number), Head: "sub/t-3-bad", Base: "main", TaskID: "T-3"}); err != nil {
		t.Fatal(err)
	}
	c, err := q.Process(ctx, repo.FullName())
	if err != nil || c == nil || c.State != mergequeue.StateEjected {
		t.Fatalf("failing verify: %+v %v", c, err)
	}
	if len(rec.changes) != 1 || !strings.Contains(rec.changes[0], "failed") {
		t.Fatalf("changes = %v", rec.changes)
	}
	if got := branchSHA(t, inst, repo, "main"); got != mainBefore {
		t.Fatal("main moved although verification failed")
	}
	if pr := repo.GetPR(t, badPR.Number); pr.Merged || pr.State != "open" {
		t.Fatalf("PR = %+v", pr)
	}

	dir := repo.Clone(t)
	repo.Git(t, dir, "checkout", "-q", "-b", "sub/t-5-conflict")
	repo.CommitFile(t, dir, "README.md", "branch version\n")
	repo.Git(t, dir, "push", "-q", "origin", "sub/t-5-conflict")
	repo.Git(t, dir, "checkout", "-q", "main")
	repo.CommitFile(t, dir, "README.md", "main version\n")
	repo.Git(t, dir, "push", "-q", "origin", "main")
	conflicting := repo.OpenPR(t, "sub/t-5-conflict", "main", "Conflicts")
	q, rec = liveQueue(t, repo, []string{"true"}, true)
	mainBefore = branchSHA(t, inst, repo, "main")
	if _, err := q.Enqueue(ctx, mergequeue.Candidate{Repo: repo.FullName(), PR: int(conflicting.Number), Head: "sub/t-5-conflict", Base: "main", TaskID: "T-5"}); err != nil {
		t.Fatal(err)
	}
	c, err = q.Process(ctx, repo.FullName())
	if err != nil || c == nil || c.State != mergequeue.StateEjected {
		t.Fatalf("conflicting rebase: %+v %v", c, err)
	}
	if len(rec.changes) != 1 || !strings.Contains(rec.changes[0], "rebase") {
		t.Fatalf("changes = %v", rec.changes)
	}
	if got := branchSHA(t, inst, repo, "main"); got != mainBefore {
		t.Fatal("main moved although the rebase failed")
	}
}

func TestLiveRunnerWarnsWhenForgejoCannotMarkTheMerge(t *testing.T) {
	inst := forgejotest.Start(t)
	repo := inst.NewRepo(t)
	repo.Commit(t, "sub/t-4-c", "main", "c.txt", "c\n")
	pr := repo.OpenPR(t, "sub/t-4-c", "main", "Add c")
	q, rec := liveQueue(t, repo, []string{"true"}, true)
	ctx := context.Background()
	if _, err := q.Enqueue(ctx, mergequeue.Candidate{Repo: repo.FullName(), PR: int(pr.Number), Head: "sub/t-4-c", Base: "main", TaskID: "T-4"}); err != nil {
		t.Fatal(err)
	}
	c, err := q.Process(ctx, repo.FullName())
	if err != nil || c == nil || c.State != mergequeue.StateMerged {
		t.Fatalf("process: %+v %v", c, err)
	}
	if got := branchSHA(t, inst, repo, "main"); got != c.MergedSHA {
		t.Fatalf("main = %s, merged sha %s", got, c.MergedSHA)
	}
	if len(rec.notes) != 1 || !strings.Contains(rec.notes[0], "could not be marked merged") {
		t.Fatalf("notes = %v", rec.notes)
	}
}
