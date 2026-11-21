package reviewsync_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/forgejotest"
	"github.com/SigmaUno/subspace/internal/forgejotest/serverrig"
)

type issueComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

func comments(t *testing.T, inst *forgejotest.Instance, repo *forgejotest.Repo, n int64) []issueComment {
	t.Helper()
	var out []issueComment
	inst.Do(t, "", http.MethodGet, repo.Path("/issues/"+strconv.FormatInt(n, 10)+"/comments"), nil, &out)
	return out
}

func waitComment(t *testing.T, inst *forgejotest.Instance, repo *forgejotest.Repo, n int64, want string) issueComment {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		cs := comments(t, inst, repo, n)
		if len(cs) == 1 && strings.Contains(cs[0].Body, want) {
			return cs[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("comments = %+v, want one containing %q", cs, want)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestLiveReviewCommentsOnForgejoPullRequest(t *testing.T) {
	inst := forgejotest.Start(t)
	repo := inst.NewRepo(t)
	repo.Commit(t, "sub/t-1-a", "main", "a.txt", "a\n")
	pr := repo.OpenPR(t, "sub/t-1-a", "main", "Add a")
	rig := serverrig.New(t, inst, repo, map[string]string{"SUBSPACE_REVIEWSYNC": "all"})

	id := rig.Task("Add <b>a</b>", "a exists", "a is documented")
	rig.Submit(id, pr)
	rig.Review(id, "changes_requested", "criterion 1: pass\ncriterion 2: fail, @alice see #12")
	first := waitComment(t, inst, repo, pr.Number, "changes requested")
	for _, want := range []string{"<!-- subspace:review task=" + id + " -->", "https://sub.test/tasks/" + id, "Round 1"} {
		if !strings.Contains(first.Body, want) {
			t.Errorf("comment misses %q:\n%s", want, first.Body)
		}
	}
	for _, bad := range []string{"@alice", "#12", "<b>", "spec 1"} {
		if strings.Contains(first.Body, bad) {
			t.Errorf("comment contains %q:\n%s", bad, first.Body)
		}
	}

	inst.Do(t, "", http.MethodDelete, repo.Path("/issues/comments/"+strconv.FormatInt(first.ID, 10)), nil, nil)
	rig.Rework(id, pr)
	rig.Review(id, "changes_requested", "again")
	recreated := waitComment(t, inst, repo, pr.Number, "Round 2")
	if recreated.ID == first.ID {
		t.Fatalf("the deleted comment %d was not recreated", first.ID)
	}

	rig.Rework(id, pr)
	rig.Review(id, "approve", "all good now")
	second := waitComment(t, inst, repo, pr.Number, "approved")
	if second.ID != recreated.ID || strings.Contains(second.Body, "changes requested") {
		t.Fatalf("approval did not update comment %d in place: %+v", recreated.ID, second)
	}
}
