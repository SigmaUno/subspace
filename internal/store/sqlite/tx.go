package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/SigmaUno/subspace/internal/store"
)

// errNotImplemented marks store methods still to be written (see split.md).
var errNotImplemented = errors.New("sqlite: not implemented")

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
