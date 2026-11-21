package dst

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/estimate"
	"github.com/SigmaUno/subspace/internal/forecast"
	"github.com/SigmaUno/subspace/internal/model"
)

type Clock struct{ ms atomic.Int64 }

func NewClock(t time.Time) *Clock {
	c := &Clock{}
	c.ms.Store(t.UnixMilli())
	return c
}

func (c *Clock) Now() time.Time { return time.UnixMilli(c.ms.Load()).UTC() }

func (c *Clock) Advance(d time.Duration) { c.ms.Add(d.Milliseconds()) }

type Kind string

const (
	OpAdvance        Kind = "advance"
	OpSweep          Kind = "sweep"
	OpFlush          Kind = "flush"
	OpRestart        Kind = "restart"
	OpBatch          Kind = "batch"
	OpSubscribe      Kind = "subscribe"
	OpUnsubscribe    Kind = "unsubscribe"
	OpDrain          Kind = "drain"
	OpApprove        Kind = "approve_plan"
	OpReject         Kind = "reject_plan"
	OpPauseAll       Kind = "pause_all"
	OpResumeAll      Kind = "resume_all"
	OpMarkDone       Kind = "mark_done"
	OpCancel         Kind = "cancel"
	OpReopen         Kind = "reopen"
	OpAnswer         Kind = "answer_question"
	OpRotate         Kind = "rotate_token"
	OpRegister       Kind = "register_agent"
	OpBulk           Kind = "register_bulk"
	OpSetCapacity    Kind = "set_capacity"
	OpPropose        Kind = "propose_plan"
	OpAssign         Kind = "assign"
	OpAssignReviewer Kind = "assign_reviewer"
	OpReassign       Kind = "reassign"
	OpSpawn          Kind = "request_spawn"
	OpClaim          Kind = "claim_task"
	OpHeartbeat      Kind = "heartbeat"
	OpUpdate         Kind = "update_status"
	OpBlock          Kind = "block"
	OpSubmit         Kind = "submit"
	OpRelease        Kind = "release_task"
	OpReview         Kind = "review"
	OpAskOwner       Kind = "ask_owner"
	OpReportTests    Kind = "report_tests"
	OpEnqueue        Kind = "mq_enqueue"
	OpMergeTick      Kind = "mq_tick"
	OpAutopilotTick  Kind = "autopilot_tick"
	OpWebhook        Kind = "webhook"
	OpWebhookRace    Kind = "webhook_race"
	OpInstrSave      Kind = "instructions_save"
	OpInstrRollback  Kind = "instructions_rollback"
	OpSearch         Kind = "search"
	OpHandoff        Kind = "set_brief_leader"
	OpSetQuota       Kind = "set_agent_quota"
	OpAnnounce       Kind = "publish_announcement"
	OpUnannounce     Kind = "delete_announcement"
	OpInstructions   Kind = "get_instructions"
	OpGetTask        Kind = "get_task"
	OpLeaderRead     Kind = "leader_read"
	OpRequestHelp    Kind = "request_help"
	OpSuggestSplit   Kind = "suggest_split"
	OpAcceptSplit    Kind = "accept_split"
	OpAddKnowledge   Kind = "add_knowledge"
	OpModerate       Kind = "moderate_knowledge"
	OpReadKnowledge  Kind = "read_knowledge"
	OpEstimate       Kind = "estimate_forecast"
	OpIncidentStart  Kind = "incident_start"
	OpIncidentEnd    Kind = "incident_end"
	OpResumeAgent    Kind = "resume_agent"
	OpSchedule       Kind = "create_schedule"
	OpScheduleTick   Kind = "schedule_tick"
	OpRetention      Kind = "retention_run"
	OpRestore        Kind = "retention_restore"
	OpSetGates       Kind = "set_quality_gates"
	OpOverride       Kind = "override_gates"
)

type Op struct {
	Kind    Kind          `json:"kind"`
	Actor   string        `json:"actor,omitempty"`
	Task    string        `json:"task,omitempty"`
	Target  string        `json:"target,omitempty"`
	Dur     time.Duration `json:"dur,omitempty"`
	Pct     int           `json:"pct,omitempty"`
	Cost    float64       `json:"cost,omitempty"`
	Approve bool          `json:"approve,omitempty"`
	N       int           `json:"n,omitempty"`
	Limit   int           `json:"limit,omitempty"`
	Token   int           `json:"token,omitempty"`
	Crash   bool          `json:"crash,omitempty"`
	Text    string        `json:"text,omitempty"`
	Forged  bool          `json:"forged,omitempty"`
	Sub     []Op          `json:"sub,omitempty"`
}

func (o Op) String() string {
	var b strings.Builder
	b.WriteString(string(o.Kind))
	if o.Actor != "" {
		fmt.Fprintf(&b, " by=%s", o.Actor)
		if o.Token > 0 {
			fmt.Fprintf(&b, "#%d", o.Token)
		}
	}
	if o.Task != "" {
		fmt.Fprintf(&b, " task=%s", o.Task)
	}
	if o.Target != "" {
		fmt.Fprintf(&b, " target=%s", o.Target)
	}
	switch o.Kind {
	case OpAdvance:
		fmt.Fprintf(&b, " %s", o.Dur)
	case OpUpdate:
		fmt.Fprintf(&b, " pct=%d cost=%.2f", o.Pct, o.Cost)
	case OpReview:
		fmt.Fprintf(&b, " approve=%t", o.Approve)
	case OpReportTests:
		fmt.Fprintf(&b, " failed=%d", o.N)
	case OpWebhook:
		fmt.Fprintf(&b, " forged=%t", o.Forged)
	case OpInstrSave:
		fmt.Fprintf(&b, " text=%q", o.Text)
	case OpInstrRollback:
		fmt.Fprintf(&b, " to=%d", o.N)
	case OpSearch:
		fmt.Fprintf(&b, " q=%q", o.Text)
	case OpRestart:
		fmt.Fprintf(&b, " crash=%t", o.Crash)
	case OpSubscribe, OpUnsubscribe, OpDrain, OpBulk, OpSpawn:
		fmt.Fprintf(&b, " n=%d", o.N)
	case OpSetCapacity:
		fmt.Fprintf(&b, " global=%d limit=%d", o.N, o.Limit)
	case OpSetQuota:
		fmt.Fprintf(&b, " cost=%.2f tasks=%d concurrent=%d", o.Cost, o.N, o.Limit)
	case OpAnnounce:
		fmt.Fprintf(&b, " expires=%s text=%q", o.Dur, o.Text)
	case OpSuggestSplit, OpScheduleTick, OpRestore, OpSetGates:
		fmt.Fprintf(&b, " n=%d text=%q", o.N, o.Text)
	case OpLeaderRead, OpReadKnowledge, OpAddKnowledge, OpIncidentStart, OpOverride:
		fmt.Fprintf(&b, " text=%q", o.Text)
	case OpModerate, OpIncidentEnd:
		fmt.Fprintf(&b, " approve=%t", o.Approve)
	case OpLogOutput:
		fmt.Fprintf(&b, " seq=%d text=%q", o.N, o.Text)
	case OpLeak:
		fmt.Fprintf(&b, " channel=%s victim_token=#%d", o.Text, o.N)
	case OpSchedule:
		fmt.Fprintf(&b, " catch_up=%s max_active=%d", o.Text, o.N)
	case OpBatch:
		parts := make([]string, len(o.Sub))
		for i, s := range o.Sub {
			parts[i] = s.String()
		}
		fmt.Fprintf(&b, " [%s]", strings.Join(parts, " | "))
	}
	return b.String()
}

func (o Op) Mutating() bool {
	switch o.Kind {
	case OpAdvance, OpSweep, OpFlush, OpHeartbeat, OpRestart, OpBatch, OpSubscribe, OpUnsubscribe, OpDrain,
		OpEnqueue, OpMergeTick, OpAutopilotTick, OpWebhook, OpWebhookRace, OpSearch,
		OpInstructions, OpGetTask, OpLeaderRead, OpReadKnowledge, OpEstimate, OpIncidentStart, OpIncidentEnd, OpResumeAgent,
		OpScheduleTick, OpRetention, OpSetGates,
		OpGatewayList, OpLogOutput, OpTranscriptRead, OpLeak, OpDrill, OpPushCut, OpPushSettle:
		return false
	}
	return true
}

func (o Op) Authenticated() bool {
	switch o.Kind {
	case OpAssign, OpAssignReviewer, OpReassign, OpSpawn, OpPropose, OpClaim, OpHeartbeat, OpUpdate, OpBlock,
		OpSubmit, OpRelease, OpReview, OpAskOwner, OpReportTests,
		OpInstructions, OpGetTask, OpLeaderRead, OpRequestHelp, OpSuggestSplit, OpAcceptSplit, OpAddKnowledge, OpReadKnowledge,
		OpGatewayCall, OpGatewayList, OpLogOutput, OpLeak:
		return true
	}
	return false
}

func (o Op) capacityConfig() capacity.Config {
	c := capacity.Config{GlobalMaxAgents: o.N, Limits: []capacity.Limit{}}
	if o.Limit > 0 {
		c.Limits = append(c.Limits, capacity.Limit{Provider: o.Target, Model: "*", MaxConcurrency: o.Limit})
	}
	return c
}

func (o Op) bulkIDs() []string {
	if o.Kind == OpRegister {
		return []string{o.Target}
	}
	ids := make([]string, o.N)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-%03d", o.Target, i+1)
	}
	return ids
}

type Result struct {
	Err       error
	Lease     *time.Time
	Paused    bool
	Hits      []string
	Actions   int
	Spawned   int
	Sub       []Result
	ID        string
	Status    string
	Rotated   []string
	Context   *board.TaskContext
	Delivered map[string]int
	Estimate  *estimate.Report
	Forecast  *forecast.Forecast
	Stable    bool
	Upstream  int
	Seq       int64
}

func (r Result) Code() string {
	if r.Err == nil {
		return ""
	}
	return model.AsError(r.Err).Code
}

type AgentSpec struct {
	ID       string
	Role     string
	Kind     string
	Provider string
}

type TaskSpec struct {
	Ref       string
	Role      string
	DependsOn []string
	Budget    model.Budget
}

type World struct {
	Seed             int64
	Server           *ServerWorld
	AllowedProviders []string
	Lease            time.Duration
	BriefCost        float64
	MaxParallel      int
	Leader           string
	Rival            string
	Agents           []AgentSpec
	Tasks            []TaskSpec
}

type Setup struct {
	BriefID string
	PlanID  string
	TaskIDs []string
}

type SubStat struct {
	ID       int
	Topics   []string
	Expected int
	Received int
	Dropped  int
	Pending  int
	Disorder string
}

type State struct {
	Now         time.Time
	Paused      bool
	Tasks       []*model.Task
	Briefs      []*model.Brief
	Agents      []*model.Agent
	Plans       []*model.Plan
	Spawns      []*model.Spawn
	Capacity    *board.CapacityView
	Audit       []*model.AuditEvent
	Tokens      map[string]int
	Subs        []SubStat
	Subscribers int
	Faults      []*Violation
	Server      *ServerState
}

func (s *State) Task(id string) *model.Task {
	for _, t := range s.Tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (s *State) Brief(id string) *model.Brief {
	for _, b := range s.Briefs {
		if b.ID == id {
			return b
		}
	}
	return nil
}

func (s *State) Agent(id string) *model.Agent {
	for _, a := range s.Agents {
		if a.ID == id {
			return a
		}
	}
	return nil
}

func (s *State) CapacityConfig() capacity.Config {
	c := capacity.Config{GlobalMaxAgents: s.Capacity.GlobalMaxAgents, Limits: []capacity.Limit{}}
	for _, l := range s.Capacity.Limits {
		c.Limits = append(c.Limits, l.Limit)
	}
	return c
}

type System interface {
	Setup(ctx context.Context, w World) (Setup, error)
	Do(ctx context.Context, op Op) Result
	Snapshot(ctx context.Context) (*State, error)
	Audit(ctx context.Context) ([]*model.AuditEvent, error)
	Close() error
}

type Factory func(dir string, clock *Clock, lease time.Duration) (System, error)
