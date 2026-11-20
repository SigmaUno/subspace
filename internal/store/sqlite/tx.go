package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/SigmaUno/subspace/internal/store"
)

type querier interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

const maxCachedStmts = 512

type stmtCache struct {
	mu      sync.RWMutex
	m       map[string]*sql.Stmt
	prepare func(context.Context, string) (*sql.Stmt, error)
}

func newStmtCache(prepare func(context.Context, string) (*sql.Stmt, error)) *stmtCache {
	return &stmtCache{m: make(map[string]*sql.Stmt), prepare: prepare}
}

func (c *stmtCache) get(ctx context.Context, q string) *sql.Stmt {
	c.mu.RLock()
	st, ok := c.m[q]
	c.mu.RUnlock()
	if ok {
		return st
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if st, ok := c.m[q]; ok {
		return st
	}
	if len(c.m) >= maxCachedStmts {
		return nil
	}
	st, err := c.prepare(ctx, q)
	if err != nil {
		return nil
	}
	c.m[q] = st
	return st
}

func (c *stmtCache) close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var errs []error
	for q, st := range c.m {
		errs = append(errs, st.Close())
		delete(c.m, q)
	}
	return errors.Join(errs...)
}

type cachedQuerier struct {
	c    *stmtCache
	base querier
}

func (q cachedQuerier) ExecContext(ctx context.Context, s string, args ...any) (sql.Result, error) {
	if st := q.c.get(ctx, s); st != nil {
		return st.ExecContext(ctx, args...)
	}
	return q.base.ExecContext(ctx, s, args...)
}

func (q cachedQuerier) QueryContext(ctx context.Context, s string, args ...any) (*sql.Rows, error) {
	if st := q.c.get(ctx, s); st != nil {
		return st.QueryContext(ctx, args...)
	}
	return q.base.QueryContext(ctx, s, args...)
}

func (q cachedQuerier) QueryRowContext(ctx context.Context, s string, args ...any) *sql.Row {
	if st := q.c.get(ctx, s); st != nil {
		return st.QueryRowContext(ctx, args...)
	}
	return q.base.QueryRowContext(ctx, s, args...)
}

type tx struct {
	q   querier
	ctx context.Context
}

var _ store.Tx = (*tx)(nil)

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
