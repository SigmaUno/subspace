package sqlite

import (
	"context"
	"time"

	"github.com/SigmaUno/subspace/internal/insights"
	"github.com/SigmaUno/subspace/internal/store"
)

func init() {
	RegisterSchema(`
CREATE TABLE IF NOT EXISTS tool_errors (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL, agent_id TEXT NOT NULL, tool TEXT NOT NULL,
  code TEXT NOT NULL, task_id TEXT NOT NULL DEFAULT '', fingerprint TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS tool_errors_ts ON tool_errors(ts);`)
}

var _ insights.Store = (*Store)(nil)

func (s *Store) InsertToolErrors(ctx context.Context, errs []insights.ToolError) error {
	return s.Atomic(ctx, func(t store.Tx) error {
		q := t.(*tx).q
		for _, e := range errs {
			if _, err := q.ExecContext(ctx, `INSERT INTO tool_errors (ts, agent_id, tool, code, task_id, fingerprint) VALUES (?, ?, ?, ?, ?, ?)`,
				e.TS.UnixMilli(), e.AgentID, e.Tool, e.Code, e.TaskID, e.Fingerprint); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) ListToolErrors(ctx context.Context, since time.Time, limit int) ([]insights.ToolError, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT id, ts, agent_id, tool, code, task_id, fingerprint FROM tool_errors
		WHERE ts >= ? ORDER BY id ASC LIMIT ?`, since.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []insights.ToolError
	for rows.Next() {
		var e insights.ToolError
		var ts int64
		if err := rows.Scan(&e.ID, &ts, &e.AgentID, &e.Tool, &e.Code, &e.TaskID, &e.Fingerprint); err != nil {
			return nil, err
		}
		e.TS = time.UnixMilli(ts).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) PruneToolErrors(ctx context.Context, before time.Time, keep int) error {
	return s.Atomic(ctx, func(t store.Tx) error {
		_, err := t.(*tx).q.ExecContext(ctx, `DELETE FROM tool_errors
			WHERE ts < ? OR id <= (SELECT COALESCE(MAX(id), 0) FROM tool_errors) - ?`, before.UnixMilli(), keep)
		return err
	})
}
