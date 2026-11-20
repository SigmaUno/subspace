package bots

import (
	"context"
	"encoding/json"

	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/model"
)

func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(desc string) map[string]any  { return map[string]any{"type": "string", "description": desc} }
func intg(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }

func kindSchema() map[string]any {
	return map[string]any{
		"type": "string", "description": "bot kind",
		"enum": []string{"claude-code", "codex", "opencode-anthropic", "opencode-deepseek"},
	}
}

func roleSchema() map[string]any {
	return map[string]any{
		"type": "string", "description": "agent role (default worker)",
		"enum": []string{model.RoleLeader, model.RoleWorker, model.RoleTester, model.RoleReviewer},
	}
}

func bind[T any](fn func(ctx context.Context, actor model.Actor, in T) (any, error)) mcp.Handler {
	return func(ctx context.Context, c *mcp.Call) (any, error) {
		var in T
		if len(c.Args) > 0 && string(c.Args) != "null" {
			if err := json.Unmarshal(c.Args, &in); err != nil {
				return nil, model.BadRequest("invalid arguments: %v", err)
			}
		}
		return fn(ctx, c.Actor, in)
	}
}

type idReason struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

func (s *Service) Tools(roles []string) []*mcp.Tool {
	idSchema := obj(map[string]any{"id": str("bot id")}, "id")
	reasonSchema := obj(map[string]any{"id": str("bot id"), "reason": str("why")}, "id")
	bot := func(b *Bot, err error) (any, error) { return map[string]any{"bot": b}, err }
	tool := func(name, desc string, ro bool, schema map[string]any, h mcp.Handler) *mcp.Tool {
		return &mcp.Tool{Name: name, Description: desc, Roles: roles, ReadOnly: ro, Schema: schema, Handler: h}
	}
	return []*mcp.Tool{
		tool("list_bots", "List bots, optionally filtered by role or status.", true,
			obj(map[string]any{"role": str("role filter"), "status": str("active, paused or revoked")}),
			bind(func(ctx context.Context, _ model.Actor, in struct{ Role, Status string }) (any, error) {
				bs, err := s.List(ctx, in.Role, in.Status)
				return map[string]any{"bots": bs}, err
			})),
		tool("create_bot", "Register a bot. The token is returned once.", false,
			obj(map[string]any{
				"name": str("bot id"), "role": roleSchema(), "kind": kindSchema(), "provider": str("provider, defaults from kind"),
				"model": str("model name"), "capacity_weight": intg("capacity units the bot consumes (default 1)"),
			}, "name", "kind"),
			bind(func(ctx context.Context, a model.Actor, in CreateInput) (any, error) { return s.Create(ctx, a, in) })),
		tool("create_bots_bulk", "Register many bots named <prefix>-001... Tokens are returned once.", false,
			obj(map[string]any{
				"prefix": str("id prefix"), "count": intg("number of bots, 1-1000"), "role": roleSchema(), "kind": kindSchema(),
				"provider": str("provider, defaults from kind"), "model": str("model name"),
				"capacity_weight": intg("capacity units per bot (default 1)"),
			}, "prefix", "count", "kind"),
			bind(func(ctx context.Context, a model.Actor, in BulkInput) (any, error) {
				cs, err := s.BulkCreate(ctx, a, in)
				return map[string]any{"bots": cs}, err
			})),
		tool("revoke_bot", "Kill a bot's token and release its leases.", false, reasonSchema,
			bind(func(ctx context.Context, a model.Actor, in idReason) (any, error) {
				return bot(s.Revoke(ctx, a, in.ID, in.Reason))
			})),
		tool("delete_bot", "Delete a revoked bot.", false, idSchema,
			bind(func(ctx context.Context, a model.Actor, in idReason) (any, error) {
				return map[string]any{"deleted": in.ID}, s.Delete(ctx, a, in.ID)
			})),
		tool("pause_bot", "Release a bot's leases and block its claims.", false, reasonSchema,
			bind(func(ctx context.Context, a model.Actor, in idReason) (any, error) {
				return bot(s.Pause(ctx, a, in.ID, in.Reason))
			})),
		tool("resume_bot", "Let a paused bot claim again.", false, idSchema,
			bind(func(ctx context.Context, a model.Actor, in idReason) (any, error) {
				return bot(s.Resume(ctx, a, in.ID))
			})),
		tool("kill_bot", "Pause a bot and stop its process.", false, reasonSchema,
			bind(func(ctx context.Context, a model.Actor, in idReason) (any, error) {
				return bot(s.Kill(ctx, a, in.ID, in.Reason))
			})),
		tool("spawn_bot", "Start a bot's process after a capacity check.", false,
			obj(map[string]any{"id": str("bot id"), "repo": str("repo to start in")}, "id"),
			bind(func(ctx context.Context, a model.Actor, in struct{ ID, Repo string }) (any, error) {
				return bot(s.Spawn(ctx, a, in.ID, in.Repo))
			})),
	}
}
