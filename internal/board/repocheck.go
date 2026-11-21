package board

import (
	"context"

	"github.com/SigmaUno/subspace/internal/redact"
	"github.com/SigmaUno/subspace/internal/store"
)

type RepoCheck func(tx store.Tx, repo, branch string) error

func (b *Board) SetRepoCheck(c RepoCheck) {
	b.mu.Lock()
	b.repoCheck = c
	b.mu.Unlock()
}

func (b *Board) checkRegistry(tx store.Tx, repo, branch string) error {
	b.mu.Lock()
	c := b.repoCheck
	b.mu.Unlock()
	if c == nil {
		return nil
	}
	return c(tx, repo, branch)
}

type RepoCredentials func(ctx context.Context, repo string) (env map[string]string, secret string, err error)

func (b *Board) SetRepoCredentials(c RepoCredentials) {
	b.mu.Lock()
	b.repoCreds = c
	b.mu.Unlock()
}

func (b *Board) agentCredentials(ctx context.Context, repo string) (map[string]string, string) {
	b.mu.Lock()
	c := b.repoCreds
	b.mu.Unlock()
	if c == nil || repo == "" {
		return nil, ""
	}
	env, secret, err := c(ctx, repo)
	if err != nil {
		b.log.Warn("agent git credentials unavailable", "repo", repo, "err", redact.String(err.Error()))
		return nil, ""
	}
	return env, secret
}
