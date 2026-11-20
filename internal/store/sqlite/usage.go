package sqlite

import (
	"fmt"
	"time"

	"github.com/SigmaUno/subspace/internal/store"
)

func init() {
	extraSchemas = append(extraSchemas, `
CREATE TABLE IF NOT EXISTS usage (
  id INTEGER PRIMARY KEY AUTOINCREMENT, agent_id TEXT NOT NULL, task_id TEXT NOT NULL DEFAULT '',
  provider TEXT NOT NULL, model TEXT NOT NULL, input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL,
  cost_eur REAL NOT NULL, at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS usage_at ON usage(at);
CREATE INDEX IF NOT EXISTS usage_provider ON usage(provider, model, at);
CREATE INDEX IF NOT EXISTS usage_task ON usage(task_id);

CREATE TABLE IF NOT EXISTS provider_errors (
  id INTEGER PRIMARY KEY AUTOINCREMENT, agent_id TEXT NOT NULL, provider TEXT NOT NULL, model TEXT NOT NULL,
  code TEXT NOT NULL, at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS provider_errors_provider ON provider_errors(provider, model, id);
`)
}

var _ store.UsageTx = (*tx)(nil)

func (t *tx) InsertUsage(u *store.Usage) error {
	res, err := t.q.ExecContext(t.ctx,
		`INSERT INTO usage(agent_id,task_id,provider,model,input_tokens,output_tokens,cost_eur,at) VALUES(?,?,?,?,?,?,?,?)`,
		u.AgentID, u.TaskID, u.Provider, u.Model, u.InputTokens, u.OutputTokens, u.CostEUR, u.At.UnixMilli())
	if err != nil {
		return err
	}
	u.ID, err = res.LastInsertId()
	return err
}

func (t *tx) InsertProviderError(e *store.ProviderError) error {
	res, err := t.q.ExecContext(t.ctx,
		`INSERT INTO provider_errors(agent_id,provider,model,code,at) VALUES(?,?,?,?,?)`,
		e.AgentID, e.Provider, e.Model, e.Code, e.At.UnixMilli())
	if err != nil {
		return err
	}
	e.ID, err = res.LastInsertId()
	return err
}

type usageGrouping struct{ key, provider, from, order string }

var usageGroupings = map[store.UsageGroup]usageGrouping{
	store.UsageByProvider: {"u.provider", "''", "usage u", "cost DESC, key"},
	store.UsageByModel:    {"u.model", "u.provider", "usage u", "cost DESC, key"},
	store.UsageByAgent:    {"u.agent_id", "''", "usage u", "cost DESC, key"},
	store.UsageByBrief:    {"COALESCE(t.brief_id, '')", "''", "usage u LEFT JOIN tasks t ON t.id = u.task_id", "cost DESC, key"},
	store.UsageByDay:      {"strftime('%Y-%m-%d', u.at / 1000, 'unixepoch')", "''", "usage u", "key"},
}

func (t *tx) UsageTotals(by store.UsageGroup, since time.Time) ([]store.UsageTotal, error) {
	g, ok := usageGroupings[by]
	if !ok {
		return nil, fmt.Errorf("unknown usage grouping %q", by)
	}
	q := fmt.Sprintf(`SELECT %s AS key, %s AS prov, COUNT(*), SUM(u.input_tokens), SUM(u.output_tokens), SUM(u.cost_eur) AS cost
		FROM %s WHERE u.at >= ? GROUP BY key, prov ORDER BY %s`, g.key, g.provider, g.from, g.order)
	rows, err := t.q.QueryContext(t.ctx, q, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []store.UsageTotal{}
	for rows.Next() {
		var u store.UsageTotal
		if err := rows.Scan(&u.Key, &u.Provider, &u.Calls, &u.InputTokens, &u.OutputTokens, &u.CostEUR); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (t *tx) ProviderStats() ([]store.ProviderStat, error) {
	rows, err := t.q.QueryContext(t.ctx, `
SELECT s.provider, s.model, s.calls, s.ok_at, s.errs, s.err_at,
  (SELECT COUNT(*) FROM provider_errors e WHERE e.provider = s.provider AND e.model = s.model AND e.at > s.ok_at),
  COALESCE((SELECT e.code FROM provider_errors e WHERE e.provider = s.provider AND e.model = s.model ORDER BY e.id DESC LIMIT 1), '')
FROM (
  SELECT provider, model, SUM(calls) AS calls, MAX(ok_at) AS ok_at, SUM(errs) AS errs, MAX(err_at) AS err_at FROM (
    SELECT provider, model, COUNT(*) AS calls, MAX(at) AS ok_at, 0 AS errs, 0 AS err_at FROM usage GROUP BY provider, model
    UNION ALL
    SELECT provider, model, 0, 0, COUNT(*), MAX(at) FROM provider_errors GROUP BY provider, model
  ) GROUP BY provider, model
) s ORDER BY s.provider, s.model`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []store.ProviderStat{}
	for rows.Next() {
		var p store.ProviderStat
		var okAt, errAt int64
		if err := rows.Scan(&p.Provider, &p.Model, &p.Calls, &okAt, &p.Errors, &errAt, &p.ErrorsSinceSuccess, &p.LastErrorCode); err != nil {
			return nil, err
		}
		if okAt > 0 {
			ts := time.UnixMilli(okAt).UTC()
			p.LastSuccessAt = &ts
		}
		if errAt > 0 {
			ts := time.UnixMilli(errAt).UTC()
			p.LastErrorAt = &ts
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
