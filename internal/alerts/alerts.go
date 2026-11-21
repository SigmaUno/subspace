package alerts

import (
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

const (
	SeverityWarning  = "warning"
	SeverityCritical = "critical"

	StatusActive   = "active"
	StatusResolved = "resolved"
)

type Finding struct {
	Key      string
	Severity string
	Title    string
	Summary  string
	BriefID  string
	TaskID   string
	AgentID  string
}

type Alert struct {
	ID         string     `json:"id"`
	Severity   string     `json:"severity"`
	Title      string     `json:"title"`
	Summary    string     `json:"summary"`
	BriefID    string     `json:"brief_id,omitempty"`
	TaskID     string     `json:"task_id,omitempty"`
	AgentID    string     `json:"agent_id,omitempty"`
	Status     string     `json:"status"`
	RaisedAt   time.Time  `json:"raised_at"`
	ResolvedAt *time.Time `json:"resolved_at"`
}

type Snapshot struct {
	Now            time.Time
	Paused         bool
	Tasks          []*model.Task
	Agents         []*model.Agent
	LastTaskChange time.Time
	LastAssignment time.Time
	BackupsEnabled bool
	LastBackup     time.Time
	BackupFailing  bool
	OffsiteEnabled bool
	LastOffsite    time.Time
	OffsiteError   string
	DiskKnown      bool
	DiskFree       uint64
	DBBytes        int64
	WALBytes       int64
	SSEStreams     int
	SSELagged      int
}

type Rule struct {
	Name string
	Eval func(Snapshot, Thresholds) []Finding
}
