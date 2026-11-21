package mcp

import (
	"context"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/playbooks"
)

type PlaybookResolver func(ctx context.Context, name string) (playbooks.Playbook, error)

func BindPlaybooks(r *Registry, resolve PlaybookResolver) {
	t, ok := r.tools["get_brief"]
	if !ok {
		return
	}
	next := t.Handler
	t.Description += " When the brief has a playbook the result also holds its rules and instructions."
	t.Handler = func(ctx context.Context, c *Call) (any, error) {
		out, err := next(ctx, c)
		if err != nil {
			return out, err
		}
		res, ok := out.(map[string]any)
		if !ok {
			return out, nil
		}
		br, ok := res["brief"].(*model.Brief)
		if !ok || br == nil || br.Playbook == "" {
			return out, nil
		}
		p, err := resolve(ctx, br.Playbook)
		if err != nil {
			return nil, err
		}
		res["playbook"] = p
		return res, nil
	}
}
