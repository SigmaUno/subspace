package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/SigmaUno/subspace/internal/store"
)

func init() {
	RegisterSchema(`
CREATE TABLE IF NOT EXISTS pr_status (
  task_id TEXT PRIMARY KEY, data TEXT NOT NULL, updated_at INTEGER NOT NULL
);`)
}

func (s *Store) PRStatus(ctx context.Context, taskID string) ([]byte, bool, error) {
	var data string
	err := s.rdb.QueryRowContext(ctx, `SELECT data FROM pr_status WHERE task_id = ?`, taskID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return []byte(data), true, nil
}

func (s *Store) PRStatuses(ctx context.Context) ([][]byte, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT data FROM pr_status ORDER BY length(task_id), task_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out [][]byte
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		out = append(out, []byte(data))
	}
	return out, rows.Err()
}

func (s *Store) SetPRStatus(ctx context.Context, taskID string, data []byte) error {
	return s.Atomic(ctx, func(t store.Tx) error {
		_, err := t.(*tx).q.ExecContext(ctx, `INSERT INTO pr_status (task_id, data, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(task_id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
			taskID, string(data), time.Now().UnixMilli())
		return err
	})
}
