package mcp

import (
	"context"

	"github.com/SigmaUno/subspace/internal/model"
)

func AppendToInstructions(r *Registry, extra func(ctx context.Context, actor model.Actor) (string, error)) {
	t, ok := r.tools["get_instructions"]
	if !ok {
		return
	}
	inner := t.Handler
	t.Handler = func(ctx context.Context, c *Call) (any, error) {
		out, err := inner(ctx, c)
		if err != nil {
			return out, err
		}
		m, ok := out.(map[string]any)
		if !ok {
			return out, nil
		}
		s, ok := m["instructions"].(string)
		if !ok {
			return out, nil
		}
		add, err := extra(ctx, c.Actor)
		if err != nil {
			return nil, err
		}
		m["instructions"] = s + add
		return m, nil
	}
}
