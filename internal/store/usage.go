package store

import "time"

type Usage struct {
	ID           int64     `json:"id"`
	AgentID      string    `json:"agent_id"`
	TaskID       string    `json:"task_id"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	CostEUR      float64   `json:"cost_eur"`
	At           time.Time `json:"at"`
}

type ProviderError struct {
	ID       int64     `json:"id"`
	AgentID  string    `json:"agent_id"`
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	Code     string    `json:"code"`
	At       time.Time `json:"at"`
}

type UsageGroup string

const (
	UsageByProvider UsageGroup = "provider"
	UsageByModel    UsageGroup = "model"
	UsageByAgent    UsageGroup = "agent"
	UsageByBrief    UsageGroup = "brief"
	UsageByDay      UsageGroup = "day"
)

type UsageTotal struct {
	Key          string  `json:"key"`
	Provider     string  `json:"provider,omitempty"`
	Calls        int64   `json:"calls"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostEUR      float64 `json:"cost_eur"`
}

type ProviderStat struct {
	Provider           string     `json:"provider"`
	Model              string     `json:"model"`
	Calls              int64      `json:"calls"`
	LastSuccessAt      *time.Time `json:"last_success_at"`
	Errors             int64      `json:"errors"`
	ErrorsSinceSuccess int64      `json:"errors_since_success"`
	LastErrorAt        *time.Time `json:"last_error_at"`
	LastErrorCode      string     `json:"last_error_code"`
}

type UsageTx interface {
	InsertUsage(u *Usage) error
	InsertProviderError(e *ProviderError) error
	UsageTotals(by UsageGroup, since time.Time) ([]UsageTotal, error)
	ProviderStats() ([]ProviderStat, error)
}
