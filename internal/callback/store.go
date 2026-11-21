package callback

import (
	"context"

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

type SQLStore struct{ St store.Store }

func (s SQLStore) Lookup(ctx context.Context, agentID string) (Target, bool, error) {
	var (
		t     Target
		found bool
	)
	err := s.St.View(ctx, store.WithSQL(func(_ store.Tx, q store.SQL) error {
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
	}))
	return t, found, err
}

func (s SQLStore) Set(ctx context.Context, agentID string, t Target) error {
	return s.St.Atomic(ctx, store.WithSQL(func(_ store.Tx, q store.SQL) error {
		res, err := q.Exec(`UPDATE agents SET callback_url = ?, callback_secret = ? WHERE id = ?`, t.URL, t.Secret, agentID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return store.ErrNotFound
		}
		return nil
	}))
}

func (s SQLStore) Any(ctx context.Context) (bool, error) {
	found := false
	err := s.St.View(ctx, store.WithSQL(func(_ store.Tx, q store.SQL) error {
		rows, err := q.Query(`SELECT 1 FROM agents WHERE callback_url != '' LIMIT 1`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		found = rows.Next()
		return rows.Err()
	}))
	return found, err
}
