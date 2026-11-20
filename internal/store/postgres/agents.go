package postgres

import (
	"database/sql"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const agentDefaultLimit = 1000

const agentSelect = `SELECT a.id, a.role, a.kind, a.model, a.provider, a.status, a.pause_reason, a.token_hash,
  a.last_seen_at, a.created_at, a.spawned,
  (SELECT COUNT(*) FROM tasks t WHERE t.assignee = a.id AND t.status IN ('assigned','in_progress','changes_requested')),
  (SELECT t.id FROM tasks t WHERE t.assignee = a.id AND t.status = 'in_progress' ORDER BY t.id LIMIT 1)
  FROM agents a`

func scanAgent(r scanner) (*model.Agent, error) {
	var (
		a                 model.Agent
		pause             string
		lastSeen, created int64
		current           sql.NullString
	)
	if err := r.Scan(&a.ID, &a.Role, &a.Kind, &a.Model, &a.Provider, &a.Status, &pause, &a.TokenHash,
		&lastSeen, &created, &a.Spawned, &a.AssignedCount, &current); err != nil {
		return nil, err
	}
	a.PauseReason = strPtr(pause)
	a.LastSeenAt = fromMSPtr(lastSeen)
	a.CreatedAt = fromMS(created)
	if current.Valid {
		a.CurrentTaskID = &current.String
	}
	return &a, nil
}

func (t *tx) InsertAgent(a *model.Agent) error {
	created := a.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	return t.exec(`INSERT INTO agents(id, role, kind, model, provider, status, pause_reason, token_hash, last_seen_at,
  created_at, spawned) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		a.ID, a.Role, a.Kind, a.Model, a.Provider, a.Status, str(a.PauseReason), a.TokenHash,
		msPtr(a.LastSeenAt), created.UnixMilli(), a.Spawned)
}

func (t *tx) GetAgent(id string) (*model.Agent, error) {
	a, err := scanAgent(t.q.QueryRowContext(t.ctx, agentSelect+` WHERE a.id = $1`, id))
	return a, mapErr(err)
}

func (t *tx) GetAgentByTokenHash(hash string) (*model.Agent, error) {
	a, err := scanAgent(t.q.QueryRowContext(t.ctx, agentSelect+` WHERE a.token_hash = $1`, hash))
	return a, mapErr(err)
}

func (t *tx) UpdateAgent(a *model.Agent) error {
	return t.update(`UPDATE agents SET role=$2, kind=$3, model=$4, provider=$5, status=$6, pause_reason=$7,
  token_hash = CASE WHEN $8 = '' THEN token_hash ELSE $8 END, last_seen_at=$9, spawned=$10 WHERE id=$1`,
		a.ID, a.Role, a.Kind, a.Model, a.Provider, a.Status, str(a.PauseReason), a.TokenHash,
		msPtr(a.LastSeenAt), a.Spawned)
}

func (t *tx) ListAgents(f store.AgentFilter) ([]*model.Agent, error) {
	var a args
	a.eq("a.role", f.Role)
	a.eq("a.status", f.Status)
	limit := f.Limit
	if limit <= 0 {
		limit = agentDefaultLimit
	}
	return list(t, scanAgent, agentSelect+a.clause()+` ORDER BY a.id LIMIT `+a.add(limit), a.vals...)
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
