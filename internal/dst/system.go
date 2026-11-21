package dst

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

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
	OpApprove        Kind = "approve_plan"
	OpPauseAll       Kind = "pause_all"
	OpResumeAll      Kind = "resume_all"
	OpMarkDone       Kind = "mark_done"
	OpReopen         Kind = "reopen"
	OpAnswer         Kind = "answer_question"
	OpAssign         Kind = "assign"
	OpAssignReviewer Kind = "assign_reviewer"
	OpReassign       Kind = "reassign"
	OpClaim          Kind = "claim_task"
	OpHeartbeat      Kind = "heartbeat"
	OpUpdate         Kind = "update_status"
	OpBlock          Kind = "block"
	OpSubmit         Kind = "submit"
	OpRelease        Kind = "release_task"
	OpReview         Kind = "review"
	OpAskOwner       Kind = "ask_owner"
	OpCancel         Kind = "cancel"
	OpReportTests    Kind = "report_tests"
	OpEnqueue        Kind = "mq_enqueue"
	OpMergeTick      Kind = "mq_tick"
	OpAutopilotTick  Kind = "autopilot_tick"
	OpWebhook        Kind = "webhook"
	OpWebhookRace    Kind = "webhook_race"
	OpInstrSave      Kind = "instructions_save"
	OpInstrRollback  Kind = "instructions_rollback"
	OpSearch         Kind = "search"
)

type Op struct {
	Kind    Kind
	Actor   string
	Task    string
	Target  string
	Dur     time.Duration
	Pct     int
	Cost    float64
	Approve bool
	N       int
	Text    string
	Forged  bool
}

func (o Op) String() string {
	var b strings.Builder
	b.WriteString(string(o.Kind))
	if o.Actor != "" {
		fmt.Fprintf(&b, " by=%s", o.Actor)
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
	}
	return b.String()
}

func (o Op) Mutating() bool {
	switch o.Kind {
	case OpAdvance, OpSweep, OpFlush, OpHeartbeat, OpEnqueue, OpMergeTick, OpAutopilotTick, OpWebhook, OpWebhookRace, OpSearch:
		return false
	}
	return true
}

type Result struct {
	Err     error
	Lease   *time.Time
	Paused  bool
	Hits    []string
	Actions int
	Spawned int
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
	Agents           []AgentSpec
	Tasks            []TaskSpec
}

type Setup struct {
	BriefID string
	PlanID  string
	TaskIDs []string
}

type State struct {
	Now    time.Time
	Paused bool
	Tasks  []*model.Task
	Briefs []*model.Brief
	Audit  []*model.AuditEvent
	Agents []*model.Agent
	Server *ServerState
}

func (s *State) Task(id string) *model.Task {
	for _, t := range s.Tasks {
		if t.ID == id {
			return t
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

func (s *State) Brief(id string) *model.Brief {
	for _, b := range s.Briefs {
		if b.ID == id {
			return b
		}
	}
	return nil
}

type System interface {
	Setup(ctx context.Context, w World) (Setup, error)
	Do(ctx context.Context, op Op) Result
	Snapshot(ctx context.Context) (*State, error)
	Close() error
}

type Factory func(dir string, clock *Clock, lease time.Duration) (System, error)
