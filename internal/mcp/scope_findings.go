package mcp

import (
	"context"
	"sync"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/scope"
)

type ScopeSource interface {
	Findings(ctx context.Context, taskIDs []string) (map[string][]scope.Finding, error)
}

var scopeSources sync.Map

func BindScope(b *board.Board, s ScopeSource) { scopeSources.Store(b, s) }

func RegisterScope(r *Registry, b *board.Board) {
	t := r.tools["list_reviews"]
	inner := t.Handler
	t.Handler = func(ctx context.Context, c *Call) (any, error) {
		res, err := inner(ctx, c)
		if err != nil {
			return res, err
		}
		out, ok := res.(map[string]any)
		if !ok {
			return res, nil
		}
		findings := map[string][]scope.Finding{}
		if src, ok := scopeSources.Load(b); ok {
			tasks, _ := out["tasks"].([]*model.Task)
			ids := make([]string, len(tasks))
			for i, task := range tasks {
				ids[i] = task.ID
			}
			var ferr error
			if findings, ferr = src.(ScopeSource).Findings(ctx, ids); ferr != nil {
				return nil, ferr
			}
		}
		out["scope_findings"] = findings
		return out, nil
	}
}
