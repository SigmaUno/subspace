package sqlite

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const agentSelect = `SELECT a.id, a.role, a.kind, a.model, a.provider, a.status, a.pause_reason, a.token_hash,
  a.last_seen_at, a.created_at, a.spawned, a.token_expires_at,
  (SELECT COUNT(*) FROM tasks t WHERE t.assignee = a.id AND t.status IN ('assigned','in_progress','changes_requested')),
  (SELECT t.id FROM tasks t WHERE t.assignee = a.id AND t.status = 'in_progress' ORDER BY t.id LIMIT 1)
  FROM agents a`

const agentDefaultLimit = 1000

type agentRowScanner interface {
	Scan(dest ...any) error
}

func agentScan(r agentRowScanner) (*model.Agent, error) {
	var (
		a                 model.Agent
		pause             string
		lastSeen, created int64
		spawned, expires  int64
		current           sql.NullString
		assigned          int
	)
	if err := r.Scan(&a.ID, &a.Role, &a.Kind, &a.Model, &a.Provider, &a.Status, &pause, &a.TokenHash,
		&lastSeen, &created, &spawned, &expires, &assigned, &current); err != nil {
		return nil, err
	}
	if pause != "" {
		a.PauseReason = &pause
	}
	if lastSeen != 0 {
		ts := time.UnixMilli(lastSeen).UTC()
		a.LastSeenAt = &ts
	}
	if expires != 0 {
		ts := time.UnixMilli(expires).UTC()
		a.TokenExpiresAt = &ts
	}
	a.CreatedAt = time.UnixMilli(created).UTC()
	a.Spawned = spawned != 0
	a.AssignedCount = assigned
	if current.Valid {
		id := current.String
		a.CurrentTaskID = &id
	}
	return &a, nil
}

func agentErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return store.ErrNotFound
	case strings.Contains(err.Error(), "UNIQUE constraint failed"):
		return store.ErrDuplicate
	}
	return err
}

func agentMillis(t *time.Time) int64 {
	if t == nil || t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func (t *tx) InsertAgent(a *model.Agent) error {
	pause := ""
	if a.PauseReason != nil {
		pause = *a.PauseReason
	}
	created := a.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	spawned := 0
	if a.Spawned {
		spawned = 1
	}
	_, err := t.q.ExecContext(t.ctx,
		`INSERT INTO agents(id, role, kind, model, provider, status, pause_reason, token_hash, last_seen_at, created_at, spawned, token_expires_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.Role, a.Kind, a.Model, a.Provider, a.Status, pause, a.TokenHash,
		agentMillis(a.LastSeenAt), created.UnixMilli(), spawned, agentMillis(a.TokenExpiresAt))
	return agentErr(err)
}

func (t *tx) GetAgent(id string) (*model.Agent, error) {
	a, err := agentScan(t.q.QueryRowContext(t.ctx, agentSelect+` WHERE a.id = ?`, id))
	if err != nil {
		return nil, agentErr(err)
	}
	return a, nil
}

func (t *tx) GetAgentByTokenHash(hash string) (*model.Agent, error) {
	a, err := agentScan(t.q.QueryRowContext(t.ctx, agentSelect+` WHERE a.token_hash = ?`, hash))
	if err != nil {
		return nil, agentErr(err)
	}
	return a, nil
}

func (t *tx) UpdateAgent(a *model.Agent) error {
	pause := ""
	if a.PauseReason != nil {
		pause = *a.PauseReason
	}
	spawned := 0
	if a.Spawned {
		spawned = 1
	}
	res, err := t.q.ExecContext(t.ctx,
		`UPDATE agents SET role=?, kind=?, model=?, provider=?, status=?, pause_reason=?,
		   token_hash=CASE WHEN ?='' THEN token_hash ELSE ? END, last_seen_at=?, spawned=?, token_expires_at=? WHERE id=?`,
		a.Role, a.Kind, a.Model, a.Provider, a.Status, pause, a.TokenHash, a.TokenHash,
		agentMillis(a.LastSeenAt), spawned, agentMillis(a.TokenExpiresAt), a.ID)
	if err != nil {
		return agentErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (t *tx) ListAgents(f store.AgentFilter) ([]*model.Agent, error) {
	q := agentSelect
	var (
		where []string
		args  []any
	)
	if f.Role != "" {
		where = append(where, "a.role = ?")
		args = append(args, f.Role)
	}
	if f.Status != "" {
		where = append(where, "a.status = ?")
		args = append(args, f.Status)
	}
	if f.AfterID != "" {
		where = append(where, "a.id > ?")
		args = append(args, f.AfterID)
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = agentDefaultLimit
	}
	q += " ORDER BY a.id LIMIT ?"
	args = append(args, limit)

	rows, err := t.q.QueryContext(t.ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*model.Agent{}
	for rows.Next() {
		a, err := agentScan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
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
