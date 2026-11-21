package postgres

import (
	"fmt"

	"github.com/SigmaUno/subspace/internal/store"
)

func init() {
	RegisterSchema(`
CREATE TABLE IF NOT EXISTS cost_records (
  key TEXT COLLATE "C" PRIMARY KEY, ts BIGINT NOT NULL, brief_id TEXT NOT NULL DEFAULT '', task_id TEXT NOT NULL DEFAULT '',
  component_id TEXT NOT NULL DEFAULT '', agent_id TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL DEFAULT '', provider TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '',
  tokens_in BIGINT NOT NULL DEFAULT 0, tokens_out BIGINT NOT NULL DEFAULT 0,
  cache_read BIGINT NOT NULL DEFAULT 0, cache_write BIGINT NOT NULL DEFAULT 0, cost_eur DOUBLE PRECISION NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS cost_records_ts ON cost_records(ts);
CREATE INDEX IF NOT EXISTS cost_records_brief ON cost_records(brief_id, ts);`, "cost_records")
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
	store.CostByDay:       "to_char(to_timestamp(ts / 1000) AT TIME ZONE 'UTC', 'YYYY-MM-DD')",
}

func (t *tx) PutCost(r store.CostRecord) error {
	return t.exec(`INSERT INTO cost_records(key, ts, brief_id, task_id, component_id, agent_id, kind, role,
		provider, model, tokens_in, tokens_out, cache_read, cache_write, cost_eur)
		VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) ON CONFLICT(key) DO UPDATE SET ts = excluded.ts,
		brief_id = excluded.brief_id, task_id = excluded.task_id, component_id = excluded.component_id,
		agent_id = excluded.agent_id, kind = excluded.kind, role = excluded.role, provider = excluded.provider,
		model = excluded.model, tokens_in = excluded.tokens_in, tokens_out = excluded.tokens_out,
		cache_read = excluded.cache_read, cache_write = excluded.cache_write, cost_eur = excluded.cost_eur`,
		r.Key, r.TS.UnixMilli(), r.BriefID, r.TaskID, r.ComponentID, r.AgentID, r.Kind, r.Role, r.Provider, r.Model,
		r.TokensIn, r.TokensOut, r.CacheRead, r.CacheWrite, r.CostEUR)
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
	return list(t, func(r scanner) (store.CostRow, error) {
		var c store.CostRow
		err := r.Scan(&c.Key, &c.Sub, &c.Requests, &c.TokensIn, &c.TokensOut, &c.CacheRead, &c.CacheWrite, &c.CostEUR)
		return c, err
	}, `SELECT `+group+`, `+split+`, count(*), sum(tokens_in)::bigint, sum(tokens_out)::bigint,
		sum(cache_read)::bigint, sum(cache_write)::bigint, sum(cost_eur)::float8 FROM cost_records
		WHERE ts >= $1 AND ts < $2 AND ($3::text = '' OR brief_id = $3) GROUP BY 1, 2 ORDER BY 1, 2`,
		q.Since.UnixMilli(), q.Until.UnixMilli(), q.BriefID)
}

var _ store.CostsTx = (*tx)(nil)
