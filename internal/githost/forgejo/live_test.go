package forgejo_test

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/forgejotest"
	"github.com/SigmaUno/subspace/internal/githost"
	"github.com/SigmaUno/subspace/internal/githost/forgejo"
)

func TestLiveClient(t *testing.T) {
	inst := forgejotest.Start(t)
	repo := inst.NewRepo(t)
	headSHA := repo.Commit(t, "feat-a", "main", "a.txt", "hello\n")
	pr := repo.OpenPR(t, "feat-a", "main", "Add a")
	ctx := context.Background()
	c := forgejo.New(inst.URL, inst.Token, nil)

	t.Run("get pr", func(t *testing.T) {
		got, err := c.GetPR(ctx, repo.FullName(), pr.Number)
		if err != nil {
			t.Fatal(err)
		}
		want := githost.PR{
			Repo: repo.FullName(), Number: pr.Number, Title: "Add a", State: "open",
			HeadBranch: "feat-a", HeadSHA: headSHA, BaseBranch: "main", URL: repo.PRURL(pr.Number),
		}
		if got != want {
			t.Fatalf("pr = %+v\nwant %+v", got, want)
		}
		if _, err := c.GetPR(ctx, repo.FullName(), 999); !errors.Is(err, githost.ErrNotFound) {
			t.Fatalf("missing pr error = %v", err)
		}
		if _, err := forgejo.New(inst.URL, "bad-token", nil).GetPR(ctx, repo.FullName(), pr.Number); err == nil {
			t.Fatal("bad token accepted")
		}
	})

	t.Run("diff", func(t *testing.T) {
		diff, err := c.GetPRDiff(ctx, repo.FullName(), pr.Number)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(diff, "+++ b/a.txt") || !strings.Contains(diff, "+hello") {
			t.Fatalf("diff = %q", diff)
		}
	})

	t.Run("statuses", func(t *testing.T) {
		for _, state := range []string{githost.StatePending, githost.StateFailure, githost.StateSuccess, githost.StateError} {
			err := c.PostStatus(ctx, repo.FullName(), headSHA, githost.CommitStatus{
				State: state, Context: "subspace/review", Description: "d " + state, TargetURL: "https://subspace.example.com/tasks/T-1",
			})
			if err != nil {
				t.Fatalf("post %s: %v", state, err)
			}
		}
		var combined struct {
			State    string `json:"state"`
			Statuses []struct {
				Context     string `json:"context"`
				Status      string `json:"status"`
				Description string `json:"description"`
				TargetURL   string `json:"target_url"`
			} `json:"statuses"`
		}
		inst.Do(t, "", http.MethodGet, repo.Path("/commits/"+headSHA+"/status"), nil, &combined)
		if len(combined.Statuses) != 1 || combined.Statuses[0].Context != "subspace/review" ||
			combined.Statuses[0].Status != "error" || combined.Statuses[0].Description != "d error" ||
			combined.Statuses[0].TargetURL != "https://subspace.example.com/tasks/T-1" {
			t.Fatalf("combined status = %+v", combined)
		}
		err := c.PostStatus(ctx, repo.FullName(), strings.Repeat("0", 40), githost.CommitStatus{State: githost.StateSuccess, Context: "x"})
		if err == nil {
			t.Fatal("status on an unknown commit succeeded")
		}
	})

	t.Run("comments", func(t *testing.T) {
		cm := githost.NewCommenter(githost.CommenterConfig{Style: githost.StyleForgejo, APIURL: inst.APIURL(), Token: inst.Token})
		id, err := cm.CreateComment(ctx, repo.FullName(), pr.Number, "first")
		if err != nil {
			t.Fatal(err)
		}
		if err := cm.UpdateComment(ctx, repo.FullName(), id, "second"); err != nil {
			t.Fatal(err)
		}
		var comments []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
		}
		inst.Do(t, "", http.MethodGet, repo.Path("/issues/"+strconv.FormatInt(pr.Number, 10)+"/comments"), nil, &comments)
		if len(comments) != 1 || comments[0].ID != id || comments[0].Body != "second" {
			t.Fatalf("comments = %+v (created %d)", comments, id)
		}
		if err := cm.UpdateComment(ctx, repo.FullName(), id+1000, "x"); !errors.Is(err, githost.ErrNotFound) {
			t.Fatalf("update of a missing comment = %v", err)
		}
	})
}
