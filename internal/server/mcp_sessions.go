package server

import (
	"context"
	"time"

	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/store"
)

const mcpSessionsSchema = `
CREATE TABLE IF NOT EXISTS mcp_sessions (
  id TEXT PRIMARY KEY, agent_id TEXT NOT NULL, created_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS mcp_sessions_agent ON mcp_sessions(agent_id, created_at);
CREATE INDEX IF NOT EXISTS mcp_sessions_created ON mcp_sessions(created_at);
`

func init() {
	registerSchema(mcpSessionsSchema, "mcp_sessions")
}

type sqlSessions struct{ st store.Store }

func (s sqlSessions) CreateSession(ctx context.Context, id, agent string, now time.Time) error {
	return s.st.Atomic(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		if _, err := q.Exec(`DELETE FROM mcp_sessions WHERE created_at < ?`, now.Add(-mcp.SessionTTL).UnixMilli()); err != nil {
			return err
		}
		if _, err := q.Exec(`INSERT INTO mcp_sessions (id, agent_id, created_at) VALUES (?, ?, ?)`, id, agent, now.UnixMilli()); err != nil {
			return err
		}
		if _, err := q.Exec(`DELETE FROM mcp_sessions WHERE agent_id = ? AND id NOT IN
  (SELECT id FROM mcp_sessions WHERE agent_id = ? ORDER BY created_at DESC, id DESC LIMIT ?)`, agent, agent, mcp.MaxSessionsPerAgent); err != nil {
			return err
		}
		_, err := q.Exec(`DELETE FROM mcp_subscriptions WHERE session_id NOT IN (SELECT id FROM mcp_sessions)`)
		return err
	}))
}

func (s sqlSessions) SessionValid(ctx context.Context, id, agent string, now time.Time) (bool, error) {
	var n int
	err := s.st.View(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		rows, err := q.Query(`SELECT count(*) FROM mcp_sessions WHERE id = ? AND agent_id = ? AND created_at > ?`,
			id, agent, now.Add(-mcp.SessionTTL).UnixMilli())
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			if err := rows.Scan(&n); err != nil {
				return err
			}
		}
		return rows.Err()
	}))
	return n > 0, err
}

func (s sqlSessions) DropSession(ctx context.Context, id, agent string) (bool, error) {
	var n int64
	err := s.st.Atomic(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		res, err := q.Exec(`DELETE FROM mcp_sessions WHERE id = ? AND agent_id = ?`, id, agent)
		if err != nil {
			return err
		}
		if n, err = res.RowsAffected(); err != nil {
			return err
		}
		_, err = q.Exec(`DELETE FROM mcp_subscriptions WHERE session_id = ?`, id)
		return err
	}))
	return n > 0, err
}
