package forgestatus_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/forgejotest"
	"github.com/SigmaUno/subspace/internal/forgejotest/serverrig"
	"github.com/SigmaUno/subspace/internal/forgestatus"
)

type reviewStatus struct {
	State       string
	Description string
	TargetURL   string
}

func reviewContext(t *testing.T, inst *forgejotest.Instance, repo *forgejotest.Repo, sha string) (reviewStatus, int) {
	t.Helper()
	var combined struct {
		Statuses []struct {
			Context     string `json:"context"`
			Status      string `json:"status"`
			Description string `json:"description"`
			TargetURL   string `json:"target_url"`
		} `json:"statuses"`
	}
	inst.Do(t, "", http.MethodGet, repo.Path("/commits/"+sha+"/status"), nil, &combined)
	for _, s := range combined.Statuses {
		if s.Context == forgestatus.Context {
			return reviewStatus{s.Status, s.Description, s.TargetURL}, len(combined.Statuses)
		}
	}
	return reviewStatus{}, len(combined.Statuses)
}

func waitState(t *testing.T, inst *forgejotest.Instance, repo *forgejotest.Repo, sha, want string) reviewStatus {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		got, _ := reviewContext(t, inst, repo, sha)
		if got.State == want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("status of %s = %q, want %q", sha, got.State, want)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestLiveReviewStatusFollowsTheTask(t *testing.T) {
	inst := forgejotest.Start(t)
	repo := inst.NewRepo(t)
	sha := repo.Commit(t, "sub/t-1-a", "main", "a.txt", "a\n")
	pr := repo.OpenPR(t, "sub/t-1-a", "main", "Add a")
	rig := serverrig.New(t, inst, repo, nil)

	id := rig.Task("Add a")
	rig.Submit(id, pr)
	got := waitState(t, inst, repo, sha, "pending")
	if got.Description != "Awaiting Subspace review" || got.TargetURL != "https://sub.test/tasks/"+id {
		t.Fatalf("pending status = %+v", got)
	}

	rig.Review(id, "changes_requested", "no")
	if got := waitState(t, inst, repo, sha, "failure"); got.Description != "Subspace reviewer requested changes" {
		t.Fatalf("failure status = %+v", got)
	}

	rig.Rework(id, pr)
	rig.Review(id, "approve", "yes")
	if got := waitState(t, inst, repo, sha, "success"); got.Description != "Approved by Subspace review" {
		t.Fatalf("success status = %+v", got)
	}
	if _, n := reviewContext(t, inst, repo, sha); n != 1 {
		t.Fatalf("%d status contexts on the commit, want only %s", n, forgestatus.Context)
	}
}
