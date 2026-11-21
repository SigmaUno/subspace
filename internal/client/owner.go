package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/chat"
	"github.com/SigmaUno/subspace/internal/components"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/hillclimb"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/prompts"
	"github.com/SigmaUno/subspace/internal/store"
)

type Owner struct {
	cfg   config
	base  string
	token string
}

func NewOwner(baseURL, token string, opts ...Option) *Owner {
	return &Owner{cfg: newConfig(opts), base: strings.TrimRight(baseURL, "/"), token: token}
}

type Health struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Paused  bool   `json:"paused"`
}

type Backup struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

type Conversation = chat.Conversation

func (o *Owner) Do(ctx context.Context, op, method, path string, in, out any) error {
	start := time.Now()
	err := o.send(ctx, method, path, in, out)
	o.cfg.observe("rest:"+op, start, err)
	return err
}

func (o *Owner) send(ctx context.Context, method, path string, in, out any) error {
	req, err := newRequest(ctx, method, o.base+path, o.token, in)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := o.cfg.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := readBody(resp)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return httpError(resp.StatusCode, b)
	}
	return decodeInto(b, out, method+" "+path)
}

func call[T any](ctx context.Context, o *Owner, op, method, path string, in any) (*T, error) {
	var out T
	if err := o.Do(ctx, op, method, path, in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func items[T any](ctx context.Context, o *Owner, op, method, path string, in any, key string) ([]T, error) {
	var out map[string]json.RawMessage
	if err := o.Do(ctx, op, method, path, in, &out); err != nil {
		return nil, err
	}
	var v []T
	if err := decodeInto(out[key], &v, op+" "+key); err != nil {
		return nil, err
	}
	return v, nil
}

func query(path string, kv ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			v.Set(kv[i], kv[i+1])
		}
	}
	if len(v) == 0 {
		return path
	}
	return path + "?" + v.Encode()
}

func itoa(n int64) string {
	if n <= 0 {
		return ""
	}
	return strconv.FormatInt(n, 10)
}

func (o *Owner) Health(ctx context.Context) (*Health, error) {
	var out Health
	if err := o.send(ctx, http.MethodGet, "/healthz", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) State(ctx context.Context) (*board.StateInfo, error) {
	return call[board.StateInfo](ctx, o, "state", http.MethodGet, "/api/v1/state", nil)
}

func (o *Owner) PauseAll(ctx context.Context, reason string) error {
	return o.Do(ctx, "pause_all", http.MethodPost, "/api/v1/pause-all", map[string]string{"reason": reason}, nil)
}

func (o *Owner) ResumeAll(ctx context.Context) error {
	return o.Do(ctx, "resume_all", http.MethodPost, "/api/v1/resume-all", map[string]string{}, nil)
}

func (o *Owner) Charter(ctx context.Context) (*board.Charter, error) {
	return call[board.Charter](ctx, o, "get_charter", http.MethodGet, "/api/v1/charter", nil)
}

func (o *Owner) SetCharter(ctx context.Context, text string) (*board.Charter, error) {
	return call[board.Charter](ctx, o, "set_charter", http.MethodPut, "/api/v1/charter", map[string]string{"text": text})
}

func (o *Owner) Backup(ctx context.Context) (*Backup, error) {
	return call[Backup](ctx, o, "backup", http.MethodPost, "/api/v1/backup", map[string]string{})
}

func (o *Owner) CreateBrief(ctx context.Context, in board.BriefInput) (*model.Brief, error) {
	return call[model.Brief](ctx, o, "create_brief", http.MethodPost, "/api/v1/briefs", in)
}

func (o *Owner) ListBriefs(ctx context.Context, status string) ([]*model.Brief, error) {
	return items[*model.Brief](ctx, o, "list_briefs", http.MethodGet, query("/api/v1/briefs", "status", status), nil, "briefs")
}

func (o *Owner) GetBrief(ctx context.Context, id string) (*model.Brief, error) {
	return call[model.Brief](ctx, o, "get_brief", http.MethodGet, "/api/v1/briefs/"+url.PathEscape(id), nil)
}

func (o *Owner) UpdateBrief(ctx context.Context, id string, patch board.BriefPatch) (*model.Brief, error) {
	return call[model.Brief](ctx, o, "update_brief", http.MethodPatch, "/api/v1/briefs/"+url.PathEscape(id), patch)
}

func (o *Owner) Board(ctx context.Context, briefID string) (*board.BoardView, error) {
	return call[board.BoardView](ctx, o, "board", http.MethodGet, "/api/v1/briefs/"+url.PathEscape(briefID)+"/board", nil)
}

func (o *Owner) Architecture(ctx context.Context, briefID string) (*components.Graph, error) {
	return call[components.Graph](ctx, o, "architecture", http.MethodGet, "/api/v1/briefs/"+url.PathEscape(briefID)+"/architecture", nil)
}

func (o *Owner) SetComponents(ctx context.Context, briefID string, specs []components.Spec) ([]components.Component, error) {
	in := map[string]any{"components": specs}
	return items[components.Component](ctx, o, "set_components", http.MethodPut, "/api/v1/briefs/"+url.PathEscape(briefID)+"/components", in, "components")
}

func (o *Owner) ListPlans(ctx context.Context, briefID, status string) ([]*model.Plan, error) {
	return items[*model.Plan](ctx, o, "list_plans", http.MethodGet, query("/api/v1/plans", "brief_id", briefID, "status", status), nil, "plans")
}

func (o *Owner) GetPlan(ctx context.Context, id string) (*model.Plan, error) {
	return call[model.Plan](ctx, o, "get_plan", http.MethodGet, "/api/v1/plans/"+url.PathEscape(id), nil)
}

func (o *Owner) ApprovePlan(ctx context.Context, id, note string) (*model.Plan, error) {
	return call[model.Plan](ctx, o, "approve_plan", http.MethodPost, "/api/v1/plans/"+url.PathEscape(id)+"/approve", map[string]string{"note": note})
}

func (o *Owner) RejectPlan(ctx context.Context, id, reason string) (*model.Plan, error) {
	return call[model.Plan](ctx, o, "reject_plan", http.MethodPost, "/api/v1/plans/"+url.PathEscape(id)+"/reject", map[string]string{"reason": reason})
}

func (o *Owner) ListTasks(ctx context.Context, q board.TaskQuery) ([]*model.Task, error) {
	path := query("/api/v1/tasks", "brief_id", q.BriefID, "status", q.Status, "assignee", q.Assignee, "role", q.Role, "limit", itoa(int64(q.Limit)))
	return items[*model.Task](ctx, o, "list_tasks", http.MethodGet, path, nil, "tasks")
}

func (o *Owner) task(ctx context.Context, op, method, id, action string, in any) (*model.Task, error) {
	path := "/api/v1/tasks/" + url.PathEscape(id)
	if action != "" {
		path += "/" + action
	}
	return call[model.Task](ctx, o, op, method, path, in)
}

func (o *Owner) GetTask(ctx context.Context, id string) (*model.Task, error) {
	return o.task(ctx, "get_task", http.MethodGet, id, "", nil)
}

func (o *Owner) SetTaskBudget(ctx context.Context, id string, b model.Budget) (*model.Task, error) {
	return o.task(ctx, "set_task_budget", http.MethodPatch, id, "", map[string]any{"budget": b})
}

func (o *Owner) MarkDone(ctx context.Context, id, note string) (*model.Task, error) {
	return o.task(ctx, "mark_done", http.MethodPost, id, "done", map[string]string{"note": note})
}

func (o *Owner) CancelTask(ctx context.Context, id, reason string) (*model.Task, error) {
	return o.task(ctx, "cancel_task", http.MethodPost, id, "cancel", map[string]string{"reason": reason})
}

func (o *Owner) ReopenTask(ctx context.Context, id, status, note string) (*model.Task, error) {
	return o.task(ctx, "reopen_task", http.MethodPost, id, "reopen", map[string]string{"status": status, "note": note})
}

func (o *Owner) ListAgents(ctx context.Context, role, status string) ([]*model.Agent, error) {
	return items[*model.Agent](ctx, o, "list_agents", http.MethodGet, query("/api/v1/agents", "role", role, "status", status), nil, "agents")
}

func (o *Owner) GetAgent(ctx context.Context, id string) (*model.Agent, error) {
	return call[model.Agent](ctx, o, "get_agent", http.MethodGet, "/api/v1/agents/"+url.PathEscape(id), nil)
}

func (o *Owner) RegisterAgent(ctx context.Context, in board.AgentInput) (*board.Registered, error) {
	return call[board.Registered](ctx, o, "register_agent", http.MethodPost, "/api/v1/agents", in)
}

func (o *Owner) RegisterBulk(ctx context.Context, in board.BulkInput) ([]*board.Registered, error) {
	return items[*board.Registered](ctx, o, "register_bulk", http.MethodPost, "/api/v1/agents/bulk", in, "agents")
}

func (o *Owner) Connect(ctx context.Context, id string) (*board.Connect, error) {
	return call[board.Connect](ctx, o, "connect", http.MethodGet, "/api/v1/agents/"+url.PathEscape(id)+"/connect", nil)
}

func (o *Owner) RotateToken(ctx context.Context, id string) (*board.Registered, error) {
	return call[board.Registered](ctx, o, "rotate_token", http.MethodPost, "/api/v1/agents/"+url.PathEscape(id)+"/rotate-token", map[string]string{})
}

func (o *Owner) RevokeAgent(ctx context.Context, id, reason string) (*model.Agent, error) {
	return call[model.Agent](ctx, o, "revoke_agent", http.MethodPost, "/api/v1/agents/"+url.PathEscape(id)+"/revoke", map[string]string{"reason": reason})
}

func (o *Owner) ResumeAgent(ctx context.Context, id string) (*model.Agent, error) {
	return call[model.Agent](ctx, o, "resume_agent", http.MethodPost, "/api/v1/agents/"+url.PathEscape(id)+"/resume", map[string]string{})
}

func (o *Owner) ListQuestions(ctx context.Context, status string) ([]*model.Question, error) {
	return items[*model.Question](ctx, o, "list_questions", http.MethodGet, query("/api/v1/questions", "status", status), nil, "questions")
}

func (o *Owner) AnswerQuestion(ctx context.Context, id, answer string) (*model.Question, error) {
	return call[model.Question](ctx, o, "answer_question", http.MethodPost, "/api/v1/questions/"+url.PathEscape(id)+"/answer", map[string]string{"answer": answer})
}

func threadPath(kind, id string) string {
	return "/api/v1/threads/" + url.PathEscape(kind) + "/" + url.PathEscape(id) + "/messages"
}

func (o *Owner) ThreadMessages(ctx context.Context, kind, id string, since int64, limit int) ([]*model.Message, error) {
	path := query(threadPath(kind, id), "since", itoa(since), "limit", itoa(int64(limit)))
	return items[*model.Message](ctx, o, "read_thread", http.MethodGet, path, nil, "messages")
}

func (o *Owner) PostThreadMessage(ctx context.Context, kind, id, body string) (*model.Message, error) {
	return call[model.Message](ctx, o, "post_thread_message", http.MethodPost, threadPath(kind, id), map[string]string{"body": body})
}

func (o *Owner) Capacity(ctx context.Context) (*board.CapacityView, error) {
	return call[board.CapacityView](ctx, o, "capacity", http.MethodGet, "/api/v1/capacity", nil)
}

func (o *Owner) SetCapacity(ctx context.Context, cfg capacity.Config) (*board.CapacityView, error) {
	return call[board.CapacityView](ctx, o, "set_capacity", http.MethodPut, "/api/v1/capacity", cfg)
}

func (o *Owner) ListSpawns(ctx context.Context, briefID string) ([]*model.Spawn, error) {
	return items[*model.Spawn](ctx, o, "list_spawns", http.MethodGet, query("/api/v1/spawns", "brief_id", briefID), nil, "spawns")
}

func (o *Owner) Audit(ctx context.Context, f store.AuditFilter) ([]*model.AuditEvent, error) {
	path := query("/api/v1/audit", "agent_id", f.AgentID, "task_id", f.TaskID, "tool", f.Tool, "since", itoa(f.SinceID), "limit", itoa(int64(f.Limit)))
	return items[*model.AuditEvent](ctx, o, "audit", http.MethodGet, path, nil, "events")
}

func (o *Owner) Decisions(ctx context.Context, limit int) ([]*decisions.Decision, error) {
	return items[*decisions.Decision](ctx, o, "list_decisions", http.MethodGet, query("/api/v1/decisions", "limit", itoa(int64(limit))), nil, "decisions")
}

func (o *Owner) NextDecision(ctx context.Context) (*decisions.Decision, error) {
	var out struct {
		Decision *decisions.Decision `json:"decision"`
	}
	if err := o.Do(ctx, "next_decision", http.MethodGet, "/api/v1/decisions/next", nil, &out); err != nil {
		return nil, err
	}
	return out.Decision, nil
}

type DecisionAction struct {
	Action string `json:"action"`
	Answer string `json:"answer,omitempty"`
	Note   string `json:"note,omitempty"`
}

func (o *Owner) ActOnDecision(ctx context.Context, id string, a DecisionAction) (*decisions.Outcome, error) {
	return call[decisions.Outcome](ctx, o, "act_decision", http.MethodPost, "/api/v1/decisions/"+url.PathEscape(id), a)
}

func (o *Owner) SendMessage(ctx context.Context, to, body string) (*Conversation, error) {
	return call[Conversation](ctx, o, "send_message", http.MethodPost, "/api/v1/messages", chat.Message{To: to, Body: body})
}

func (o *Owner) Conversations(ctx context.Context) ([]Conversation, error) {
	return items[Conversation](ctx, o, "list_conversations", http.MethodGet, "/api/v1/conversations", nil, "conversations")
}

func (o *Owner) GetConversation(ctx context.Context, id string) (*Conversation, error) {
	return call[Conversation](ctx, o, "get_conversation", http.MethodGet, "/api/v1/conversations/"+url.PathEscape(id), nil)
}

func (o *Owner) Prompts(ctx context.Context, tag string) ([]prompts.Template, error) {
	return items[prompts.Template](ctx, o, "list_prompts", http.MethodGet, query("/api/v1/prompts", "tag", tag), nil, "prompts")
}

func (o *Owner) GetPrompt(ctx context.Context, id string) (*prompts.Template, error) {
	return call[prompts.Template](ctx, o, "get_prompt", http.MethodGet, "/api/v1/prompts/"+url.PathEscape(id), nil)
}

func (o *Owner) RenderPrompt(ctx context.Context, id string, vars map[string]string) (string, error) {
	v := url.Values{}
	for k, val := range vars {
		v.Set(k, val)
	}
	path := "/api/v1/prompts/" + url.PathEscape(id) + "/render"
	if len(v) > 0 {
		path += "?" + v.Encode()
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := o.Do(ctx, "render_prompt", http.MethodGet, path, nil, &out); err != nil {
		return "", err
	}
	return out.Text, nil
}

type HillclimbJobInput struct {
	BriefID   string   `json:"brief_id"`
	Repo      string   `json:"repo"`
	MetricCmd string   `json:"metric_cmd"`
	Direction string   `json:"direction"`
	Target    *float64 `json:"target,omitempty"`
	Budget    int      `json:"budget,omitempty"`
	PlateauN  int      `json:"plateau_n,omitempty"`
}

type HillclimbSeries struct {
	Job    *hillclimb.Job    `json:"job"`
	Points []hillclimb.Point `json:"points"`
}

func (o *Owner) HillclimbJobs(ctx context.Context) ([]*hillclimb.Job, error) {
	return items[*hillclimb.Job](ctx, o, "list_hillclimb_jobs", http.MethodGet, "/api/v1/hillclimb/jobs", nil, "jobs")
}

func (o *Owner) CreateHillclimbJob(ctx context.Context, in HillclimbJobInput) (*hillclimb.Job, error) {
	return call[hillclimb.Job](ctx, o, "create_hillclimb_job", http.MethodPost, "/api/v1/hillclimb/jobs", in)
}

func (o *Owner) HillclimbSeries(ctx context.Context, id string) (*HillclimbSeries, error) {
	return call[HillclimbSeries](ctx, o, "hillclimb_series", http.MethodGet, "/api/v1/hillclimb/jobs/"+url.PathEscape(id)+"/series", nil)
}
