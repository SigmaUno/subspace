package webpush

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/postgres"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS push_subscriptions (
  id TEXT PRIMARY KEY, endpoint TEXT NOT NULL UNIQUE, p256dh TEXT NOT NULL, auth TEXT NOT NULL, name TEXT NOT NULL,
  expires_at INTEGER, created_at INTEGER NOT NULL, last_success_at INTEGER, failures INTEGER NOT NULL DEFAULT 0
);
`

const pgSchema = `
CREATE TABLE IF NOT EXISTS push_subscriptions (
  id TEXT COLLATE "C" PRIMARY KEY, endpoint TEXT NOT NULL UNIQUE, p256dh TEXT NOT NULL, auth TEXT NOT NULL, name TEXT NOT NULL,
  expires_at BIGINT, created_at BIGINT NOT NULL, last_success_at BIGINT, failures INTEGER NOT NULL DEFAULT 0
);
`

const (
	kvKey = "webpush_vapid_key"
	cols  = `id, endpoint, p256dh, auth, name, expires_at, created_at, last_success_at, failures`
)

func init() {
	sqlite.RegisterSchema(schema)
	postgres.RegisterSchema(pgSchema, "push_subscriptions")
}

type sqlTx interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
}

var errNoSQL = errors.New("webpush: store transaction does not expose SQL")

func withSQL(fn func(store.Tx, sqlTx) error) func(store.Tx) error {
	return func(t store.Tx) error {
		q, ok := t.(sqlTx)
		if !ok {
			return errNoSQL
		}
		return fn(t, q)
	}
}

type auditFn func(created bool) *model.AuditEvent

type db struct{ st store.Store }

func loadVAPID(ctx context.Context, st store.Store) (*VAPID, error) {
	var v *VAPID
	err := st.Atomic(ctx, func(t store.Tx) error {
		raw, ok, err := t.GetKV(kvKey)
		if err != nil {
			return err
		}
		if ok {
			v, err = ParseVAPID(raw)
			return err
		}
		if v, err = NewVAPID(); err != nil {
			return err
		}
		enc, err := v.marshal()
		if err != nil {
			return err
		}
		return t.SetKV(kvKey, enc)
	})
	return v, err
}

func ms(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixMilli()
}

func fromMS(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := time.UnixMilli(n.Int64).UTC()
	return &t
}

func scan(rows *sql.Rows) (*Subscription, error) {
	var (
		s                Subscription
		expires, success sql.NullInt64
		created          int64
	)
	if err := rows.Scan(&s.ID, &s.Endpoint, &s.p256dh, &s.auth, &s.Name, &expires, &created, &success, &s.Failures); err != nil {
		return nil, err
	}
	s.ExpiresAt, s.LastSuccessAt = fromMS(expires), fromMS(success)
	s.CreatedAt = time.UnixMilli(created).UTC()
	return &s, nil
}

func query(q sqlTx, where string, args ...any) ([]*Subscription, error) {
	rows, err := q.Query(`SELECT `+cols+` FROM push_subscriptions `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*Subscription
	for rows.Next() {
		s, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d db) list(ctx context.Context) ([]*Subscription, error) {
	var out []*Subscription
	err := d.st.View(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		var err error
		out, err = query(q, `ORDER BY length(id), id`)
		return err
	}))
	return out, err
}

func (d db) upsert(ctx context.Context, s *Subscription, limit int, audit auditFn) (bool, error) {
	var created bool
	err := d.st.Atomic(ctx, withSQL(func(t store.Tx, q sqlTx) error {
		cur, err := query(q, `WHERE endpoint = ?`, s.Endpoint)
		if err != nil {
			return err
		}
		if len(cur) > 0 {
			s.ID, s.CreatedAt = cur[0].ID, cur[0].CreatedAt
			if _, err := q.Exec(`UPDATE push_subscriptions SET p256dh = ?, auth = ?, name = ?, expires_at = ?, failures = 0 WHERE id = ?`,
				s.p256dh, s.auth, s.Name, ms(s.ExpiresAt), s.ID); err != nil {
				return err
			}
			s.LastSuccessAt = cur[0].LastSuccessAt
			return t.InsertAudit(audit(false))
		}
		all, err := query(q, ``)
		if err != nil {
			return err
		}
		if len(all) >= limit {
			return model.Conflict("at most %d push subscriptions are allowed", limit)
		}
		if s.ID, err = t.NextID("PS"); err != nil {
			return err
		}
		if _, err := q.Exec(`INSERT INTO push_subscriptions (`+cols+`) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, 0)`,
			s.ID, s.Endpoint, s.p256dh, s.auth, s.Name, ms(s.ExpiresAt), s.CreatedAt.UnixMilli()); err != nil {
			return err
		}
		created = true
		return t.InsertAudit(audit(true))
	}))
	return created, err
}

func (d db) delete(ctx context.Context, where string, arg string, audit func(*Subscription) *model.AuditEvent) error {
	return d.st.Atomic(ctx, withSQL(func(t store.Tx, q sqlTx) error {
		cur, err := query(q, `WHERE `+where+` = ?`, arg)
		if err != nil {
			return err
		}
		if len(cur) == 0 {
			return model.NotFound("push subscription not found")
		}
		if _, err := q.Exec(`DELETE FROM push_subscriptions WHERE id = ?`, cur[0].ID); err != nil {
			return err
		}
		return t.InsertAudit(audit(cur[0]))
	}))
}

func (d db) result(ctx context.Context, id string, ok bool, at time.Time) error {
	return d.st.Atomic(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		var err error
		if ok {
			_, err = q.Exec(`UPDATE push_subscriptions SET failures = 0, last_success_at = ? WHERE id = ?`, at.UnixMilli(), id)
		} else {
			_, err = q.Exec(`UPDATE push_subscriptions SET failures = failures + 1 WHERE id = ?`, id)
		}
		return err
	}))
}
