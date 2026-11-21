package webhookout

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/postgres"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS outbound_webhooks (
  id TEXT PRIMARY KEY, url TEXT NOT NULL, events TEXT NOT NULL, brief_id TEXT, secret TEXT NOT NULL, created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS outbound_webhook_deliveries (
  id TEXT PRIMARY KEY, webhook_id TEXT NOT NULL, event TEXT NOT NULL, payload TEXT NOT NULL, state TEXT NOT NULL,
  attempts INTEGER NOT NULL, next_at INTEGER NOT NULL, last_status INTEGER NOT NULL, last_error TEXT NOT NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, delivered_at INTEGER
);
CREATE INDEX IF NOT EXISTS outbound_webhook_deliveries_due ON outbound_webhook_deliveries(state, next_at);
CREATE INDEX IF NOT EXISTS outbound_webhook_deliveries_hook ON outbound_webhook_deliveries(webhook_id, state);
`

const pgSchema = `
CREATE TABLE IF NOT EXISTS outbound_webhooks (
  id TEXT COLLATE "C" PRIMARY KEY, url TEXT NOT NULL, events TEXT NOT NULL, brief_id TEXT, secret TEXT NOT NULL, created_at BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS outbound_webhook_deliveries (
  id TEXT COLLATE "C" PRIMARY KEY, webhook_id TEXT COLLATE "C" NOT NULL, event TEXT NOT NULL, payload TEXT NOT NULL, state TEXT NOT NULL,
  attempts INTEGER NOT NULL, next_at BIGINT NOT NULL, last_status INTEGER NOT NULL, last_error TEXT NOT NULL,
  created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL, delivered_at BIGINT
);
CREATE INDEX IF NOT EXISTS outbound_webhook_deliveries_due ON outbound_webhook_deliveries(state, next_at);
CREATE INDEX IF NOT EXISTS outbound_webhook_deliveries_hook ON outbound_webhook_deliveries(webhook_id, state);
`

func init() {
	sqlite.RegisterSchema(schema)
	postgres.RegisterSchema(pgSchema, "outbound_webhooks", "outbound_webhook_deliveries")
}

const (
	subCols = `s.id, s.url, s.events, s.brief_id, s.secret, s.created_at,
  (SELECT COUNT(*) FROM outbound_webhook_deliveries d WHERE d.webhook_id = s.id AND d.state = 'pending'),
  (SELECT COUNT(*) FROM outbound_webhook_deliveries d WHERE d.webhook_id = s.id AND d.state = 'delivered'),
  (SELECT COUNT(*) FROM outbound_webhook_deliveries d WHERE d.webhook_id = s.id AND d.state = 'dead')`
	delCols = `d.id, d.webhook_id, d.event, d.payload, d.state, d.attempts, d.next_at, d.last_status, d.last_error, d.created_at, d.updated_at, d.delivered_at`
)

type Store interface {
	CreateSub(ctx context.Context, s *Subscription, max int, audit AuditFn) error
	ListSubs(ctx context.Context) ([]*Subscription, error)
	GetSub(ctx context.Context, id string) (*Subscription, error)
	DeleteSub(ctx context.Context, id string, audit AuditFn) error
	Enqueue(ctx context.Context, d *Delivery, build func(id string) string, maxPending int) error
	Due(ctx context.Context, now time.Time, limit int) ([]*Due, error)
	Complete(ctx context.Context, d *Delivery, keep int) error
	Deliveries(ctx context.Context, subID, state string, limit int) ([]*Delivery, error)
	Requeue(ctx context.Context, subID, id string, now time.Time, audit AuditFn) (*Delivery, error)
}

type sqlTx interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
}

var errNoSQL = errors.New("webhookout: store transaction does not expose SQL")

func withSQL(fn func(store.Tx, sqlTx) error) func(store.Tx) error {
	return func(t store.Tx) error {
		q, ok := t.(sqlTx)
		if !ok {
			return errNoSQL
		}
		return fn(t, q)
	}
}

type SQLStore struct{ st store.Store }

func NewSQLStore(st store.Store) *SQLStore { return &SQLStore{st: st} }

func count(q sqlTx, query string, args ...any) (int, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	var n int
	if rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return 0, err
		}
	}
	return n, rows.Err()
}

func (s *SQLStore) CreateSub(ctx context.Context, sub *Subscription, max int, audit AuditFn) error {
	events, err := json.Marshal(sub.Events)
	if err != nil {
		return err
	}
	return s.st.Atomic(ctx, withSQL(func(t store.Tx, q sqlTx) error {
		n, err := count(q, `SELECT COUNT(*) FROM outbound_webhooks`)
		if err != nil {
			return err
		}
		if n >= max {
			return model.Conflict("at most %d outbound webhooks are allowed", max)
		}
		id, err := t.NextID("WH")
		if err != nil {
			return err
		}
		if _, err := q.Exec(`INSERT INTO outbound_webhooks (id, url, events, brief_id, secret, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, sub.URL, string(events), sub.BriefID, sub.Secret, sub.CreatedAt.UnixMilli()); err != nil {
			return err
		}
		sub.ID = id
		return t.InsertAudit(audit())
	}))
}

func scanSub(rows *sql.Rows) (*Subscription, error) {
	var (
		sub     Subscription
		events  string
		brief   sql.NullString
		created int64
	)
	if err := rows.Scan(&sub.ID, &sub.URL, &events, &brief, &sub.Secret, &created, &sub.Pending, &sub.Delivered, &sub.Dead); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(events), &sub.Events); err != nil {
		return nil, err
	}
	if brief.Valid {
		sub.BriefID = &brief.String
	}
	sub.CreatedAt = time.UnixMilli(created).UTC()
	return &sub, nil
}

func (s *SQLStore) subs(ctx context.Context, where string, args ...any) ([]*Subscription, error) {
	var out []*Subscription
	err := s.st.View(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		rows, err := q.Query(`SELECT `+subCols+` FROM outbound_webhooks s `+where, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			sub, err := scanSub(rows)
			if err != nil {
				return err
			}
			out = append(out, sub)
		}
		return rows.Err()
	}))
	return out, err
}

func (s *SQLStore) ListSubs(ctx context.Context) ([]*Subscription, error) {
	return s.subs(ctx, `ORDER BY length(s.id), s.id`)
}

func (s *SQLStore) GetSub(ctx context.Context, id string) (*Subscription, error) {
	subs, err := s.subs(ctx, `WHERE s.id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(subs) == 0 {
		return nil, model.NotFound("webhook %s not found", id)
	}
	return subs[0], nil
}

func (s *SQLStore) DeleteSub(ctx context.Context, id string, audit AuditFn) error {
	return s.st.Atomic(ctx, withSQL(func(t store.Tx, q sqlTx) error {
		res, err := q.Exec(`DELETE FROM outbound_webhooks WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return errors.Join(err, model.NotFound("webhook %s not found", id))
		}
		if _, err := q.Exec(`DELETE FROM outbound_webhook_deliveries WHERE webhook_id = ?`, id); err != nil {
			return err
		}
		return t.InsertAudit(audit())
	}))
}

func (s *SQLStore) Enqueue(ctx context.Context, d *Delivery, build func(id string) string, maxPending int) error {
	return s.st.Atomic(ctx, withSQL(func(t store.Tx, q sqlTx) error {
		exists, err := count(q, `SELECT COUNT(*) FROM outbound_webhooks WHERE id = ?`, d.WebhookID)
		if err != nil {
			return err
		}
		if exists == 0 {
			return model.NotFound("webhook %s not found", d.WebhookID)
		}
		pending, err := count(q, `SELECT COUNT(*) FROM outbound_webhook_deliveries WHERE webhook_id = ? AND state = 'pending'`, d.WebhookID)
		if err != nil {
			return err
		}
		id, err := t.NextID("WD")
		if err != nil {
			return err
		}
		d.ID = id
		d.Payload = json.RawMessage(build(id))
		d.Attempts = 0
		d.UpdatedAt = d.CreatedAt
		d.State, d.NextAttemptAt, d.LastError = StatePending, &d.CreatedAt, ""
		if pending >= maxPending {
			d.State, d.NextAttemptAt, d.LastError = StateDead, nil, "queue full"
		}
		_, err = q.Exec(`INSERT INTO outbound_webhook_deliveries (id, webhook_id, event, payload, state, attempts, next_at, last_status, last_error, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 0, ?, 0, ?, ?, ?)`,
			d.ID, d.WebhookID, d.Event, string(d.Payload), d.State, nextMS(d.NextAttemptAt), d.LastError, d.CreatedAt.UnixMilli(), d.UpdatedAt.UnixMilli())
		return err
	}))
}

func nextMS(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.UnixMilli()
}

func scanDelivery(rows *sql.Rows, extra ...any) (*Delivery, error) {
	var (
		d                  Delivery
		payload            string
		next, created, upd int64
		status             int
		delivered          sql.NullInt64
	)
	dest := append([]any{&d.ID, &d.WebhookID, &d.Event, &payload, &d.State, &d.Attempts, &next, &status, &d.LastError, &created, &upd, &delivered}, extra...)
	if err := rows.Scan(dest...); err != nil {
		return nil, err
	}
	d.Payload = json.RawMessage(payload)
	d.CreatedAt, d.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(upd).UTC()
	if d.State == StatePending {
		t := time.UnixMilli(next).UTC()
		d.NextAttemptAt = &t
	}
	if status != 0 {
		d.LastStatus = &status
	}
	if delivered.Valid {
		t := time.UnixMilli(delivered.Int64).UTC()
		d.DeliveredAt = &t
	}
	return &d, nil
}

func (s *SQLStore) Due(ctx context.Context, now time.Time, limit int) ([]*Due, error) {
	var out []*Due
	err := s.st.View(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		rows, err := q.Query(`SELECT `+delCols+`, s.url, s.secret FROM outbound_webhook_deliveries d JOIN outbound_webhooks s ON s.id = d.webhook_id
			WHERE d.state = 'pending' AND d.next_at <= ? ORDER BY d.next_at, length(d.id), d.id LIMIT ?`, now.UnixMilli(), limit)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var u Due
			d, err := scanDelivery(rows, &u.URL, &u.Secret)
			if err != nil {
				return err
			}
			u.Delivery = *d
			out = append(out, &u)
		}
		return rows.Err()
	}))
	return out, err
}

func (s *SQLStore) Complete(ctx context.Context, d *Delivery, keep int) error {
	var status int
	if d.LastStatus != nil {
		status = *d.LastStatus
	}
	return s.st.Atomic(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		res, err := q.Exec(`UPDATE outbound_webhook_deliveries SET state = ?, attempts = ?, next_at = ?, last_status = ?, last_error = ?, updated_at = ?, delivered_at = ?
			WHERE id = ? AND state = 'pending'`,
			d.State, d.Attempts, nextMS(d.NextAttemptAt), status, d.LastError, d.UpdatedAt.UnixMilli(), msPtr(d.DeliveredAt), d.ID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 || d.State == StatePending {
			return err
		}
		_, err = q.Exec(`DELETE FROM outbound_webhook_deliveries WHERE webhook_id = ? AND state <> 'pending' AND id NOT IN
			(SELECT id FROM outbound_webhook_deliveries WHERE webhook_id = ? AND state <> 'pending' ORDER BY length(id) DESC, id DESC LIMIT ?)`,
			d.WebhookID, d.WebhookID, keep)
		return err
	}))
}

func msPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixMilli()
}

func (s *SQLStore) Deliveries(ctx context.Context, subID, state string, limit int) ([]*Delivery, error) {
	if _, err := s.GetSub(ctx, subID); err != nil {
		return nil, err
	}
	query, args := `WHERE d.webhook_id = ?`, []any{subID}
	if state != "" {
		query += ` AND d.state = ?`
		args = append(args, state)
	}
	args = append(args, limit)
	out := []*Delivery{}
	err := s.st.View(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		rows, err := q.Query(`SELECT `+delCols+` FROM outbound_webhook_deliveries d `+query+` ORDER BY length(d.id) DESC, d.id DESC LIMIT ?`, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			d, err := scanDelivery(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	}))
	return out, err
}

func (s *SQLStore) Requeue(ctx context.Context, subID, id string, now time.Time, audit AuditFn) (*Delivery, error) {
	var out *Delivery
	err := s.st.Atomic(ctx, withSQL(func(t store.Tx, q sqlTx) error {
		rows, err := q.Query(`SELECT `+delCols+` FROM outbound_webhook_deliveries d WHERE d.id = ? AND d.webhook_id = ?`, id, subID)
		if err != nil {
			return err
		}
		var cur *Delivery
		if rows.Next() {
			cur, err = scanDelivery(rows)
		}
		if cerr := rows.Close(); err == nil {
			err = errors.Join(cerr, rows.Err())
		}
		if err != nil {
			return err
		}
		if cur == nil {
			return model.NotFound("delivery %s not found", id)
		}
		if cur.State != StateDead {
			return model.Conflict("delivery %s is %s, only dead deliveries can be redelivered", id, cur.State)
		}
		if _, err := q.Exec(`UPDATE outbound_webhook_deliveries SET state = 'pending', attempts = 0, next_at = ?, last_status = 0, last_error = '',
			created_at = ?, updated_at = ?, delivered_at = NULL WHERE id = ?`, now.UnixMilli(), now.UnixMilli(), now.UnixMilli(), id); err != nil {
			return err
		}
		cur.State, cur.Attempts, cur.NextAttemptAt, cur.LastStatus, cur.LastError = StatePending, 0, &now, nil, ""
		cur.CreatedAt, cur.UpdatedAt = now, now
		out = cur
		return t.InsertAudit(audit())
	}))
	return out, err
}
