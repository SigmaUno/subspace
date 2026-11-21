package store

import (
	"context"
	"errors"

	"github.com/SigmaUno/subspace/internal/model"
)

var ErrNotFound = errors.New("store: not found")

var ErrDuplicate = errors.New("store: duplicate")

type TaskFilter struct {
	BriefID  string
	PlanID   string
	Status   []string
	Assignee string
	Reviewer string
	Role     string
	AfterID  string
	Limit    int
}

type AgentFilter struct {
	Role    string
	Status  string
	AfterID string
	Limit   int
}

type BriefFilter struct {
	Status  string
	AfterID string
	Limit   int
}

type PlanFilter struct {
	BriefID string
	Status  string
	AfterID string
	Limit   int
}

type QuestionFilter struct {
	Status  string
	AfterID string
	Limit   int
}

type SpawnFilter struct {
	BriefID string
	AfterID string
	Limit   int
}

type AuditFilter struct {
	AgentID string
	TaskID  string
	Tool    string
	SinceID int64
	Limit   int
}

type Tx interface {
	NextID(prefix string) (string, error)

	InsertBrief(b *model.Brief) error
	GetBrief(id string) (*model.Brief, error)
	UpdateBrief(b *model.Brief) error
	ListBriefs(f BriefFilter) ([]*model.Brief, error)

	InsertPlan(p *model.Plan) error
	GetPlan(id string) (*model.Plan, error)
	UpdatePlan(p *model.Plan) error
	ListPlans(f PlanFilter) ([]*model.Plan, error)

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

	TouchAgents(seen map[string]int64) error

	ExtendLeases(rows []LeaseRow) error

	InsertMessage(m *model.Message) error
	ListMessages(kind, id string, sinceID int64, limit int) ([]*model.Message, error)

	InsertQuestion(q *model.Question) error
	GetQuestion(id string) (*model.Question, error)
	UpdateQuestion(q *model.Question) error
	ListQuestions(f QuestionFilter) ([]*model.Question, error)

	InsertAudit(e *model.AuditEvent) error
	ListAudit(f AuditFilter) ([]*model.AuditEvent, error)
	AuditHead(maxID int64) (model.AuditHead, error)
	VerifyAudit() (model.AuditCheck, error)

	InsertSpawn(s *model.Spawn) error
	UpdateSpawn(s *model.Spawn) error
	ListSpawns(f SpawnFilter) ([]*model.Spawn, error)

	GetKV(key string) (string, bool, error)
	SetKV(key, value string) error
}

type LeaseRow struct {
	TaskID    string
	Assignee  string
	Heartbeat int64
	Lease     int64
}

type Store interface {
	Atomic(ctx context.Context, fn func(Tx) error) error

	View(ctx context.Context, fn func(Tx) error) error

	Backup(ctx context.Context, path string) error
	Close() error
}
