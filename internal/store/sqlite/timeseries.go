package sqlite

import (
	"fmt"
	"time"

	"github.com/SigmaUno/subspace/internal/store"
)

const timeseriesSchema = `
CREATE TABLE IF NOT EXISTS usage_records (
  key TEXT PRIMARY KEY, ts INTEGER NOT NULL, agent_id TEXT NOT NULL DEFAULT '', task_id TEXT NOT NULL DEFAULT '',
  brief_id TEXT NOT NULL DEFAULT '', tokens_in INTEGER NOT NULL DEFAULT 0, tokens_out INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS usage_records_ts ON usage_records(ts);
CREATE INDEX IF NOT EXISTS audit_ts ON audit(ts);
CREATE INDEX IF NOT EXISTS tasks_created ON tasks(created_at);
`

func init() { RegisterSchema(timeseriesSchema) }

const auditStatus = `CASE WHEN json_valid(a.after) THEN json_extract(a.after, '$.status') END`

var timeseriesQueries = map[string]string{
	store.MetricTasksCreated: `SELECT (created_at / ?4) * ?4, count(*) FROM tasks
		WHERE created_at >= ?1 AND created_at < ?2 AND (?3 = '' OR brief_id = ?3) GROUP BY 1 ORDER BY 1`,
	store.MetricTasksDone: `SELECT (a.ts / ?4) * ?4, count(*) FROM audit a JOIN tasks t ON t.id = a.task_id
		WHERE a.ts >= ?1 AND a.ts < ?2 AND ` + auditStatus + ` = 'done' AND (?3 = '' OR t.brief_id = ?3) GROUP BY 1 ORDER BY 1`,
	store.MetricReviewRounds: `SELECT (a.ts / ?4) * ?4, count(*) FROM audit a JOIN tasks t ON t.id = a.task_id
		WHERE a.ts >= ?1 AND a.ts < ?2 AND a.tool = 'review' AND ` + auditStatus + ` IN ('changes_requested', 'needs_owner')
		AND (?3 = '' OR t.brief_id = ?3) GROUP BY 1 ORDER BY 1`,
	store.MetricActiveAgents: `SELECT (a.ts / ?4) * ?4, count(DISTINCT a.agent_id) FROM audit a LEFT JOIN tasks t ON t.id = a.task_id
		WHERE a.ts >= ?1 AND a.ts < ?2 AND a.role <> 'owner' AND a.agent_id <> '' AND (?3 = '' OR t.brief_id = ?3) GROUP BY 1 ORDER BY 1`,
	store.MetricTokens: `SELECT (ts / ?4) * ?4, sum(tokens_in + tokens_out) FROM usage_records
		WHERE ts >= ?1 AND ts < ?2 AND (?3 = '' OR brief_id = ?3) GROUP BY 1 ORDER BY 1`,
	store.MetricCostEUR: `SELECT (ts / ?4) * ?4, sum(CASE WHEN cost > prev THEN cost - prev ELSE 0 END) FROM (
			SELECT a.ts AS ts, COALESCE(json_extract(a.after, '$.cost_used'), 0) AS cost,
				COALESCE(LAG(json_extract(a.after, '$.cost_used')) OVER (PARTITION BY a.task_id ORDER BY a.id), 0) AS prev
			FROM audit a JOIN tasks t ON t.id = a.task_id
			WHERE a.tool = 'update_status' AND json_valid(a.after) AND (?3 = '' OR t.brief_id = ?3)
		) WHERE ts >= ?1 AND ts < ?2 GROUP BY 1 ORDER BY 1`,
}

func (t *tx) Timeseries(q store.TimeseriesQuery) ([]store.TimeseriesPoint, error) {
	sql, ok := timeseriesQueries[q.Metric]
	if !ok {
		return nil, fmt.Errorf("unknown metric %q", q.Metric)
	}
	rows, err := t.q.QueryContext(t.ctx, sql, q.Since.UnixMilli(), q.Until.UnixMilli(), q.BriefID, q.Bucket.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []store.TimeseriesPoint
	for rows.Next() {
		var at int64
		var v float64
		if err := rows.Scan(&at, &v); err != nil {
			return nil, err
		}
		out = append(out, store.TimeseriesPoint{At: time.UnixMilli(at).UTC(), Value: v})
	}
	return out, rows.Err()
}

func (t *tx) PutUsage(r store.UsageRecord) error {
	_, err := t.q.ExecContext(t.ctx, `INSERT INTO usage_records(key, ts, agent_id, task_id, brief_id, tokens_in, tokens_out)
		VALUES(?, ?, ?, ?, ?, ?, ?) ON CONFLICT(key) DO UPDATE SET ts = excluded.ts, agent_id = excluded.agent_id,
		task_id = excluded.task_id, brief_id = excluded.brief_id, tokens_in = excluded.tokens_in, tokens_out = excluded.tokens_out`,
		r.Key, r.TS.UnixMilli(), r.AgentID, r.TaskID, r.BriefID, r.TokensIn, r.TokensOut)
	return err
}

var _ store.TimeseriesTx = (*tx)(nil)
