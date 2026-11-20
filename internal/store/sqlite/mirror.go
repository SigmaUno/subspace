package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/SigmaUno/subspace/internal/store"
)

func init() {
	extraSchemas = append(extraSchemas, `
CREATE TABLE IF NOT EXISTS mirror_issues (
  task_id TEXT PRIMARY KEY, repo TEXT NOT NULL, url TEXT NOT NULL, digest TEXT NOT NULL, updated_at INTEGER NOT NULL
);`)
}

func (s *Store) MirrorLink(ctx context.Context, taskID string) (repo, url, digest string, ok bool, err error) {
	err = s.rdb.QueryRowContext(ctx, `SELECT repo, url, digest FROM mirror_issues WHERE task_id = ?`, taskID).Scan(&repo, &url, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", false, nil
	}
	return repo, url, digest, err == nil, err
}

func (s *Store) SetMirrorLink(ctx context.Context, taskID, repo, url, digest string) error {
	return s.Atomic(ctx, func(t store.Tx) error {
		_, err := t.(*tx).q.ExecContext(ctx, `INSERT INTO mirror_issues (task_id, repo, url, digest, updated_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(task_id) DO UPDATE SET repo = excluded.repo, url = excluded.url, digest = excluded.digest, updated_at = excluded.updated_at`,
			taskID, repo, url, digest, time.Now().UnixMilli())
		return err
	})
}
