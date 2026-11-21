package server

import (
	"context"

	"github.com/SigmaUno/subspace/internal/store"
)

const mcpSubscriptionsSchema = `
CREATE TABLE IF NOT EXISTS mcp_subscriptions (
  session_id TEXT NOT NULL, uri TEXT NOT NULL, PRIMARY KEY (session_id, uri)
);
CREATE INDEX IF NOT EXISTS mcp_subscriptions_uri ON mcp_subscriptions(uri);
`

func init() {
	registerSchema(mcpSubscriptionsSchema, "mcp_subscriptions")
}

func (s sqlSessions) AddSubscription(ctx context.Context, sid, uri string) error {
	return s.st.Atomic(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		_, err := q.Exec(`INSERT INTO mcp_subscriptions (session_id, uri) VALUES (?, ?) ON CONFLICT (session_id, uri) DO NOTHING`, sid, uri)
		return err
	}))
}

func (s sqlSessions) RemoveSubscription(ctx context.Context, sid, uri string) error {
	return s.st.Atomic(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		_, err := q.Exec(`DELETE FROM mcp_subscriptions WHERE session_id = ? AND uri = ?`, sid, uri)
		return err
	}))
}

func (s sqlSessions) Subscribers(ctx context.Context, uri string) ([]string, error) {
	var ids []string
	err := s.st.View(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		rows, err := q.Query(`SELECT session_id FROM mcp_subscriptions WHERE uri = ?`, uri)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	}))
	return ids, err
}
