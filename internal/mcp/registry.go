package mcp

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/SigmaUno/subspace/internal/model"
)

type Call struct {
	Actor model.Actor
	Args  json.RawMessage
}

type Handler func(ctx context.Context, c *Call) (any, error)

type Tool struct {
	Name        string
	Description string
	Roles       []string
	Schema      map[string]any

	ReadOnly bool
	Handler  Handler
}

type ErrorHook func(actor model.Actor, tool, code, message string, args json.RawMessage)

type ToolSource interface {
	Tools(ctx context.Context, role string) []*Tool
	Tool(ctx context.Context, name, role string) (*Tool, bool)
}

type Registry struct {
	tools   map[string]*Tool
	hooks   []ErrorHook
	sources []ToolSource
}

func (r *Registry) AddSource(s ToolSource) { r.sources = append(r.sources, s) }

func (r *Registry) Visible(ctx context.Context, role string) []*Tool {
	out := r.List(role)
	if len(r.sources) == 0 {
		return out
	}
	for _, s := range r.sources {
		for _, t := range s.Tools(ctx, role) {
			if _, taken := r.tools[t.Name]; !taken && hasRole(t.Roles, role) {
				out = append(out, t)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Registry) Lookup(ctx context.Context, name, role string) (*Tool, bool) {
	if t, ok := r.tools[name]; ok {
		return t, hasRole(t.Roles, role)
	}
	for _, s := range r.sources {
		if t, ok := s.Tool(ctx, name, role); ok && hasRole(t.Roles, role) {
			return t, true
		}
	}
	return nil, false
}

func NewRegistry0() *Registry { return &Registry{tools: map[string]*Tool{}} }

func (r *Registry) OnError(h ErrorHook) { r.hooks = append(r.hooks, h) }

func (r *Registry) fail(actor model.Actor, tool, code, message string, args json.RawMessage) {
	for _, h := range r.hooks {
		h(actor, tool, code, message, args)
	}
}

func (r *Registry) Register(t *Tool) { r.tools[t.Name] = t }

func (r *Registry) Get(name, role string) (*Tool, bool) {
	t, ok := r.tools[name]
	if !ok || !hasRole(t.Roles, role) {
		return nil, false
	}
	return t, true
}

func (r *Registry) List(role string) []*Tool {
	var out []*Tool
	for _, t := range r.tools {
		if hasRole(t.Roles, role) {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func hasRole(roles []string, role string) bool {
	for _, x := range roles {
		if x == role {
			return true
		}
	}
	return false
}

var (
	AllRoles   = []string{model.RoleLeader, model.RoleWorker, model.RoleTester, model.RoleReviewer}
	WorkTester = []string{model.RoleWorker, model.RoleTester}
	TesterOnly = []string{model.RoleTester}
	ReviewOnly = []string{model.RoleReviewer}
	LeaderOnly = []string{model.RoleLeader}
)
