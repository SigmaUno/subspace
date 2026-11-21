package sqlite

import (
	"fmt"
	"time"

	"github.com/SigmaUno/subspace/internal/store"
)

func init() {
	RegisterSchema(`
CREATE TABLE IF NOT EXISTS cost_records (
  key TEXT PRIMARY KEY, ts INTEGER NOT NULL, brief_id TEXT NOT NULL DEFAULT '', task_id TEXT NOT NULL DEFAULT '',
  component_id TEXT NOT NULL DEFAULT '', agent_id TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL DEFAULT '', provider TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '',
  tokens_in INTEGER NOT NULL DEFAULT 0, tokens_out INTEGER NOT NULL DEFAULT 0,
  cache_read INTEGER NOT NULL DEFAULT 0, cache_write INTEGER NOT NULL DEFAULT 0, cost_eur REAL NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS cost_records_ts ON cost_records(ts);
CREATE INDEX IF NOT EXISTS cost_records_brief ON cost_records(brief_id, ts);`)
}

var costColumns = map[string]string{
	store.CostByBrief:     "brief_id",
	store.CostByTask:      "task_id",
	store.CostByComponent: "component_id",
	store.CostByAgent:     "agent_id",
	store.CostByKind:      "kind",
	store.CostByModel:     "model",
	store.CostByProvider:  "provider",
	store.CostByRole:      "role",
	store.CostByDay:       "strftime('%Y-%m-%d', ts / 1000, 'unixepoch')",
}

func (t *tx) PutCost(r store.CostRecord) error {
	_, err := t.q.ExecContext(t.ctx, `INSERT INTO cost_records(key, ts, brief_id, task_id, component_id, agent_id, kind, role,
		provider, model, tokens_in, tokens_out, cache_read, cache_write, cost_eur)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(key) DO UPDATE SET ts = excluded.ts,
		brief_id = excluded.brief_id, task_id = excluded.task_id, component_id = excluded.component_id,
		agent_id = excluded.agent_id, kind = excluded.kind, role = excluded.role, provider = excluded.provider,
		model = excluded.model, tokens_in = excluded.tokens_in, tokens_out = excluded.tokens_out,
		cache_read = excluded.cache_read, cache_write = excluded.cache_write, cost_eur = excluded.cost_eur`,
		r.Key, r.TS.UnixMilli(), r.BriefID, r.TaskID, r.ComponentID, r.AgentID, r.Kind, r.Role, r.Provider, r.Model,
		r.TokensIn, r.TokensOut, r.CacheRead, r.CacheWrite, r.CostEUR)
	return err
}

func (t *tx) Costs(q store.CostQuery) ([]store.CostRow, error) {
	group, ok := costColumns[q.GroupBy]
	if !ok {
		return nil, fmt.Errorf("unknown cost dimension %q", q.GroupBy)
	}
	split := "''"
	if q.SplitBy != "" {
		if split, ok = costColumns[q.SplitBy]; !ok {
			return nil, fmt.Errorf("unknown cost dimension %q", q.SplitBy)
		}
	}
	rows, err := t.q.QueryContext(t.ctx, `SELECT `+group+`, `+split+`, count(*), sum(tokens_in), sum(tokens_out),
		sum(cache_read), sum(cache_write), sum(cost_eur) FROM cost_records
		WHERE ts >= ?1 AND ts < ?2 AND (?3 = '' OR brief_id = ?3) GROUP BY 1, 2 ORDER BY 1, 2`,
		q.Since.UnixMilli(), q.Until.UnixMilli(), q.BriefID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []store.CostRow
	for rows.Next() {
		var r store.CostRow
		if err := rows.Scan(&r.Key, &r.Sub, &r.Requests, &r.TokensIn, &r.TokensOut, &r.CacheRead, &r.CacheWrite, &r.CostEUR); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (t *tx) ListCosts(since, until time.Time) ([]store.CostRecord, error) {
	rows, err := t.q.QueryContext(t.ctx, `SELECT key, ts, brief_id, task_id, component_id, agent_id, kind, role, provider, model,
		tokens_in, tokens_out, cache_read, cache_write, cost_eur FROM cost_records WHERE ts >= ? AND ts < ? ORDER BY ts, key`,
		since.UnixMilli(), until.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []store.CostRecord
	for rows.Next() {
		var r store.CostRecord
		var ts int64
		if err := rows.Scan(&r.Key, &ts, &r.BriefID, &r.TaskID, &r.ComponentID, &r.AgentID, &r.Kind, &r.Role, &r.Provider, &r.Model,
			&r.TokensIn, &r.TokensOut, &r.CacheRead, &r.CacheWrite, &r.CostEUR); err != nil {
			return nil, err
		}
		r.TS = time.UnixMilli(ts).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

var _ store.CostsTx = (*tx)(nil)
