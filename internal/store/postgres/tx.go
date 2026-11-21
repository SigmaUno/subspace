package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SigmaUno/subspace/internal/store"
)

type querier interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

type tx struct {
	q   querier
	ctx context.Context
}

var _ store.Tx = (*tx)(nil)

type scanner interface {
	Scan(dest ...any) error
}

func (t *tx) NextID(prefix string) (string, error) {
	var n int64
	err := t.q.QueryRowContext(t.ctx,
		`INSERT INTO seq(name, n) VALUES($1, 1) ON CONFLICT(name) DO UPDATE SET n = seq.n + 1 RETURNING n`, prefix).Scan(&n)
	if err != nil {
		return "", err
	}
	return prefix + "-" + strconv.FormatInt(n, 10), nil
}

func (t *tx) Exec(q string, args ...any) (sql.Result, error) {
	return t.q.ExecContext(t.ctx, rebind(q), args...)
}

func (t *tx) Query(q string, args ...any) (*sql.Rows, error) {
	return t.q.QueryContext(t.ctx, rebind(q), args...)
}

func rebind(q string) string {
	if !strings.Contains(q, "?") {
		return q
	}
	var b strings.Builder
	n := 0
	quoted := false
	for _, r := range q {
		switch {
		case r == '\'':
			quoted = !quoted
		case r == '?' && !quoted:
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (t *tx) exec(q string, args ...any) error {
	_, err := t.q.ExecContext(t.ctx, q, args...)
	return mapErr(err)
}

func (t *tx) update(q string, args ...any) error {
	res, err := t.q.ExecContext(t.ctx, q, args...)
	if err != nil {
		return mapErr(err)
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

func list[T any](t *tx, scan func(scanner) (T, error), q string, args ...any) ([]T, error) {
	rows, err := t.q.QueryContext(t.ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return store.ErrDuplicate
	}
	return err
}

type args struct {
	where []string
	vals  []any
}

func (a *args) add(v any) string {
	a.vals = append(a.vals, v)
	return "$" + strconv.Itoa(len(a.vals))
}

func (a *args) eq(col, v string) {
	if v != "" {
		a.where = append(a.where, col+" = "+a.add(v))
	}
}

func (a *args) afterCreated(table, afterID string) {
	if afterID == "" {
		return
	}
	id := a.add(afterID)
	at := "(SELECT created_at FROM " + table + " WHERE id = " + id + ")"
	a.where = append(a.where, "(created_at > "+at+" OR (created_at = "+at+" AND id > "+id+"))")
}

func (a *args) afterCreatedNum(table, afterID string) {
	if afterID == "" {
		return
	}
	id := a.add(afterID)
	at := "(SELECT created_at FROM " + table + " WHERE id = " + id + ")"
	n := a.add(store.IDNum(afterID))
	a.where = append(a.where, "(created_at > "+at+" OR (created_at = "+at+" AND ("+idNum+" > "+n+" OR ("+idNum+" = "+n+" AND id > "+id+"))))")
}

func (a *args) limit(n int) string {
	if n <= 0 {
		return ""
	}
	return " LIMIT " + a.add(n)
}

func (a *args) clause() string {
	if len(a.where) == 0 {
		return ""
	}
	s := " WHERE " + a.where[0]
	for _, w := range a.where[1:] {
		s += " AND " + w
	}
	return s
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func msPtr(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return ms(*t)
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

func fromMSPtr(v int64) *time.Time {
	if v == 0 {
		return nil
	}
	t := time.UnixMilli(v).UTC()
	return &t
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func toJSON(v []string) (string, error) {
	if v == nil {
		v = []string{}
	}
	b, err := json.Marshal(v)
	return string(b), err
}

func fromJSON(b []byte) ([]string, error) {
	out := []string{}
	if len(b) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}
