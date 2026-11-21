package client

import (
	"context"
	"errors"
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
	safe := method == http.MethodGet || method == http.MethodHead
	for attempt := 0; ; attempt++ {
		b, status, err := o.roundTrip(ctx, method, path, in)
		if err == nil {
			if status >= 300 {
				return httpError(status, b)
			}
			return decodeInto(b, out, method+" "+path)
		}
		var te *TransportError
		if !safe || !errors.As(err, &te) || attempt >= o.cfg.retries || ctx.Err() != nil {
			return err
		}
		if o.cfg.sleep(ctx, min(100*time.Millisecond<<attempt, 5*time.Second)) != nil {
			return err
		}
	}
}

func (o *Owner) roundTrip(ctx context.Context, method, path string, in any) ([]byte, int, error) {
	req, err := newRequest(ctx, method, o.base+path, o.token, in)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := o.cfg.hc.Do(req)
	if err != nil {
		return nil, 0, &TransportError{Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := readBody(resp)
	if err != nil {
		return nil, 0, &TransportError{Err: err}
	}
	return b, resp.StatusCode, nil
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
	var out board.StateInfo
	if err := o.Do(ctx, "state", http.MethodGet, "/api/v1/state", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) PauseAll(ctx context.Context, reason string) error {
	return o.Do(ctx, "pause_all", http.MethodPost, "/api/v1/pause-all", map[string]string{"reason": reason}, nil)
}

func (o *Owner) ResumeAll(ctx context.Context) error {
	return o.Do(ctx, "resume_all", http.MethodPost, "/api/v1/resume-all", map[string]string{}, nil)
}

func (o *Owner) Charter(ctx context.Context) (*board.Charter, error) {
	var out board.Charter
	if err := o.Do(ctx, "get_charter", http.MethodGet, "/api/v1/charter", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) SetCharter(ctx context.Context, text string) (*board.Charter, error) {
	var out board.Charter
	if err := o.Do(ctx, "set_charter", http.MethodPut, "/api/v1/charter", map[string]string{"text": text}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) Backup(ctx context.Context) (*Backup, error) {
	var out Backup
	if err := o.Do(ctx, "backup", http.MethodPost, "/api/v1/backup", map[string]string{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) CreateBrief(ctx context.Context, in board.BriefInput) (*model.Brief, error) {
	var out model.Brief
	if err := o.Do(ctx, "create_brief", http.MethodPost, "/api/v1/briefs", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) ListBriefs(ctx context.Context, status string) ([]*model.Brief, error) {
	var out struct {
		Briefs []*model.Brief `json:"briefs"`
	}
	if err := o.Do(ctx, "list_briefs", http.MethodGet, query("/api/v1/briefs", "status", status), nil, &out); err != nil {
		return nil, err
	}
	return out.Briefs, nil
}

func (o *Owner) GetBrief(ctx context.Context, id string) (*model.Brief, error) {
	var out model.Brief
	if err := o.Do(ctx, "get_brief", http.MethodGet, "/api/v1/briefs/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) UpdateBrief(ctx context.Context, id string, patch board.BriefPatch) (*model.Brief, error) {
	var out model.Brief
	if err := o.Do(ctx, "update_brief", http.MethodPatch, "/api/v1/briefs/"+url.PathEscape(id), patch, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) Board(ctx context.Context, briefID string) (*board.BoardView, error) {
	var out board.BoardView
	if err := o.Do(ctx, "board", http.MethodGet, "/api/v1/briefs/"+url.PathEscape(briefID)+"/board", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) Architecture(ctx context.Context, briefID string) (*components.Graph, error) {
	var out components.Graph
	if err := o.Do(ctx, "architecture", http.MethodGet, "/api/v1/briefs/"+url.PathEscape(briefID)+"/architecture", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) SetComponents(ctx context.Context, briefID string, specs []components.Spec) ([]components.Component, error) {
	var out struct {
		Components []components.Component `json:"components"`
	}
	in := map[string]any{"components": specs}
	if err := o.Do(ctx, "set_components", http.MethodPut, "/api/v1/briefs/"+url.PathEscape(briefID)+"/components", in, &out); err != nil {
		return nil, err
	}
	return out.Components, nil
}

func (o *Owner) ListPlans(ctx context.Context, briefID, status string) ([]*model.Plan, error) {
	var out struct {
		Plans []*model.Plan `json:"plans"`
	}
	if err := o.Do(ctx, "list_plans", http.MethodGet, query("/api/v1/plans", "brief_id", briefID, "status", status), nil, &out); err != nil {
		return nil, err
	}
	return out.Plans, nil
}

func (o *Owner) GetPlan(ctx context.Context, id string) (*model.Plan, error) {
	var out model.Plan
	if err := o.Do(ctx, "get_plan", http.MethodGet, "/api/v1/plans/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) ApprovePlan(ctx context.Context, id, note string) (*model.Plan, error) {
	var out model.Plan
	if err := o.Do(ctx, "approve_plan", http.MethodPost, "/api/v1/plans/"+url.PathEscape(id)+"/approve", map[string]string{"note": note}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) RejectPlan(ctx context.Context, id, reason string) (*model.Plan, error) {
	var out model.Plan
	if err := o.Do(ctx, "reject_plan", http.MethodPost, "/api/v1/plans/"+url.PathEscape(id)+"/reject", map[string]string{"reason": reason}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) ListTasks(ctx context.Context, q board.TaskQuery) ([]*model.Task, error) {
	var out struct {
		Tasks []*model.Task `json:"tasks"`
	}
	path := query("/api/v1/tasks", "brief_id", q.BriefID, "status", q.Status, "assignee", q.Assignee, "role", q.Role, "limit", itoa(int64(q.Limit)))
	if err := o.Do(ctx, "list_tasks", http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Tasks, nil
}

func (o *Owner) task(ctx context.Context, op, method, id, action string, in any) (*model.Task, error) {
	path := "/api/v1/tasks/" + url.PathEscape(id)
	if action != "" {
		path += "/" + action
	}
	var out model.Task
	if err := o.Do(ctx, op, method, path, in, &out); err != nil {
		return nil, err
	}
	return &out, nil
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
	var out struct {
		Agents []*model.Agent `json:"agents"`
	}
	if err := o.Do(ctx, "list_agents", http.MethodGet, query("/api/v1/agents", "role", role, "status", status), nil, &out); err != nil {
		return nil, err
	}
	return out.Agents, nil
}

func (o *Owner) GetAgent(ctx context.Context, id string) (*model.Agent, error) {
	var out model.Agent
	if err := o.Do(ctx, "get_agent", http.MethodGet, "/api/v1/agents/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) RegisterAgent(ctx context.Context, in board.AgentInput) (*board.Registered, error) {
	var out board.Registered
	if err := o.Do(ctx, "register_agent", http.MethodPost, "/api/v1/agents", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) RegisterBulk(ctx context.Context, in board.BulkInput) ([]*board.Registered, error) {
	var out struct {
		Agents []*board.Registered `json:"agents"`
	}
	if err := o.Do(ctx, "register_bulk", http.MethodPost, "/api/v1/agents/bulk", in, &out); err != nil {
		return nil, err
	}
	return out.Agents, nil
}

func (o *Owner) Connect(ctx context.Context, id string) (*board.Connect, error) {
	var out board.Connect
	if err := o.Do(ctx, "connect", http.MethodGet, "/api/v1/agents/"+url.PathEscape(id)+"/connect", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) RotateToken(ctx context.Context, id string) (*board.Registered, error) {
	var out board.Registered
	if err := o.Do(ctx, "rotate_token", http.MethodPost, "/api/v1/agents/"+url.PathEscape(id)+"/rotate-token", map[string]string{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) RevokeAgent(ctx context.Context, id, reason string) (*model.Agent, error) {
	var out model.Agent
	if err := o.Do(ctx, "revoke_agent", http.MethodPost, "/api/v1/agents/"+url.PathEscape(id)+"/revoke", map[string]string{"reason": reason}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) ResumeAgent(ctx context.Context, id string) (*model.Agent, error) {
	var out model.Agent
	if err := o.Do(ctx, "resume_agent", http.MethodPost, "/api/v1/agents/"+url.PathEscape(id)+"/resume", map[string]string{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) ListQuestions(ctx context.Context, status string) ([]*model.Question, error) {
	var out struct {
		Questions []*model.Question `json:"questions"`
	}
	if err := o.Do(ctx, "list_questions", http.MethodGet, query("/api/v1/questions", "status", status), nil, &out); err != nil {
		return nil, err
	}
	return out.Questions, nil
}

func (o *Owner) AnswerQuestion(ctx context.Context, id, answer string) (*model.Question, error) {
	var out model.Question
	if err := o.Do(ctx, "answer_question", http.MethodPost, "/api/v1/questions/"+url.PathEscape(id)+"/answer", map[string]string{"answer": answer}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func threadPath(kind, id string) string {
	return "/api/v1/threads/" + url.PathEscape(kind) + "/" + url.PathEscape(id) + "/messages"
}

func (o *Owner) ThreadMessages(ctx context.Context, kind, id string, since int64, limit int) ([]*model.Message, error) {
	var out struct {
		Messages []*model.Message `json:"messages"`
	}
	path := query(threadPath(kind, id), "since", itoa(since), "limit", itoa(int64(limit)))
	if err := o.Do(ctx, "read_thread", http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Messages, nil
}

func (o *Owner) PostThreadMessage(ctx context.Context, kind, id, body string) (*model.Message, error) {
	var out model.Message
	if err := o.Do(ctx, "post_thread_message", http.MethodPost, threadPath(kind, id), map[string]string{"body": body}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) Capacity(ctx context.Context) (*board.CapacityView, error) {
	var out board.CapacityView
	if err := o.Do(ctx, "capacity", http.MethodGet, "/api/v1/capacity", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) SetCapacity(ctx context.Context, cfg capacity.Config) (*board.CapacityView, error) {
	var out board.CapacityView
	if err := o.Do(ctx, "set_capacity", http.MethodPut, "/api/v1/capacity", cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) ListSpawns(ctx context.Context, briefID string) ([]*model.Spawn, error) {
	var out struct {
		Spawns []*model.Spawn `json:"spawns"`
	}
	if err := o.Do(ctx, "list_spawns", http.MethodGet, query("/api/v1/spawns", "brief_id", briefID), nil, &out); err != nil {
		return nil, err
	}
	return out.Spawns, nil
}

func (o *Owner) Audit(ctx context.Context, f store.AuditFilter) ([]*model.AuditEvent, error) {
	var out struct {
		Events []*model.AuditEvent `json:"events"`
	}
	path := query("/api/v1/audit", "agent_id", f.AgentID, "task_id", f.TaskID, "tool", f.Tool, "since", itoa(f.SinceID), "limit", itoa(int64(f.Limit)))
	if err := o.Do(ctx, "audit", http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Events, nil
}

func (o *Owner) Decisions(ctx context.Context, limit int) ([]*decisions.Decision, error) {
	var out struct {
		Decisions []*decisions.Decision `json:"decisions"`
	}
	if err := o.Do(ctx, "list_decisions", http.MethodGet, query("/api/v1/decisions", "limit", itoa(int64(limit))), nil, &out); err != nil {
		return nil, err
	}
	return out.Decisions, nil
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
	var out decisions.Outcome
	if err := o.Do(ctx, "act_decision", http.MethodPost, "/api/v1/decisions/"+url.PathEscape(id), a, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) SendMessage(ctx context.Context, to, body string) (*Conversation, error) {
	var out Conversation
	if err := o.Do(ctx, "send_message", http.MethodPost, "/api/v1/messages", chat.Message{To: to, Body: body}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) Conversations(ctx context.Context) ([]Conversation, error) {
	var out struct {
		Conversations []Conversation `json:"conversations"`
	}
	if err := o.Do(ctx, "list_conversations", http.MethodGet, "/api/v1/conversations", nil, &out); err != nil {
		return nil, err
	}
	return out.Conversations, nil
}

func (o *Owner) GetConversation(ctx context.Context, id string) (*Conversation, error) {
	var out Conversation
	if err := o.Do(ctx, "get_conversation", http.MethodGet, "/api/v1/conversations/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) Prompts(ctx context.Context, tag string) ([]prompts.Template, error) {
	var out struct {
		Prompts []prompts.Template `json:"prompts"`
	}
	if err := o.Do(ctx, "list_prompts", http.MethodGet, query("/api/v1/prompts", "tag", tag), nil, &out); err != nil {
		return nil, err
	}
	return out.Prompts, nil
}

func (o *Owner) GetPrompt(ctx context.Context, id string) (*prompts.Template, error) {
	var out prompts.Template
	if err := o.Do(ctx, "get_prompt", http.MethodGet, "/api/v1/prompts/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
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
	var out struct {
		Jobs []*hillclimb.Job `json:"jobs"`
	}
	if err := o.Do(ctx, "list_hillclimb_jobs", http.MethodGet, "/api/v1/hillclimb/jobs", nil, &out); err != nil {
		return nil, err
	}
	return out.Jobs, nil
}

func (o *Owner) CreateHillclimbJob(ctx context.Context, in HillclimbJobInput) (*hillclimb.Job, error) {
	var out hillclimb.Job
	if err := o.Do(ctx, "create_hillclimb_job", http.MethodPost, "/api/v1/hillclimb/jobs", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (o *Owner) HillclimbSeries(ctx context.Context, id string) (*HillclimbSeries, error) {
	var out HillclimbSeries
	if err := o.Do(ctx, "hillclimb_series", http.MethodGet, "/api/v1/hillclimb/jobs/"+url.PathEscape(id)+"/series", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
