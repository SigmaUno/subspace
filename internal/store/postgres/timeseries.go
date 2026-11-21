package postgres

import (
	"fmt"
	"time"

	"github.com/SigmaUno/subspace/internal/store"
)

const auditStatus = `a.after->>'status'`

var timeseriesQueries = map[string]string{
	store.MetricTasksCreated: `SELECT (created_at / $4) * $4, count(*)::float8 FROM tasks
		WHERE created_at >= $1 AND created_at < $2 AND ($3::text = '' OR brief_id = $3) GROUP BY 1 ORDER BY 1`,
	store.MetricTasksDone: `SELECT (a.ts / $4) * $4, count(*)::float8 FROM audit a JOIN tasks t ON t.id = a.task_id
		WHERE a.ts >= $1 AND a.ts < $2 AND ` + auditStatus + ` = 'done' AND ($3::text = '' OR t.brief_id = $3) GROUP BY 1 ORDER BY 1`,
	store.MetricReviewRounds: `SELECT (a.ts / $4) * $4, count(*)::float8 FROM audit a JOIN tasks t ON t.id = a.task_id
		WHERE a.ts >= $1 AND a.ts < $2 AND a.tool = 'review' AND ` + auditStatus + ` IN ('changes_requested', 'needs_owner')
		AND ($3::text = '' OR t.brief_id = $3) GROUP BY 1 ORDER BY 1`,
	store.MetricActiveAgents: `SELECT (a.ts / $4) * $4, count(DISTINCT a.agent_id)::float8 FROM audit a LEFT JOIN tasks t ON t.id = a.task_id
		WHERE a.ts >= $1 AND a.ts < $2 AND a.role <> 'owner' AND a.agent_id <> '' AND ($3::text = '' OR t.brief_id = $3) GROUP BY 1 ORDER BY 1`,
	store.MetricTokens: `SELECT (ts / $4) * $4, sum(tokens_in + tokens_out)::float8 FROM usage_records
		WHERE ts >= $1 AND ts < $2 AND ($3::text = '' OR brief_id = $3) GROUP BY 1 ORDER BY 1`,
	store.MetricCostEUR: `SELECT (ts / $4) * $4, sum(CASE WHEN cost > prev THEN cost - prev ELSE 0 END)::float8 FROM (
			SELECT a.ts AS ts, COALESCE((a.after->>'cost_used')::float8, 0) AS cost,
				COALESCE(LAG((a.after->>'cost_used')::float8) OVER (PARTITION BY a.task_id ORDER BY a.id), 0) AS prev
			FROM audit a JOIN tasks t ON t.id = a.task_id
			WHERE a.tool = 'update_status' AND a.after IS NOT NULL AND ($3::text = '' OR t.brief_id = $3)
		) c WHERE ts >= $1 AND ts < $2 GROUP BY 1 ORDER BY 1`,
}

func (t *tx) Timeseries(q store.TimeseriesQuery) ([]store.TimeseriesPoint, error) {
	sql, ok := timeseriesQueries[q.Metric]
	if !ok {
		return nil, fmt.Errorf("unknown metric %q", q.Metric)
	}
	return list(t, func(r scanner) (store.TimeseriesPoint, error) {
		var at int64
		var p store.TimeseriesPoint
		if err := r.Scan(&at, &p.Value); err != nil {
			return p, err
		}
		p.At = time.UnixMilli(at).UTC()
		return p, nil
	}, sql, q.SinceMilli(), q.UntilMilli(), q.BriefID, q.Bucket.Milliseconds())
}

func (t *tx) PutUsage(r store.UsageRecord) error {
	return t.exec(`INSERT INTO usage_records(key, ts, agent_id, task_id, brief_id, tokens_in, tokens_out)
		VALUES($1, $2, $3, $4, $5, $6, $7) ON CONFLICT(key) DO UPDATE SET ts = excluded.ts, agent_id = excluded.agent_id,
		task_id = excluded.task_id, brief_id = excluded.brief_id, tokens_in = excluded.tokens_in, tokens_out = excluded.tokens_out`,
		r.Key, r.TS.UnixMilli(), r.AgentID, r.TaskID, r.BriefID, r.TokensIn, r.TokensOut)
}

var _ store.TimeseriesTx = (*tx)(nil)
