package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/SigmaUno/subspace/internal/store"
)

func init() {
	RegisterSchema(`
CREATE TABLE IF NOT EXISTS action_link_uses (
  nonce TEXT PRIMARY KEY, decision_id TEXT NOT NULL, action TEXT NOT NULL, used_at INTEGER NOT NULL, expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS action_link_uses_expires ON action_link_uses(expires_at);`)
}

func (s *Store) ClaimActionNonce(ctx context.Context, nonce, decisionID, action string, expires, now time.Time) (bool, error) {
	var claimed bool
	err := s.Atomic(ctx, func(t store.Tx) error {
		q := t.(*tx).q
		if _, err := q.ExecContext(ctx, `DELETE FROM action_link_uses WHERE expires_at < ?`, now.Add(-time.Minute).UnixMilli()); err != nil {
			return err
		}
		res, err := q.ExecContext(ctx, `INSERT INTO action_link_uses (nonce, decision_id, action, used_at, expires_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(nonce) DO NOTHING`, nonce, decisionID, action, now.UnixMilli(), expires.UnixMilli())
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		claimed = n == 1
		return err
	})
	return claimed, err
}

func (s *Store) ReleaseActionNonce(ctx context.Context, nonce string) error {
	return s.Atomic(ctx, func(t store.Tx) error {
		_, err := t.(*tx).q.ExecContext(ctx, `DELETE FROM action_link_uses WHERE nonce = ?`, nonce)
		return err
	})
}

func (s *Store) ActionNonceUsed(ctx context.Context, nonce string) (bool, error) {
	var one int
	err := s.rdb.QueryRowContext(ctx, `SELECT 1 FROM action_link_uses WHERE nonce = ?`, nonce).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
