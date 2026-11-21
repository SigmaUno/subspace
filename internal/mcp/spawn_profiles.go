package mcp

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/profiles"
)

type ProfileResolver func(ctx context.Context, name string) (profiles.Profile, error)

func BindSpawnProfiles(r *Registry, resolve ProfileResolver) {
	t, ok := r.tools["request_spawn"]
	if !ok {
		return
	}
	next := t.Handler
	t.Description += " Optionally name a profile: it fills worker_kind, model, role and provider that the call leaves out."
	t.Schema["properties"].(map[string]any)["profile"] = str("agent profile name; explicit fields override it")
	t.Schema["required"] = slices.DeleteFunc(slices.Clone(t.Schema["required"].([]string)), func(f string) bool {
		return f == "worker_kind" || f == "model"
	})
	t.Handler = func(ctx context.Context, c *Call) (any, error) {
		var fields profiles.Fields
		if len(c.Args) == 0 || json.Unmarshal(c.Args, &fields) != nil {
			return next(ctx, c)
		}
		raw, ok := fields["profile"]
		if !ok {
			return next(ctx, c)
		}
		delete(fields, "profile")
		var name string
		if json.Unmarshal(raw, &name) != nil || name == "" {
			return nil, model.BadRequest("profile must be a profile name")
		}
		p, err := resolve(ctx, name)
		if err != nil {
			if model.AsError(err).Code == "not_found" {
				err = model.BadRequest("unknown profile %q", name)
			}
			return nil, err
		}
		role, kind, mdl, provider := fields.Text("role"), fields.Text("worker_kind"), fields.Text("model"), fields.Text("provider")
		p.Apply(&role, &kind, &mdl, &provider)
		fields.Fill("role", role)
		fields.Fill("worker_kind", kind)
		fields.Fill("model", mdl)
		fields.Fill("provider", provider)
		args, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		c.Args = args
		return next(ctx, c)
	}
}
