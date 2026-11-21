package mergequeue

import (
	"context"
	"strings"

	"github.com/SigmaUno/subspace/internal/githost/forgejo"
)

type ForgejoFinisher map[string]*forgejo.Client

func (f ForgejoFinisher) Merged(ctx context.Context, repo string, pr int, sha string) error {
	c := f[strings.ToLower(repo)]
	if c == nil {
		return nil
	}
	return c.MarkMerged(ctx, repo, int64(pr), sha)
}
