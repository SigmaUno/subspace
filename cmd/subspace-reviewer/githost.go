package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/SigmaUno/subspace/internal/githost"
	"github.com/SigmaUno/subspace/internal/githost/forgejo"
	"github.com/SigmaUno/subspace/internal/githost/github"
)

type forgejoSource struct{ c *forgejo.Client }

func (s forgejoSource) Fetch(ctx context.Context, prURL string) (*pullRequest, error) {
	repo, n, err := githost.ParsePRURL(prURL)
	if err != nil {
		return nil, err
	}
	pr, err := s.c.GetPR(ctx, repo, n)
	if err != nil {
		return nil, err
	}
	diff, err := s.c.GetPRDiff(ctx, repo, n)
	if err != nil {
		return nil, err
	}
	return &pullRequest{Repo: pr.Repo, Number: pr.Number, Title: pr.Title, Body: pr.Body, State: pr.State,
		Merged: pr.Merged, HeadBranch: pr.HeadBranch, Diff: diff}, nil
}

type githubSource struct{ c *github.Client }

func (s githubSource) Fetch(ctx context.Context, prURL string) (*pullRequest, error) {
	repo, n, err := githost.ParsePRURL(prURL)
	if err != nil {
		return nil, err
	}
	ref := github.PRRef{Repo: repo, Number: int(n)}
	pr, err := s.c.GetPR(ctx, ref)
	if err != nil {
		return nil, err
	}
	diff, err := s.c.GetPRDiff(ctx, ref)
	if err != nil {
		return nil, err
	}
	state := pr.State
	if pr.Merged {
		state = "closed"
	}
	return &pullRequest{Repo: pr.Ref.Repo, Number: int64(pr.Ref.Number), Title: pr.Title, Body: pr.Body, State: state,
		Merged: pr.Merged, HeadBranch: pr.Branch, Diff: diff}, nil
}

func newSource(kind, baseURL, token string, hc *http.Client) (prSource, error) {
	switch kind {
	case "forgejo":
		if baseURL == "" {
			return nil, fmt.Errorf("forgejo needs a git host url")
		}
		return forgejoSource{forgejo.New(baseURL, token, hc)}, nil
	case "github":
		return githubSource{github.New(github.Config{APIURL: baseURL, Token: token, HTTPClient: hc})}, nil
	default:
		return nil, fmt.Errorf("unknown git host %q (forgejo, github)", kind)
	}
}
