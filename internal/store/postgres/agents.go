package postgres

import (
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const agentDefaultLimit = 1000

var (
	agentSelect = `SELECT ` + store.Agents.Select("a") + ` FROM agents a`
	agentInsert = store.Agents.Insert(ph)
	agentUpdate = store.Agents.Update(ph)
)

func (t *tx) InsertAgent(a *model.Agent) error {
	v, err := store.Agents.InsertArgs(a)
	if err != nil {
		return err
	}
	return t.exec(agentInsert, v...)
}

func (t *tx) GetAgent(id string) (*model.Agent, error) {
	return get(t, store.Agents.Scan, agentSelect+` WHERE a.id = $1`, id)
}

func (t *tx) AgentProviders(ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := t.q.QueryContext(t.ctx, `SELECT id, provider FROM agents WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, p string
		if err := rows.Scan(&id, &p); err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, rows.Err()
}

func (t *tx) GetAgentByTokenHash(hash string) (*model.Agent, error) {
	return get(t, store.Agents.Scan, agentSelect+` WHERE a.token_hash = $1`, hash)
}

func (t *tx) UpdateAgent(a *model.Agent) error {
	v, err := store.Agents.UpdateArgs(a)
	if err != nil {
		return err
	}
	return t.update(agentUpdate, v...)
}

func (t *tx) ListAgents(f store.AgentFilter) ([]*model.Agent, error) {
	var a args
	a.eq("a.role", f.Role)
	a.eq("a.status", f.Status)
	if f.AfterID != "" {
		a.where = append(a.where, "a.id > "+a.add(f.AfterID))
	}
	limit := f.Limit
	if limit <= 0 {
		limit = agentDefaultLimit
	}
	return list(t, store.Agents.Scan, agentSelect+a.clause()+` ORDER BY a.id LIMIT `+a.add(limit), a.vals...)
}

func (t *tx) TouchAgents(seen map[string]int64) error {
	if len(seen) == 0 {
		return nil
	}
	ids := make([]string, 0, len(seen))
	at := make([]int64, 0, len(seen))
	for id, v := range seen {
		ids = append(ids, id)
		at = append(at, v)
	}
	return t.exec(`UPDATE agents a SET last_seen_at = v.at
  FROM unnest($1::text[], $2::bigint[]) AS v(id, at)
  WHERE a.id = v.id AND a.last_seen_at < v.at`, ids, at)
}
