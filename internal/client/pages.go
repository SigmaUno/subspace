package client

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

type Page[T any] struct {
	Items      []T
	NextCursor string
}

func getPage[T any](ctx context.Context, o *Owner, op, path, key string) (*Page[T], error) {
	var raw map[string]json.RawMessage
	if err := o.Do(ctx, op, http.MethodGet, path, nil, &raw); err != nil {
		return nil, err
	}
	var p Page[T]
	if err := json.Unmarshal(raw[key], &p.Items); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw["next_cursor"], &p.NextCursor); err != nil && raw["next_cursor"] != nil {
		return nil, err
	}
	return &p, nil
}

func (o *Owner) AgentsPage(ctx context.Context, role, status, cursor string, limit int) (*Page[*model.Agent], error) {
	path := query("/api/v1/agents", "role", role, "status", status, "cursor", cursor, "limit", itoa(int64(limit)))
	return getPage[*model.Agent](ctx, o, "list_agents", path, "agents")
}

func (o *Owner) AuditPage(ctx context.Context, f store.AuditFilter, cursor string) (*Page[*model.AuditEvent], error) {
	path := query("/api/v1/audit", "agent_id", f.AgentID, "task_id", f.TaskID, "tool", f.Tool, "since", itoa(f.SinceID), "cursor", cursor, "limit", itoa(int64(f.Limit)))
	return getPage[*model.AuditEvent](ctx, o, "audit", path, "events")
}
