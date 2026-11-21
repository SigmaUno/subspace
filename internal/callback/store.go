package callback

import (
	"context"
	"database/sql"
	"errors"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

type Target struct {
	URL    string
	Secret string
}

type Targets interface {
	Lookup(ctx context.Context, agentID string) (Target, bool, error)
}

type sqlTx interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
}

var errNoSQL = errors.New("callback: store transaction does not expose SQL")

type SQLStore struct{ St store.Store }

func SupportsSQL(ctx context.Context, st store.Store) bool {
	ok := false
	_ = st.View(ctx, func(t store.Tx) error {
		_, ok = t.(sqlTx)
		return nil
	})
	return ok
}

func (s SQLStore) Lookup(ctx context.Context, agentID string) (Target, bool, error) {
	var (
		t     Target
		found bool
	)
	err := s.St.View(ctx, func(tx store.Tx) error {
		q, ok := tx.(sqlTx)
		if !ok {
			return errNoSQL
		}
		rows, err := q.Query(`SELECT status, callback_url, callback_secret FROM agents WHERE id = ?`, agentID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		if rows.Next() {
			var status string
			if err := rows.Scan(&status, &t.URL, &t.Secret); err != nil {
				return err
			}
			found = status == model.AgentActive && t.URL != "" && t.Secret != ""
		}
		return rows.Err()
	})
	return t, found, err
}

func (s SQLStore) Set(ctx context.Context, agentID string, t Target) error {
	return s.St.Atomic(ctx, func(tx store.Tx) error {
		q, ok := tx.(sqlTx)
		if !ok {
			return errNoSQL
		}
		res, err := q.Exec(`UPDATE agents SET callback_url = ?, callback_secret = ? WHERE id = ?`, t.URL, t.Secret, agentID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (s SQLStore) Any(ctx context.Context) (bool, error) {
	found := false
	err := s.St.View(ctx, func(tx store.Tx) error {
		q, ok := tx.(sqlTx)
		if !ok {
			return errNoSQL
		}
		rows, err := q.Query(`SELECT 1 FROM agents WHERE callback_url != '' LIMIT 1`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		found = rows.Next()
		return rows.Err()
	})
	return found, err
}
