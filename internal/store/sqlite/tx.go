package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"sync"

	"github.com/SigmaUno/subspace/internal/store"
)

type querier interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

type tx struct {
	q     querier
	ctx   context.Context
	trace string
}

var _ store.Tx = (*tx)(nil)

const maxStmts = 512

type stmtCache struct {
	mu sync.RWMutex
	m  map[string]*sql.Stmt
}

func newStmtCache() *stmtCache { return &stmtCache{m: map[string]*sql.Stmt{}} }

func (c *stmtCache) get(q string) *sql.Stmt {
	c.mu.RLock()
	st := c.m[q]
	c.mu.RUnlock()
	return st
}

func (c *stmtCache) prepare(ctx context.Context, db *sql.DB, q string) *sql.Stmt {
	c.mu.RLock()
	st, full := c.m[q], len(c.m) >= maxStmts
	c.mu.RUnlock()
	if st != nil || full {
		return st
	}
	st, err := db.PrepareContext(ctx, q)
	if err != nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if cur := c.m[q]; cur != nil {
		_ = st.Close()
		return cur
	}
	c.m[q] = st
	return st
}

func (c *stmtCache) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for q, st := range c.m {
		_ = st.Close()
		delete(c.m, q)
	}
}

type readQ struct {
	db *sql.DB
	c  *stmtCache
}

func (r *readQ) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	if st := r.c.prepare(ctx, r.db, q); st != nil {
		return st.ExecContext(ctx, args...)
	}
	return r.db.ExecContext(ctx, q, args...)
}

func (r *readQ) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	if st := r.c.prepare(ctx, r.db, q); st != nil {
		return st.QueryContext(ctx, args...)
	}
	return r.db.QueryContext(ctx, q, args...)
}

func (r *readQ) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	if st := r.c.prepare(ctx, r.db, q); st != nil {
		return st.QueryRowContext(ctx, args...)
	}
	return r.db.QueryRowContext(ctx, q, args...)
}

type writeQ struct {
	tx   *sql.Tx
	c    *stmtCache
	miss map[string]struct{}
}

func (w *writeQ) stmt(ctx context.Context, q string) *sql.Stmt {
	if st := w.c.get(q); st != nil {
		return w.tx.StmtContext(ctx, st)
	}
	if w.miss == nil {
		w.miss = map[string]struct{}{}
	}
	w.miss[q] = struct{}{}
	return nil
}

func (w *writeQ) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	if st := w.stmt(ctx, q); st != nil {
		return st.ExecContext(ctx, args...)
	}
	return w.tx.ExecContext(ctx, q, args...)
}

func (w *writeQ) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	if st := w.stmt(ctx, q); st != nil {
		return st.QueryContext(ctx, args...)
	}
	return w.tx.QueryContext(ctx, q, args...)
}

func (w *writeQ) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	if st := w.stmt(ctx, q); st != nil {
		return st.QueryRowContext(ctx, args...)
	}
	return w.tx.QueryRowContext(ctx, q, args...)
}

func (t *tx) NextID(prefix string) (string, error) {
	var n int64
	err := t.q.QueryRowContext(t.ctx,
		`INSERT INTO seq(name,n) VALUES(?,1) ON CONFLICT(name) DO UPDATE SET n=n+1 RETURNING n`, prefix).Scan(&n)
	if err != nil {
		return "", err
	}
	return prefix + "-" + strconv.FormatInt(n, 10), nil
}

func ph(int) string { return "?" }

func isDup(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func mapErr(err error) error {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return store.ErrNotFound
	case isDup(err):
		return store.ErrDuplicate
	}
	return err
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

func get[T any](t *tx, scan func(store.Scanner) (T, error), q string, args ...any) (T, error) {
	v, err := scan(t.q.QueryRowContext(t.ctx, q, args...))
	return v, mapErr(err)
}

func list[T any](t *tx, scan func(store.Scanner) (T, error), q string, args ...any) ([]T, error) {
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

type where struct {
	conds []string
	args  []any
}

func (w *where) eq(col, v string) {
	if v != "" {
		w.add(col+"=?", v)
	}
}

func (w *where) add(cond string, args ...any) {
	w.conds = append(w.conds, cond)
	w.args = append(w.args, args...)
}

func (w *where) afterCreated(table, afterID string) {
	if afterID != "" {
		at := `(SELECT created_at FROM ` + table + ` WHERE id=?)`
		w.add(`(created_at > `+at+` OR (created_at = `+at+` AND id > ?))`, afterID, afterID, afterID)
	}
}

func (w *where) afterCreatedNum(table, afterID string) {
	if afterID != "" {
		n := store.IDNum(afterID)
		at := `(SELECT created_at FROM ` + table + ` WHERE id=?)`
		w.add(`(created_at > `+at+` OR (created_at = `+at+` AND (`+idNum+` > ? OR (`+idNum+` = ? AND id > ?))))`,
			afterID, afterID, n, n, afterID)
	}
}

func (w *where) clause() string {
	if len(w.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.conds, " AND ")
}

func (w *where) limit(n int) string {
	if n <= 0 {
		return ""
	}
	w.args = append(w.args, n)
	return " LIMIT ?"
}
