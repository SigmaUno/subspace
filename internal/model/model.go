package model

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

const (
	RoleOwner    = "owner"
	RoleLeader   = "leader"
	RoleWorker   = "worker"
	RoleTester   = "tester"
	RoleReviewer = "reviewer"
)

func ValidAgentRole(r string) bool {
	switch r {
	case RoleLeader, RoleWorker, RoleTester, RoleReviewer:
		return true
	}
	return false
}

const (
	StatusBacklog          = "backlog"
	StatusReady            = "ready"
	StatusAssigned         = "assigned"
	StatusInProgress       = "in_progress"
	StatusSubmitted        = "submitted"
	StatusInReview         = "in_review"
	StatusChangesRequested = "changes_requested"
	StatusApproved         = "approved"
	StatusDone             = "done"
	StatusBlocked          = "blocked"
	StatusNeedsOwner       = "needs_owner"
	StatusCancelled        = "cancelled"
)

var AllStatuses = []string{
	StatusBacklog, StatusReady, StatusAssigned, StatusInProgress, StatusSubmitted, StatusInReview,
	StatusChangesRequested, StatusApproved, StatusDone, StatusBlocked, StatusNeedsOwner, StatusCancelled,
}

const (
	AgentActive  = "active"
	AgentPaused  = "paused"
	AgentRevoked = "revoked"
)

const (
	KindClaudeCode = "claude-code"
	KindCodex      = "codex"
	KindOpenCode   = "opencode"
	KindOther      = "other"
)

func ProviderForKind(kind string) string {
	switch kind {
	case KindClaudeCode:
		return "anthropic"
	case KindCodex:
		return "openai"
	case KindOpenCode:
		return "deepseek"
	}
	return "other"
}

const (
	ThreadAgent = "agent"
	ThreadTask  = "task"
	ThreadBrief = "brief"
)

const (
	MsgMessage  = "message"
	MsgStatus   = "status"
	MsgQuestion = "question"
	MsgAnswer   = "answer"
	MsgNote     = "note"
	MsgSystem   = "system"
)

const (
	PlanProposed   = "proposed"
	PlanApproved   = "approved"
	PlanRejected   = "rejected"
	PlanSuperseded = "superseded"
)

type Budget struct {
	MaxMinutes int     `json:"max_minutes"`
	MaxCostEUR float64 `json:"max_cost_eur"`
}

type BriefBudget struct {
	MaxCostEUR       float64 `json:"max_cost_eur"`
	MaxParallelAgent int     `json:"max_parallel_agents"`
}

type Brief struct {
	ID               string      `json:"id"`
	Title            string      `json:"title"`
	Body             string      `json:"body"`
	Repos            []string    `json:"repos"`
	Status           string      `json:"status"`
	Budget           BriefBudget `json:"budget"`
	AllowedProviders []string    `json:"allowed_providers"`
	CrossReview      bool        `json:"cross_review"`
	AutoDoneOnMerge  bool        `json:"auto_done_on_merge"`
	CostUsed         float64     `json:"cost_used"`
	PlanStatus       string      `json:"plan_status"`
	LeaderID         *string     `json:"leader_id"`
	CreatedAt        time.Time   `json:"created_at"`
}

func (b *Brief) LedBy(leaderID string) bool {
	return b.LeaderID == nil || *b.LeaderID == leaderID
}

type TestResult struct {
	Passed     int       `json:"passed"`
	Failed     int       `json:"failed"`
	ReportedAt time.Time `json:"reported_at"`
}

type Task struct {
	ID                 string      `json:"id"`
	BriefID            string      `json:"brief_id"`
	PlanID             string      `json:"plan_id"`
	Title              string      `json:"title"`
	Spec               string      `json:"spec"`
	AcceptanceCriteria []string    `json:"acceptance_criteria"`
	Repo               string      `json:"repo"`
	Branch             string      `json:"branch"`
	Role               string      `json:"role"`
	Assignee           *string     `json:"assignee"`
	Reviewer           *string     `json:"reviewer"`
	DependsOn          []string    `json:"depends_on"`
	Priority           int         `json:"priority"`
	ComponentID        *string     `json:"component_id"`
	Status             string      `json:"status"`
	ProgressPct        int         `json:"progress_pct"`
	LastHeartbeatAt    *time.Time  `json:"last_heartbeat_at"`
	LeaseExpiresAt     *time.Time  `json:"lease_expires_at"`
	PRURL              *string     `json:"pr_url"`
	Budget             Budget      `json:"budget"`
	CostUsed           float64     `json:"cost_used"`
	MinutesUsed        int         `json:"minutes_used"`
	ReviewRounds       int         `json:"review_rounds"`
	Tests              *TestResult `json:"tests"`
	BlockedReason      *string     `json:"blocked_reason"`
	LeaseExpiries      int         `json:"lease_expiries"`
	CreatedAt          time.Time   `json:"created_at"`
	UpdatedAt          time.Time   `json:"updated_at"`

	ClaimedAt *time.Time `json:"-"`
}

type Plan struct {
	ID         string        `json:"id"`
	BriefID    string        `json:"brief_id"`
	Status     string        `json:"status"`
	ProposedBy string        `json:"proposed_by"`
	Note       *string       `json:"note"`
	Tasks      []*Task       `json:"tasks"`
	Warnings   []PlanFinding `json:"warnings"`
	CreatedAt  time.Time     `json:"created_at"`
	DecidedAt  *time.Time    `json:"decided_at"`
}

type PlanFinding struct {
	Severity string `json:"severity"`
	Rule     string `json:"rule"`
	Task     string `json:"task,omitempty"`
	Message  string `json:"message"`
}

type Agent struct {
	ID            string     `json:"id"`
	Role          string     `json:"role"`
	Kind          string     `json:"kind"`
	Model         string     `json:"model"`
	Provider      string     `json:"provider"`
	Status        string     `json:"status"`
	PauseReason   *string    `json:"pause_reason"`
	CurrentTaskID *string    `json:"current_task_id"`
	AssignedCount int        `json:"assigned_count"`
	LastSeenAt    *time.Time `json:"last_seen_at"`
	CreatedAt     time.Time  `json:"created_at"`
	Spawned       bool       `json:"spawned"`

	TokenExpiresAt *time.Time `json:"token_expires_at"`

	TokenHash string `json:"-"`
}

type Message struct {
	ID         int64     `json:"id"`
	ThreadKind string    `json:"thread_kind"`
	ThreadID   string    `json:"thread_id"`
	AuthorID   string    `json:"author_id"`
	AuthorRole string    `json:"author_role"`
	Kind       string    `json:"kind"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"created_at"`
}

type Question struct {
	ID         string     `json:"id"`
	BriefID    *string    `json:"brief_id"`
	TaskID     *string    `json:"task_id"`
	AgentID    string     `json:"agent_id"`
	Question   string     `json:"question"`
	Options    []string   `json:"options"`
	Status     string     `json:"status"`
	Answer     *string    `json:"answer"`
	AnsweredAt *time.Time `json:"answered_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

type AuditEvent struct {
	ID      int64          `json:"id"`
	TS      time.Time      `json:"ts"`
	AgentID string         `json:"agent_id"`
	Role    string         `json:"role"`
	Tool    string         `json:"tool"`
	TaskID  *string        `json:"task_id"`
	Before  map[string]any `json:"before"`
	After   map[string]any `json:"after"`
	Note    *string        `json:"note"`
	Hash    string         `json:"hash,omitempty"`
}

type AuditHead struct {
	ID   int64  `json:"id"`
	Hash string `json:"hash"`
}

type AuditCheck struct {
	OK       bool      `json:"ok"`
	Events   int64     `json:"events"`
	BrokenID int64     `json:"broken_id,omitempty"`
	Head     AuditHead `json:"head"`
}

type Spawn struct {
	ID          string    `json:"id"`
	BriefID     string    `json:"brief_id"`
	AgentID     string    `json:"agent_id"`
	Kind        string    `json:"kind"`
	Model       string    `json:"model"`
	Provider    string    `json:"provider"`
	Status      string    `json:"status"`
	Spawner     string    `json:"spawner"`
	Error       *string   `json:"error"`
	RequestedBy string    `json:"requested_by"`
	CreatedAt   time.Time `json:"created_at"`
}

type Actor struct {
	ID   string
	Role string
	Kind string
}

var Owner = Actor{ID: "owner", Role: RoleOwner}

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func (e *Error) HTTPStatus() int {
	switch e.Code {
	case "bad_request":
		return http.StatusBadRequest
	case "unauthorized", "token_expired":
		return http.StatusUnauthorized
	case "forbidden", "not_assigned":
		return http.StatusForbidden
	case "not_found":
		return http.StatusNotFound
	case "conflict", "capacity", "budget_exceeded", "lease_expired":
		return http.StatusConflict
	case "paused", "agent_paused":
		return http.StatusLocked
	case "rate_limited":
		return http.StatusTooManyRequests
	}
	return http.StatusInternalServerError
}

func Errf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func BadRequest(format string, a ...any) *Error { return Errf("bad_request", format, a...) }
func NotFound(format string, a ...any) *Error   { return Errf("not_found", format, a...) }
func Conflict(format string, a ...any) *Error   { return Errf("conflict", format, a...) }
func Forbidden(format string, a ...any) *Error  { return Errf("forbidden", format, a...) }

func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Code: "internal", Message: "internal error"}
}
