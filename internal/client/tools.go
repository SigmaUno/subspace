package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/usage"
)

type Instructions struct {
	Instructions string `json:"instructions"`
	Role         string `json:"role"`
	AgentID      string `json:"agent_id"`
}

type WhoAmI struct {
	Agent      *model.Agent `json:"agent"`
	Paused     bool         `json:"paused"`
	ServerTime time.Time    `json:"server_time"`
}

type UsageReport struct {
	Provider    string  `json:"provider,omitempty"`
	Model       string  `json:"model"`
	TokensIn    int64   `json:"tokens_in,omitempty"`
	TokensOut   int64   `json:"tokens_out,omitempty"`
	CacheRead   int64   `json:"cache_read,omitempty"`
	CacheWrite  int64   `json:"cache_write,omitempty"`
	CostUSD     float64 `json:"cost_usd,omitempty"`
	TaskID      string  `json:"task_id,omitempty"`
	BriefID     string  `json:"brief_id,omitempty"`
	RateLimited bool    `json:"rate_limited,omitempty"`
	RequestID   string  `json:"request_id,omitempty"`
}

func (m *MCP) task(ctx context.Context, tool string, args any) (*model.Task, error) {
	var out struct {
		Task *model.Task `json:"task"`
	}
	if err := m.Call(ctx, tool, args, &out); err != nil {
		return nil, err
	}
	return out.Task, nil
}

func (m *MCP) tasks(ctx context.Context, tool string, args any) ([]*model.Task, error) {
	var out struct {
		Tasks []*model.Task `json:"tasks"`
	}
	if err := m.Call(ctx, tool, args, &out); err != nil {
		return nil, err
	}
	return out.Tasks, nil
}

func (m *MCP) message(ctx context.Context, tool string, args any) (*model.Message, error) {
	var out struct {
		Message *model.Message `json:"message"`
	}
	if err := m.Call(ctx, tool, args, &out); err != nil {
		return nil, err
	}
	return out.Message, nil
}

func taskArgs(id string) map[string]any { return map[string]any{"task_id": id} }

func (m *MCP) GetInstructions(ctx context.Context) (*Instructions, error) {
	var out Instructions
	if err := m.Call(ctx, "get_instructions", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (m *MCP) WhoAmI(ctx context.Context) (*WhoAmI, error) {
	var out WhoAmI
	if err := m.Call(ctx, "whoami", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (m *MCP) ListMyTasks(ctx context.Context, statuses ...string) ([]*model.Task, error) {
	args := map[string]any{}
	if len(statuses) > 0 {
		args["status"] = statuses
	}
	return m.tasks(ctx, "list_my_tasks", args)
}

func (m *MCP) GetTask(ctx context.Context, taskID string) (*model.Task, error) {
	return m.task(ctx, "get_task", taskArgs(taskID))
}

func (m *MCP) Heartbeat(ctx context.Context, taskID string) (*board.HeartbeatResult, error) {
	args := map[string]any{}
	if taskID != "" {
		args["task_id"] = taskID
	}
	var out board.HeartbeatResult
	if err := m.Call(ctx, "heartbeat", args, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (m *MCP) UpdateStatus(ctx context.Context, in board.StatusInput) (*model.Task, error) {
	return m.task(ctx, "update_status", in)
}

func (m *MCP) AddNote(ctx context.Context, taskID, body string) (*model.Message, error) {
	args := map[string]any{"body": body}
	if taskID != "" {
		args["task_id"] = taskID
	}
	return m.message(ctx, "add_note", args)
}

func (m *MCP) AskOwner(ctx context.Context, taskID, question string, options ...string) (*model.Question, error) {
	args := map[string]any{"question": question}
	if taskID != "" {
		args["task_id"] = taskID
	}
	if len(options) > 0 {
		args["options"] = options
	}
	var out struct {
		Question *model.Question `json:"question"`
	}
	if err := m.Call(ctx, "ask_owner", args, &out); err != nil {
		return nil, err
	}
	return out.Question, nil
}

func (m *MCP) PostMessage(ctx context.Context, thread, body string) (*model.Message, error) {
	return m.message(ctx, "post_message", map[string]any{"thread": thread, "body": body})
}

func (m *MCP) ReadThread(ctx context.Context, thread string, since int64, limit int) ([]*model.Message, error) {
	args := map[string]any{"thread": thread}
	if since > 0 {
		args["since"] = since
	}
	if limit > 0 {
		args["limit"] = limit
	}
	var out struct {
		Messages []*model.Message `json:"messages"`
	}
	if err := m.Call(ctx, "read_thread", args, &out); err != nil {
		return nil, err
	}
	return out.Messages, nil
}

func (m *MCP) ClaimTask(ctx context.Context, taskID string) (*model.Task, error) {
	return m.task(ctx, "claim_task", taskArgs(taskID))
}

func (m *MCP) ReleaseTask(ctx context.Context, taskID, reason string) (*model.Task, error) {
	args := taskArgs(taskID)
	if reason != "" {
		args["reason"] = reason
	}
	return m.task(ctx, "release_task", args)
}

func (m *MCP) Submit(ctx context.Context, in board.SubmitInput) (*model.Task, error) {
	return m.task(ctx, "submit", in)
}

func (m *MCP) ReportTests(ctx context.Context, taskID string, passed, failed int, logExcerpt string) (*model.Task, error) {
	args := map[string]any{"task_id": taskID, "passed": passed, "failed": failed}
	if logExcerpt != "" {
		args["log_excerpt"] = logExcerpt
	}
	return m.task(ctx, "report_tests", args)
}

func (m *MCP) ListReviews(ctx context.Context) ([]*model.Task, error) {
	return m.tasks(ctx, "list_reviews", nil)
}

func (m *MCP) Review(ctx context.Context, taskID, verdict, comments string) (*model.Task, error) {
	return m.task(ctx, "review", map[string]any{"task_id": taskID, "verdict": verdict, "comments": comments})
}

func (m *MCP) GetBrief(ctx context.Context, briefID string) (*model.Brief, error) {
	var out struct {
		Brief *model.Brief `json:"brief"`
	}
	if err := m.Call(ctx, "get_brief", map[string]any{"brief_id": briefID}, &out); err != nil {
		return nil, err
	}
	return out.Brief, nil
}

func (m *MCP) ListBriefs(ctx context.Context, status string) ([]*model.Brief, error) {
	args := map[string]any{}
	if status != "" {
		args["status"] = status
	}
	var out struct {
		Briefs []*model.Brief `json:"briefs"`
	}
	if err := m.Call(ctx, "list_briefs", args, &out); err != nil {
		return nil, err
	}
	return out.Briefs, nil
}

func (m *MCP) ProposePlan(ctx context.Context, in board.PlanInput) (*model.Plan, error) {
	var out struct {
		Plan *model.Plan `json:"plan"`
	}
	if err := m.Call(ctx, "propose_plan", in, &out); err != nil {
		return nil, err
	}
	return out.Plan, nil
}

func (m *MCP) CreateTask(ctx context.Context, in board.TaskInput) (*model.Task, error) {
	return m.task(ctx, "create_task", in)
}

func (m *MCP) UpdateTask(ctx context.Context, taskID string, patch board.TaskPatch) (*model.Task, error) {
	return m.task(ctx, "update_task", struct {
		TaskID string `json:"task_id"`
		board.TaskPatch
	}{taskID, patch})
}

func (m *MCP) Assign(ctx context.Context, taskID, agentID, as string) (*model.Task, error) {
	args := map[string]any{"task_id": taskID, "agent_id": agentID}
	if as != "" {
		args["as"] = as
	}
	return m.task(ctx, "assign", args)
}

func (m *MCP) Reassign(ctx context.Context, taskID, agentID, reason string) (*model.Task, error) {
	args := map[string]any{"task_id": taskID, "agent_id": agentID}
	if reason != "" {
		args["reason"] = reason
	}
	return m.task(ctx, "reassign", args)
}

func (m *MCP) Board(ctx context.Context, briefID string) (map[string][]*model.Task, error) {
	args := map[string]any{}
	if briefID != "" {
		args["brief_id"] = briefID
	}
	var out struct {
		Columns map[string][]*model.Task `json:"columns"`
	}
	if err := m.Call(ctx, "board", args, &out); err != nil {
		return nil, err
	}
	return out.Columns, nil
}

func (m *MCP) ListAgents(ctx context.Context, role, status string) ([]*model.Agent, error) {
	args := map[string]any{}
	if role != "" {
		args["role"] = role
	}
	if status != "" {
		args["status"] = status
	}
	var out struct {
		Agents []*model.Agent `json:"agents"`
	}
	if err := m.Call(ctx, "list_agents", args, &out); err != nil {
		return nil, err
	}
	return out.Agents, nil
}

func (m *MCP) Capacity(ctx context.Context) (*board.CapacityView, error) {
	var out board.CapacityView
	if err := m.Call(ctx, "capacity", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (m *MCP) RequestSpawn(ctx context.Context, in board.SpawnInput) ([]*model.Spawn, error) {
	var out struct {
		Spawns []*model.Spawn `json:"spawns"`
	}
	if err := m.Call(ctx, "request_spawn", in, &out); err != nil {
		return nil, err
	}
	return out.Spawns, nil
}

func (m *MCP) ReportUsage(ctx context.Context, in UsageReport) (*usage.Record, error) {
	var out struct {
		Record *usage.Record `json:"record"`
	}
	if err := m.Call(ctx, "report_usage", in, &out); err != nil {
		return nil, err
	}
	return out.Record, nil
}

func (m *MCP) ListArtifacts(ctx context.Context, scope string, limit int) (json.RawMessage, error) {
	var out json.RawMessage
	err := m.Call(ctx, "list_artifacts", map[string]any{"scope": scope, "limit": limit}, &out)
	return out, err
}

func (m *MCP) GetArtifact(ctx context.Context, id string) (json.RawMessage, error) {
	var out json.RawMessage
	err := m.Call(ctx, "get_artifact", map[string]any{"id": id}, &out)
	return out, err
}

func (m *MCP) PutArtifact(ctx context.Context, scope, name, contentType string, content []byte) (json.RawMessage, error) {
	var out json.RawMessage
	err := m.Call(ctx, "put_artifact", map[string]any{"scope": scope, "name": name, "content_type": contentType, "content_base64": base64.StdEncoding.EncodeToString(content)}, &out)
	return out, err
}
