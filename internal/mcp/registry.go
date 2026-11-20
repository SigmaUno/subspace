// Package mcp implements the MCP Streamable-HTTP transport (JSON-RPC 2.0) and the role-filtered tool registry.
package mcp

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/SigmaUno/subspace/internal/model"
)

// Call is the context of one tools/call.
type Call struct {
	Actor model.Actor
	Args  json.RawMessage
}

// Handler executes a tool and returns a JSON-serializable result.
type Handler func(ctx context.Context, c *Call) (any, error)

// Tool is a registered MCP tool.
type Tool struct {
	Name        string
	Description string
	Roles       []string       // roles that may see/call the tool
	Schema      map[string]any // JSON Schema for inputs
	// ReadOnly tools are exempt from loop protection.
	ReadOnly bool
	Handler  Handler
}

// Registry holds tools.
type Registry struct{ tools map[string]*Tool }

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{tools: map[string]*Tool{}} }

// Register adds a tool.
func (r *Registry) Register(t *Tool) { r.tools[t.Name] = t }

// Get returns a tool if the role may use it.
func (r *Registry) Get(name, role string) (*Tool, bool) {
	t, ok := r.tools[name]
	if !ok || !hasRole(t.Roles, role) {
		return nil, false
	}
	return t, true
}

// List returns the tools visible to a role, sorted by name.
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

// Role groups used by the tool table (docs/api.md §5).
var (
	AllRoles   = []string{model.RoleLeader, model.RoleWorker, model.RoleTester, model.RoleReviewer}
	WorkTester = []string{model.RoleWorker, model.RoleTester}
	TesterOnly = []string{model.RoleTester}
	ReviewOnly = []string{model.RoleReviewer}
	LeaderOnly = []string{model.RoleLeader}
)
