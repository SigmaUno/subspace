package report

import (
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

type Snapshot struct {
	Brief  model.Brief
	Tasks  []*model.Task
	Agents map[string]string
	Audit  []*model.AuditEvent
	Now    time.Time
}

type Step struct {
	At             time.Time `json:"at"`
	From           string    `json:"from"`
	To             string    `json:"to"`
	By             string    `json:"by"`
	Tool           string    `json:"tool"`
	ElapsedSeconds int64     `json:"elapsed_seconds"`
}

type Review struct {
	At          time.Time `json:"at"`
	Reviewer    string    `json:"reviewer"`
	Verdict     string    `json:"verdict"`
	WaitSeconds int64     `json:"wait_seconds"`
}

type TaskReport struct {
	ID             string           `json:"id"`
	Title          string           `json:"title"`
	Status         string           `json:"status"`
	Assignee       string           `json:"assignee"`
	Provider       string           `json:"provider"`
	CostEUR        float64          `json:"cost_eur"`
	CreatedAt      time.Time        `json:"created_at"`
	ClaimedAt      *time.Time       `json:"claimed_at"`
	SubmittedAt    *time.Time       `json:"submitted_at"`
	DoneAt         *time.Time       `json:"done_at"`
	Claims         int              `json:"claims"`
	ReviewRounds   int              `json:"review_rounds"`
	Reviews        []Review         `json:"reviews"`
	Timeline       []Step           `json:"timeline"`
	TimeInStatus   map[string]int64 `json:"time_in_status"`
	QueueSeconds   int64            `json:"queue_seconds"`
	WorkSeconds    int64            `json:"work_seconds"`
	ReviewSeconds  int64            `json:"review_seconds"`
	BlockedSeconds int64            `json:"blocked_seconds"`
	ActiveSeconds  int64            `json:"active_seconds"`
	LeadSeconds    int64            `json:"lead_seconds"`
}

type Cost struct {
	TotalEUR   float64            `json:"total_eur"`
	ByTask     map[string]float64 `json:"by_task"`
	ByAgent    map[string]float64 `json:"by_agent"`
	ByProvider map[string]float64 `json:"by_provider"`
}

type Bucket struct {
	Hour time.Time `json:"hour"`
	Done int       `json:"done"`
}

type Throughput struct {
	Done        int      `json:"done"`
	WindowHours float64  `json:"window_hours"`
	PerHour     float64  `json:"per_hour"`
	Hourly      []Bucket `json:"hourly"`
}

type CriticalPath struct {
	TaskIDs []string `json:"task_ids"`
	Seconds int64    `json:"seconds"`
}

type Outlier struct {
	TaskID    string  `json:"task_id"`
	Metric    string  `json:"metric"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
}

type Report struct {
	BriefID        string         `json:"brief_id"`
	Title          string         `json:"title"`
	GeneratedAt    time.Time      `json:"generated_at"`
	Total          int            `json:"total"`
	Counts         map[string]int `json:"counts"`
	Tasks          []TaskReport   `json:"tasks"`
	Cost           Cost           `json:"cost"`
	BlockedSeconds int64          `json:"blocked_seconds"`
	Throughput     Throughput     `json:"throughput"`
	CriticalPath   CriticalPath   `json:"critical_path"`
	Outliers       []Outlier      `json:"outliers"`
}
