package client

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/SigmaUno/subspace/internal/board"
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

func (o *Owner) TasksPage(ctx context.Context, q board.TaskQuery) (*Page[*model.Task], error) {
	path := query("/api/v1/tasks", "brief_id", q.BriefID, "status", q.Status, "assignee", q.Assignee, "role", q.Role, "cursor", q.Cursor, "limit", itoa(int64(q.Limit)))
	return getPage[*model.Task](ctx, o, "list_tasks", path, "tasks")
}

func (o *Owner) AgentsPage(ctx context.Context, role, status, cursor string, limit int) (*Page[*model.Agent], error) {
	path := query("/api/v1/agents", "role", role, "status", status, "cursor", cursor, "limit", itoa(int64(limit)))
	return getPage[*model.Agent](ctx, o, "list_agents", path, "agents")
}

func (o *Owner) BriefsPage(ctx context.Context, status, cursor string, limit int) (*Page[*model.Brief], error) {
	path := query("/api/v1/briefs", "status", status, "cursor", cursor, "limit", itoa(int64(limit)))
	return getPage[*model.Brief](ctx, o, "list_briefs", path, "briefs")
}

func (o *Owner) PlansPage(ctx context.Context, briefID, status, cursor string, limit int) (*Page[*model.Plan], error) {
	path := query("/api/v1/plans", "brief_id", briefID, "status", status, "cursor", cursor, "limit", itoa(int64(limit)))
	return getPage[*model.Plan](ctx, o, "list_plans", path, "plans")
}

func (o *Owner) QuestionsPage(ctx context.Context, status, cursor string, limit int) (*Page[*model.Question], error) {
	path := query("/api/v1/questions", "status", status, "cursor", cursor, "limit", itoa(int64(limit)))
	return getPage[*model.Question](ctx, o, "list_questions", path, "questions")
}

func (o *Owner) SpawnsPage(ctx context.Context, briefID, cursor string, limit int) (*Page[*model.Spawn], error) {
	path := query("/api/v1/spawns", "brief_id", briefID, "cursor", cursor, "limit", itoa(int64(limit)))
	return getPage[*model.Spawn](ctx, o, "list_spawns", path, "spawns")
}

func (o *Owner) AuditPage(ctx context.Context, f store.AuditFilter, cursor string) (*Page[*model.AuditEvent], error) {
	path := query("/api/v1/audit", "agent_id", f.AgentID, "task_id", f.TaskID, "tool", f.Tool, "since", itoa(f.SinceID), "cursor", cursor, "limit", itoa(int64(f.Limit)))
	return getPage[*model.AuditEvent](ctx, o, "audit", path, "events")
}

func (o *Owner) ThreadMessagesPage(ctx context.Context, kind, id string, since int64, cursor string, limit int) (*Page[*model.Message], error) {
	path := query(threadPath(kind, id), "since", itoa(since), "cursor", cursor, "limit", itoa(int64(limit)))
	return getPage[*model.Message](ctx, o, "read_thread", path, "messages")
}
