package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/SigmaUno/subspace/internal/idempotency"
	"github.com/SigmaUno/subspace/internal/store"
)

func init() {
	RegisterSchema(`
CREATE TABLE IF NOT EXISTS idempotency_keys (
  agent_id TEXT COLLATE "C" NOT NULL, key TEXT COLLATE "C" NOT NULL, tool TEXT NOT NULL, args_hash TEXT NOT NULL,
  result TEXT NOT NULL, created_at BIGINT NOT NULL, expires_at BIGINT NOT NULL,
  PRIMARY KEY (agent_id, key)
);
CREATE INDEX IF NOT EXISTS idempotency_keys_expires ON idempotency_keys(expires_at);`, "idempotency_keys")
}

var _ idempotency.Store = (*Store)(nil)

func (s *Store) GetIdempotency(ctx context.Context, agentID, key string, now time.Time) (*idempotency.Record, error) {
	var r idempotency.Record
	var res string
	var exp int64
	err := s.rdb.QueryRowContext(ctx, `SELECT tool, args_hash, result, expires_at FROM idempotency_keys
		WHERE agent_id = $1 AND key = $2 AND expires_at > $3`, agentID, key, now.UnixMilli()).Scan(&r.Tool, &r.ArgsHash, &res, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.Result, r.ExpiresAt = []byte(res), time.UnixMilli(exp).UTC()
	return &r, nil
}

func (s *Store) PutIdempotency(ctx context.Context, agentID, key string, r *idempotency.Record, now time.Time) error {
	return s.Atomic(ctx, func(st store.Tx) error {
		t := st.(*tx)
		if _, err := t.q.ExecContext(t.ctx, `DELETE FROM idempotency_keys WHERE expires_at <= $1`, now.UnixMilli()); err != nil {
			return err
		}
		_, err := t.q.ExecContext(t.ctx, `INSERT INTO idempotency_keys (agent_id, key, tool, args_hash, result, created_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT(agent_id, key) DO UPDATE SET tool = excluded.tool, args_hash = excluded.args_hash, result = excluded.result,
			created_at = excluded.created_at, expires_at = excluded.expires_at`,
			agentID, key, r.Tool, r.ArgsHash, string(r.Result), now.UnixMilli(), r.ExpiresAt.UnixMilli())
		return err
	})
}
