package mergequeue

import (
	"context"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/githost/github"
)

type Finishers map[string]Finisher

func (f Finishers) Merged(ctx context.Context, repo string, pr int, sha string) error {
	x := f[strings.ToLower(repo)]
	if x == nil {
		return nil
	}
	return x.Merged(ctx, repo, pr, sha)
}

type GitHubFinisher struct {
	Client *github.Client
	Settle time.Duration
	Poll   time.Duration
}

func (f GitHubFinisher) Merged(ctx context.Context, repo string, pr int, _ string) error {
	ref := github.PRRef{Repo: repo, Number: pr}
	deadline := time.Now().Add(f.Settle)
	for {
		p, err := f.Client.GetPR(ctx, ref)
		if err != nil {
			return err
		}
		if p.Merged || p.State == "closed" {
			return nil
		}
		if f.Poll <= 0 || !time.Now().Before(deadline) {
			break
		}
		t := time.NewTimer(f.Poll)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
	return f.Client.ClosePR(ctx, ref)
}
