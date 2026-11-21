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
CREATE TABLE IF NOT EXISTS review_comments (
  task_id TEXT PRIMARY KEY, pr_url TEXT NOT NULL, comment_id INTEGER NOT NULL, message_id INTEGER NOT NULL,
  digest TEXT NOT NULL, updated_at INTEGER NOT NULL
);`)
}

func (s *Store) ReviewComment(ctx context.Context, taskID string) (prURL string, commentID, messageID int64, digest string, ok bool, err error) {
	err = s.rdb.QueryRowContext(ctx, `SELECT pr_url, comment_id, message_id, digest FROM review_comments WHERE task_id = ?`, taskID).
		Scan(&prURL, &commentID, &messageID, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, 0, "", false, nil
	}
	return prURL, commentID, messageID, digest, err == nil, err
}

func (s *Store) SetReviewComment(ctx context.Context, taskID, prURL string, commentID, messageID int64, digest string) error {
	return s.Atomic(ctx, func(t store.Tx) error {
		_, err := t.(*tx).q.ExecContext(ctx, `INSERT INTO review_comments (task_id, pr_url, comment_id, message_id, digest, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(task_id) DO UPDATE SET pr_url = excluded.pr_url, comment_id = excluded.comment_id,
			  message_id = excluded.message_id, digest = excluded.digest, updated_at = excluded.updated_at`,
			taskID, prURL, commentID, messageID, digest, time.Now().UnixMilli())
		return err
	})
}
