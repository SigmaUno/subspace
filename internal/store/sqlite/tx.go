package sqlite

import (
	"context"
	"database/sql"
	"sync"

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
	return prefix + "-" + itoa(n), nil
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
