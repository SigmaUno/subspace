package sqlite

import (
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

var (
	agentSelect = `SELECT ` + store.Agents.Select("a") + ` FROM agents a`
	agentInsert = store.Agents.Insert(ph)
	agentUpdate = store.Agents.Update(ph)
)

const agentDefaultLimit = 1000

func (t *tx) InsertAgent(a *model.Agent) error {
	args, err := store.Agents.InsertArgs(a)
	if err != nil {
		return err
	}
	return t.exec(agentInsert, args...)
}

func (t *tx) GetAgent(id string) (*model.Agent, error) {
	return get(t, store.Agents.Scan, agentSelect+` WHERE a.id = ?`, id)
}

func (t *tx) GetAgentByTokenHash(hash string) (*model.Agent, error) {
	return get(t, store.Agents.Scan, agentSelect+` WHERE a.token_hash = ?`, hash)
}

func (t *tx) UpdateAgent(a *model.Agent) error {
	args, err := store.Agents.UpdateArgs(a)
	if err != nil {
		return err
	}
	return t.update(agentUpdate, args...)
}

func (t *tx) ListAgents(f store.AgentFilter) ([]*model.Agent, error) {
	var w where
	w.eq("a.role", f.Role)
	w.eq("a.status", f.Status)
	if f.AfterID != "" {
		w.add("a.id > ?", f.AfterID)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = agentDefaultLimit
	}
	q := agentSelect + w.clause() + ` ORDER BY a.id` + w.limit(limit)
	return list(t, store.Agents.Scan, q, w.args...)
}

func (t *tx) TouchAgents(seen map[string]int64) error {
	for id, ms := range seen {
		if _, err := t.q.ExecContext(t.ctx,
			`UPDATE agents SET last_seen_at = ? WHERE id = ? AND last_seen_at < ?`, ms, id, ms); err != nil {
			return err
		}
	}
	return nil
}
