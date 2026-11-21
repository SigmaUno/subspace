package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/tokens"
)

func init() {
	extraSchemas = append(extraSchemas, `
CREATE TABLE IF NOT EXISTS api_tokens (
  seq INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, hash TEXT NOT NULL UNIQUE, scope TEXT NOT NULL,
  created_at INTEGER NOT NULL, last_used_at INTEGER, revoked_at INTEGER, expires_at INTEGER
);`)
}

const apiAccessCols = `seq, name, scope, created_at, last_used_at, revoked_at, expires_at`

var _ tokens.Store = (*Store)(nil)

func apiTokenSeq(id string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimPrefix(id, "K-"), 10, 64)
	return n, err == nil && strings.HasPrefix(id, "K-") && n > 0
}

func msPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixMilli()
}

func timePtr(ms sql.NullInt64) *time.Time {
	if !ms.Valid {
		return nil
	}
	t := time.UnixMilli(ms.Int64).UTC()
	return &t
}

func scanAPIToken(row interface{ Scan(...any) error }) (*tokens.Token, error) {
	var (
		t                    tokens.Token
		seq, created         int64
		used, revoked, until sql.NullInt64
	)
	if err := row.Scan(&seq, &t.Name, &t.Scope, &created, &used, &revoked, &until); err != nil {
		return nil, err
	}
	t.ID = "K-" + strconv.FormatInt(seq, 10)
	t.CreatedAt = time.UnixMilli(created).UTC()
	t.LastUsedAt, t.RevokedAt, t.ExpiresAt = timePtr(used), timePtr(revoked), timePtr(until)
	return &t, nil
}

func (s *Store) CreateAPIToken(ctx context.Context, t *tokens.Token, hash string, audit tokens.AuditFunc) error {
	return s.Atomic(ctx, func(st store.Tx) error {
		res, err := st.(*tx).Exec(`INSERT INTO api_tokens (name, hash, scope, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
			t.Name, hash, t.Scope, t.CreatedAt.UnixMilli(), msPtr(t.ExpiresAt))
		if err != nil {
			return err
		}
		seq, err := res.LastInsertId()
		if err != nil {
			return err
		}
		t.ID = "K-" + strconv.FormatInt(seq, 10)
		return st.InsertAudit(audit(t))
	})
}

func (s *Store) ListAPITokens(ctx context.Context) ([]*tokens.Token, error) {
	rows, err := s.rq.QueryContext(ctx, `SELECT `+apiAccessCols+` FROM api_tokens ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*tokens.Token
	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) APITokenByHash(ctx context.Context, hash string) (*tokens.Token, error) {
	t, err := scanAPIToken(s.rq.QueryRowContext(ctx, `SELECT `+apiAccessCols+` FROM api_tokens WHERE hash = ?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

func (s *Store) RevokeAPIToken(ctx context.Context, id string, at time.Time, audit tokens.AuditFunc) (*tokens.Token, error) {
	seq, ok := apiTokenSeq(id)
	if !ok {
		return nil, store.ErrNotFound
	}
	var out *tokens.Token
	err := s.Atomic(ctx, func(st store.Tx) error {
		t := st.(*tx)
		cur, err := scanAPIToken(t.q.QueryRowContext(t.ctx, `SELECT `+apiAccessCols+` FROM api_tokens WHERE seq = ?`, seq))
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		if cur.RevokedAt != nil {
			return tokens.ErrRevoked
		}
		if _, err := t.Exec(`UPDATE api_tokens SET revoked_at = ? WHERE seq = ?`, at.UnixMilli(), seq); err != nil {
			return err
		}
		cur.RevokedAt = &at
		out = cur
		return st.InsertAudit(audit(cur))
	})
	return out, err
}

func (s *Store) TouchAPIToken(ctx context.Context, id string, at time.Time) error {
	seq, ok := apiTokenSeq(id)
	if !ok {
		return store.ErrNotFound
	}
	return s.Atomic(ctx, func(st store.Tx) error {
		_, err := st.(*tx).Exec(`UPDATE api_tokens SET last_used_at = ? WHERE seq = ? AND (last_used_at IS NULL OR last_used_at < ?)`,
			at.UnixMilli(), seq, at.UnixMilli())
		return err
	})
}
