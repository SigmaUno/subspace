package prsync_test

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/forgejotest"
	"github.com/SigmaUno/subspace/internal/githost"
	"github.com/SigmaUno/subspace/internal/githost/forgejo"
	"github.com/SigmaUno/subspace/internal/scope"
)

func newLiveRig(t *testing.T) (*forgeRig, *forgejotest.Repo, *forgejotest.Instance) {
	t.Helper()
	inst := forgejotest.Start(t)
	repo := inst.NewRepo(t)
	return newForgeRig(t, inst.URL, inst.Token, repo.FullName()), repo, inst
}

func TestLivePollingAgainstForgejo(t *testing.T) {
	r, repo, inst := newLiveRig(t)
	sha := repo.Commit(t, "sub/t-1-a", "main", "a.txt", "a\n")
	pr := repo.OpenPR(t, "sub/t-1-a", "main", "Add a")
	id := r.submit(pr.Number)
	repo.WaitMergeable(t, pr.Number)

	r.sync()
	s := r.status(id)
	if s["state"] != "open" || s["checks"] != "none" || s["host"] != "forge" || s["repo"] != repo.FullName() ||
		s["number"] != float64(pr.Number) || s["head_sha"] != sha || s["mergeable"] != true || s["draft"] != false ||
		s["review_decision"] != "none" || s["error"] != nil {
		t.Fatalf("first status: %v", s)
	}
	if st := r.stored(id); st.Diff == nil || len(st.Diff.Files) != 1 || st.Diff.Files[0] != "a.txt" || st.Diff.Additions != 1 {
		t.Fatalf("stored diff: %+v", st.Diff)
	}

	r.sync()
	if again := r.status(id); again["changed_at"] != s["changed_at"] || again["checked_at"] == s["checked_at"] {
		t.Fatalf("an unchanged PR changed: %v -> %v", s, again)
	}

	fc := forgejo.New(inst.URL, inst.Token, nil)
	post := func(state string) {
		t.Helper()
		if err := fc.PostStatus(context.Background(), repo.FullName(), sha, githost.CommitStatus{State: state, Context: "ci/build"}); err != nil {
			t.Fatal(err)
		}
		r.sync()
	}
	for _, state := range []string{"pending", "failure", "success"} {
		post(state)
		if got := r.status(id)["checks"]; got != state {
			t.Fatalf("checks after %s = %v", state, got)
		}
	}
	if err := fc.PostStatus(context.Background(), repo.FullName(), sha, githost.CommitStatus{State: "pending", Context: "ci/lint"}); err != nil {
		t.Fatal(err)
	}
	r.sync()
	if got := r.status(id)["checks"]; got != "pending" {
		t.Fatalf("checks with a pending context = %v", got)
	}
	post("success")

	reviewer := inst.NewUser(t)
	repo.Collaborator(t, reviewer)
	repo.Review(t, reviewer, pr.Number, "REQUEST_CHANGES")
	r.sync()
	if s := r.status(id); s["review_decision"] != "changes_requested" || len(s["reviews"].([]any)) != 1 {
		t.Fatalf("after request changes: %v", s)
	}
	repo.Review(t, reviewer, pr.Number, "COMMENT")
	repo.Review(t, reviewer, pr.Number, "APPROVED")
	r.sync()
	s = r.status(id)
	review := s["reviews"].([]any)[0].(map[string]any)
	if s["review_decision"] != "approved" || review["user"] != reviewer || review["state"] != "approved" {
		t.Fatalf("after approval: %v", s)
	}

	newSHA := repo.Commit(t, "sub/t-1-a", "main", "b.txt", "b\n")
	s = r.syncUntil(id, func(s map[string]any) bool { return s["head_sha"] == newSHA })
	if s["checks"] != "none" {
		t.Fatalf("after push: %v", s)
	}
	if st := r.stored(id); st.Diff == nil || len(st.Diff.Files) != 2 {
		t.Fatalf("diff after push: %+v", st.Diff)
	}
	if msgs := r.syncMessages(id); len(msgs) != 1 || !strings.Contains(msgs[0], "Checks failed") {
		t.Fatalf("messages: %v", msgs)
	}

	repo.WaitMergeable(t, pr.Number)
	inst.Do(t, "", http.MethodPost, repo.Path("/pulls/"+strconv.FormatInt(pr.Number, 10)+"/merge"), map[string]string{"Do": "merge"}, nil)
	r.sync()
	if s := r.status(id); s["state"] != "merged" {
		t.Fatalf("after merge: %v", s)
	}
	if msgs := r.syncMessages(id); !strings.Contains(strings.Join(msgs, "\n"), "merged") {
		t.Fatalf("no merge message: %v", msgs)
	}
}

func TestLiveClosedAndDraftPullRequests(t *testing.T) {
	r, repo, inst := newLiveRig(t)
	repo.Commit(t, "sub/t-2-w", "main", "w.txt", "w\n")
	draft := repo.OpenPR(t, "sub/t-2-w", "main", "WIP: not ready")
	repo.Commit(t, "sub/t-3-c", "main", "c.txt", "c\n")
	closed := repo.OpenPR(t, "sub/t-3-c", "main", "Close me")
	dID, cID := r.submit(draft.Number), r.submit(closed.Number)

	r.sync()
	if s := r.status(dID); s["draft"] != true || s["state"] != "open" {
		t.Fatalf("draft status: %v", s)
	}
	inst.Do(t, "", http.MethodPatch, repo.Path("/pulls/"+strconv.FormatInt(closed.Number, 10)), map[string]string{"state": "closed"}, nil)
	r.sync()
	if s := r.status(cID); s["state"] != "closed" {
		t.Fatalf("closed status: %v", s)
	}
	if msgs := r.syncMessages(cID); len(msgs) != 1 || !strings.Contains(msgs[0], "closed without merging") {
		t.Fatalf("messages: %v", msgs)
	}
}

func TestLiveLargePullRequestPagination(t *testing.T) {
	r, repo, _ := newLiveRig(t)
	const n = 120
	dir := repo.Clone(t)
	repo.Git(t, dir, "checkout", "-q", "-b", "sub/t-4-big")
	for i := range n {
		repo.CommitFile(t, dir, fmt.Sprintf("dir/f%03d.txt", i), "x\n")
	}
	repo.Git(t, dir, "push", "-q", "origin", "sub/t-4-big")
	pr := repo.OpenPR(t, "sub/t-4-big", "main", "Big")
	id := r.submit(pr.Number)
	r.sync()

	st := r.stored(id)
	if st.Diff == nil || st.Diff.Truncated || len(st.Diff.Files) != n || st.Diff.Additions != n {
		t.Fatalf("diff files = %d (truncated %v) additions %d, want %d", len(st.Diff.Files), st.Diff.Truncated, st.Diff.Additions, n)
	}
	if len(st.Diff.Files) > scope.MaxFiles {
		t.Fatalf("scope.MaxFiles = %d is below the test size", scope.MaxFiles)
	}

	last := repo.CommitFile(t, dir, "dir/last.txt", "x\n")
	repo.Git(t, dir, "push", "-q", "origin", "sub/t-4-big")
	r.syncUntil(id, func(s map[string]any) bool { return s["head_sha"] == last })
	if msgs := r.syncMessages(id); len(msgs) != 0 {
		t.Fatalf("a normal push was reported as a force push: %v", msgs)
	}
}

func TestLiveReviewsBeyondTheFirstPage(t *testing.T) {
	r, repo, inst := newLiveRig(t)
	repo.Commit(t, "sub/t-5-rev", "main", "r.txt", "r\n")
	pr := repo.OpenPR(t, "sub/t-5-rev", "main", "Review me")
	id := r.submit(pr.Number)
	reviewer := inst.NewUser(t)
	repo.Collaborator(t, reviewer)

	repo.Review(t, reviewer, pr.Number, "REQUEST_CHANGES")
	for range 30 {
		repo.Review(t, reviewer, pr.Number, "COMMENT")
	}
	repo.Review(t, reviewer, pr.Number, "APPROVED")
	r.sync()
	s := r.status(id)
	if s["review_decision"] != "approved" || len(s["reviews"].([]any)) != 1 {
		t.Fatalf("reviews: %v", s)
	}
}
