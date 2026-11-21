package board

import "github.com/SigmaUno/subspace/internal/store"

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
