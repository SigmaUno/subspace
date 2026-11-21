package store

import (
	"context"
	"database/sql"
	"errors"
)

type SQL interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
}

var ErrNoSQL = errors.New("store transaction does not expose SQL")

func SQLOf(tx Tx) (SQL, error) {
	q, ok := tx.(SQL)
	if !ok {
		return nil, ErrNoSQL
	}
	return q, nil
}

func WithSQL(fn func(Tx, SQL) error) func(Tx) error {
	return func(tx Tx) error {
		q, err := SQLOf(tx)
		if err != nil {
			return err
		}
		return fn(tx, q)
	}
}

func SupportsSQL(ctx context.Context, st Store) bool {
	ok := false
	_ = st.View(ctx, func(tx Tx) error {
		_, ok = tx.(SQL)
		return nil
	})
	return ok
}
