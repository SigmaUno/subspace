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

func Each(q SQL, fn func(*sql.Rows) error, query string, args ...any) error {
	rows, err := q.Query(query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func QueryAll[T any](q SQL, scan func(*sql.Rows) (T, error), query string, args ...any) ([]T, error) {
	out := []T{}
	err := Each(q, func(rows *sql.Rows) error {
		v, err := scan(rows)
		if err == nil {
			out = append(out, v)
		}
		return err
	}, query, args...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func ScanRow(q SQL, dest []any, query string, args ...any) error {
	rows, err := q.Query(query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return err
		}
	}
	return rows.Err()
}

func Scalar[T any](q SQL, query string, args ...any) (T, error) {
	var v T
	err := ScanRow(q, []any{&v}, query, args...)
	return v, err
}

func QueryOne[T any](q SQL, scan func(*sql.Rows) (T, error), missing error, query string, args ...any) (T, error) {
	var zero T
	all, err := QueryAll(q, scan, query, args...)
	if err != nil {
		return zero, err
	}
	if len(all) == 0 {
		return zero, missing
	}
	return all[0], nil
}

func ViewSQL[T any](ctx context.Context, st Store, fn func(SQL) (T, error)) (T, error) {
	var out T
	err := st.View(ctx, WithSQL(func(_ Tx, q SQL) (err error) {
		out, err = fn(q)
		return err
	}))
	return out, err
}

func AtomicSQL[T any](ctx context.Context, st Store, fn func(SQL) (T, error)) (T, error) {
	var out T
	err := st.Atomic(ctx, WithSQL(func(_ Tx, q SQL) (err error) {
		out, err = fn(q)
		return err
	}))
	return out, err
}
