// Package store defines the persistence interface of Subspace. Business logic
// depends only on these interfaces so the SQLite implementation can be swapped
// (e.g. for Postgres).
package store

import (
	"context"
	"errors"

	"github.com/SigmaUno/subspace/internal/model"
)

// ErrNotFound is returned by Get* when the row does not exist.
var ErrNotFound = errors.New("store: not found")

// ErrDuplicate is returned on unique violations.
var ErrDuplicate = errors.New("store: duplicate")

// TaskFilter selects tasks. Zero fields are ignored.
type TaskFilter struct {
	BriefID  string
	PlanID   string
	Status   []string
	Assignee string
	Reviewer string
	Role     string
	Limit    int
}

// AgentFilter selects agents.
type AgentFilter struct {
	Role   string
	Status string
	Limit  int
}

// AuditFilter selects audit events (ascending id).
type AuditFilter struct {
	AgentID string
	TaskID  string
	Tool    string
	SinceID int64
	Limit   int
}

// Tx is the set of typed operations available both for reads (View) and inside
// serialized write transactions (Atomic).
type Tx interface {
	NextID(prefix string) (string, error)

	InsertBrief(b *model.Brief) error
	GetBrief(id string) (*model.Brief, error)
	UpdateBrief(b *model.Brief) error
	ListBriefs(status string) ([]*model.Brief, error)

	InsertPlan(p *model.Plan) error
	GetPlan(id string) (*model.Plan, error) // Tasks not populated
	UpdatePlan(p *model.Plan) error
	ListPlans(briefID, status string) ([]*model.Plan, error) // Tasks not populated

	InsertTask(t *model.Task) error
	GetTask(id string) (*model.Task, error)
	UpdateTask(t *model.Task) error
	ListTasks(f TaskFilter) ([]*model.Task, error)
	CountTasksByStatus() (map[string]int, error)
	BriefCost(briefID string) (float64, error)

	InsertAgent(a *model.Agent) error
	GetAgent(id string) (*model.Agent, error)
	GetAgentByTokenHash(hash string) (*model.Agent, error)
	UpdateAgent(a *model.Agent) error
	ListAgents(f AgentFilter) ([]*model.Agent, error)
	// TouchAgents sets last_seen for many agents (heartbeat flush).
	TouchAgents(seen map[string]int64) error
	// ExtendLeases monotonically extends heartbeat/lease of in_progress tasks (heartbeat flush).
	ExtendLeases(rows []LeaseRow) error

	InsertMessage(m *model.Message) error
	ListMessages(kind, id string, sinceID int64, limit int) ([]*model.Message, error)

	InsertQuestion(q *model.Question) error
	GetQuestion(id string) (*model.Question, error)
	UpdateQuestion(q *model.Question) error
	ListQuestions(status string) ([]*model.Question, error)

	InsertAudit(e *model.AuditEvent) error
	ListAudit(f AuditFilter) ([]*model.AuditEvent, error)

	InsertSpawn(s *model.Spawn) error
	UpdateSpawn(s *model.Spawn) error
	ListSpawns(briefID string) ([]*model.Spawn, error)

	GetKV(key string) (string, bool, error)
	SetKV(key, value string) error
}

// LeaseRow is one heartbeat flush row (unix milliseconds).
type LeaseRow struct {
	TaskID    string
	Assignee  string
	Heartbeat int64
	Lease     int64
}

// Store is a persistence backend.
type Store interface {
	// Atomic runs fn in a serialized write transaction. A returned error rolls
	// back only this fn's effects (other queued writers are unaffected).
	Atomic(ctx context.Context, fn func(Tx) error) error
	// View runs fn against a consistent read connection.
	View(ctx context.Context, fn func(Tx) error) error
	// Backup writes a consistent copy of the database to path.
	Backup(ctx context.Context, path string) error
	Close() error
}
