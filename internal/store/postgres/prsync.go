package postgres

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
  task_id TEXT COLLATE "C" PRIMARY KEY, data TEXT NOT NULL, updated_at BIGINT NOT NULL
);`, "pr_status")
}

func (s *Store) PRStatus(ctx context.Context, taskID string) ([]byte, bool, error) {
	var data string
	err := s.rdb.QueryRowContext(ctx, `SELECT data FROM pr_status WHERE task_id = $1`, taskID).Scan(&data)
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

func (s *Store) PRStatusesOf(ctx context.Context, ids []string) (map[string][]byte, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.rdb.QueryContext(ctx, `SELECT task_id, data FROM pr_status WHERE task_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]byte{}
	for rows.Next() {
		var id, data string
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		out[id] = []byte(data)
	}
	return out, rows.Err()
}

func (s *Store) SetPRStatus(ctx context.Context, taskID string, data []byte) error {
	return s.Atomic(ctx, func(st store.Tx) error {
		t := st.(*tx)
		_, err := t.q.ExecContext(t.ctx, `INSERT INTO pr_status (task_id, data, updated_at) VALUES ($1, $2, $3)
			ON CONFLICT(task_id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
			taskID, string(data), time.Now().UnixMilli())
		return err
	})
}
