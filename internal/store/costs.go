package store

import "time"

const (
	CostByBrief     = "brief"
	CostByTask      = "task"
	CostByComponent = "component"
	CostByAgent     = "agent"
	CostByKind      = "kind"
	CostByModel     = "model"
	CostByProvider  = "provider"
	CostByRole      = "role"
	CostByDay       = "day"
)

var CostDimensions = []string{
	CostByBrief, CostByTask, CostByComponent, CostByAgent, CostByKind, CostByModel, CostByProvider, CostByRole, CostByDay,
}

type CostRecord struct {
	Key         string
	TS          time.Time
	BriefID     string
	TaskID      string
	ComponentID string
	AgentID     string
	Kind        string
	Role        string
	Provider    string
	Model       string
	TokensIn    int64
	TokensOut   int64
	CacheRead   int64
	CacheWrite  int64
	CostEUR     float64
}

type CostQuery struct {
	GroupBy string
	SplitBy string
	Since   time.Time
	Until   time.Time
	BriefID string
}

type CostRow struct {
	Key        string
	Sub        string
	Requests   int64
	TokensIn   int64
	TokensOut  int64
	CacheRead  int64
	CacheWrite int64
	CostEUR    float64
}

type CostsTx interface {
	PutCost(r CostRecord) error
	Costs(q CostQuery) ([]CostRow, error)
}
