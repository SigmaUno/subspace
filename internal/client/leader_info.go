package client

import (
	"context"
	"encoding/json"

	"github.com/SigmaUno/subspace/internal/components"
)

func (m *MCP) info(ctx context.Context, tool string, args map[string]any) (json.RawMessage, error) {
	var out json.RawMessage
	err := m.Call(ctx, tool, args, &out)
	return out, err
}

func (m *MCP) GetForecast(ctx context.Context, briefID string) (json.RawMessage, error) {
	return m.info(ctx, "get_forecast", map[string]any{"brief_id": briefID})
}

func (m *MCP) GetReport(ctx context.Context, briefID string) (json.RawMessage, error) {
	return m.info(ctx, "get_report", map[string]any{"brief_id": briefID})
}

func (m *MCP) Search(ctx context.Context, query, kind string, limit int) (json.RawMessage, error) {
	args := map[string]any{"query": query}
	if kind != "" {
		args["kind"] = kind
	}
	if limit > 0 {
		args["limit"] = limit
	}
	return m.info(ctx, "search", args)
}

func (m *MCP) AgentStats(ctx context.Context, agentID string) (json.RawMessage, error) {
	args := map[string]any{}
	if agentID != "" {
		args["agent_id"] = agentID
	}
	return m.info(ctx, "agent_stats", args)
}

func (m *MCP) ListDecisions(ctx context.Context) (json.RawMessage, error) {
	return m.info(ctx, "list_decisions", map[string]any{})
}

func (m *MCP) ListSpawns(ctx context.Context, briefID string) (json.RawMessage, error) {
	args := map[string]any{}
	if briefID != "" {
		args["brief_id"] = briefID
	}
	return m.info(ctx, "list_spawns", args)
}

func (m *MCP) GetComponents(ctx context.Context, briefID string) (json.RawMessage, error) {
	return m.info(ctx, "get_components", map[string]any{"brief_id": briefID})
}

func (m *MCP) SetComponents(ctx context.Context, briefID string, specs []components.Spec) (json.RawMessage, error) {
	return m.info(ctx, "set_components", map[string]any{"brief_id": briefID, "components": specs})
}
