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
CREATE TABLE IF NOT EXISTS review_comments (
  task_id TEXT COLLATE "C" PRIMARY KEY, pr_url TEXT NOT NULL, comment_id BIGINT NOT NULL, message_id BIGINT NOT NULL,
  digest TEXT NOT NULL, updated_at BIGINT NOT NULL
);`, "review_comments")
}

func (s *Store) ReviewComment(ctx context.Context, taskID string) (prURL string, commentID, messageID int64, digest string, ok bool, err error) {
	err = s.rdb.QueryRowContext(ctx, `SELECT pr_url, comment_id, message_id, digest FROM review_comments WHERE task_id = $1`, taskID).
		Scan(&prURL, &commentID, &messageID, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, 0, "", false, nil
	}
	return prURL, commentID, messageID, digest, err == nil, err
}

func (s *Store) SetReviewComment(ctx context.Context, taskID, prURL string, commentID, messageID int64, digest string) error {
	return s.Atomic(ctx, func(st store.Tx) error {
		t := st.(*tx)
		_, err := t.q.ExecContext(t.ctx, `INSERT INTO review_comments (task_id, pr_url, comment_id, message_id, digest, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT(task_id) DO UPDATE SET pr_url = excluded.pr_url, comment_id = excluded.comment_id,
			  message_id = excluded.message_id, digest = excluded.digest, updated_at = excluded.updated_at`,
			taskID, prURL, commentID, messageID, digest, time.Now().UnixMilli())
		return err
	})
}
