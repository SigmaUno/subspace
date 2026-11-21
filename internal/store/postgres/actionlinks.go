package postgres

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
  nonce TEXT COLLATE "C" PRIMARY KEY, decision_id TEXT NOT NULL, action TEXT NOT NULL, used_at BIGINT NOT NULL, expires_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS action_link_uses_expires ON action_link_uses(expires_at);`, "action_link_uses")
}

func (s *Store) ClaimActionNonce(ctx context.Context, nonce, decisionID, action string, expires, now time.Time) (bool, error) {
	var claimed bool
	err := s.Atomic(ctx, func(st store.Tx) error {
		t := st.(*tx)
		if _, err := t.q.ExecContext(t.ctx, `DELETE FROM action_link_uses WHERE expires_at < $1`, now.Add(-time.Minute).UnixMilli()); err != nil {
			return err
		}
		res, err := t.q.ExecContext(t.ctx, `INSERT INTO action_link_uses (nonce, decision_id, action, used_at, expires_at) VALUES ($1, $2, $3, $4, $5)
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
	return s.Atomic(ctx, func(st store.Tx) error {
		t := st.(*tx)
		_, err := t.q.ExecContext(t.ctx, `DELETE FROM action_link_uses WHERE nonce = $1`, nonce)
		return err
	})
}

func (s *Store) ActionNonceUsed(ctx context.Context, nonce string) (bool, error) {
	var one int
	err := s.rdb.QueryRowContext(ctx, `SELECT 1 FROM action_link_uses WHERE nonce = $1`, nonce).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
