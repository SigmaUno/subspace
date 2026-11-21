package store

import "time"

const (
	MetricTasksDone    = "tasks_done"
	MetricTasksCreated = "tasks_created"
	MetricCostEUR      = "cost_eur"
	MetricActiveAgents = "active_agents"
	MetricTokens       = "tokens"
	MetricReviewRounds = "review_rounds"
)

type TimeseriesQuery struct {
	Metric  string
	Since   time.Time
	Until   time.Time
	Bucket  time.Duration
	BriefID string
}

func (q TimeseriesQuery) SinceMilli() int64 { return q.Since.UnixMilli() }

func (q TimeseriesQuery) UntilMilli() int64 {
	ms := q.Until.UnixMilli()
	if q.Until.Nanosecond()%int(time.Millisecond) != 0 {
		ms++
	}
	return ms
}

type TimeseriesPoint struct {
	At    time.Time
	Value float64
}

type UsageRecord struct {
	Key       string
	TS        time.Time
	AgentID   string
	TaskID    string
	BriefID   string
	TokensIn  int64
	TokensOut int64
}

type TimeseriesTx interface {
	Timeseries(q TimeseriesQuery) ([]TimeseriesPoint, error)
	PutUsage(r UsageRecord) error
}
