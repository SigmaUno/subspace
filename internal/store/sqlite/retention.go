package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/SigmaUno/subspace/internal/model"
)

const retentionSchema = `
CREATE TABLE IF NOT EXISTS retention_segments (
  id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL, seq INTEGER NOT NULL, file TEXT NOT NULL,
  row_count INTEGER NOT NULL, bytes INTEGER NOT NULL, sha256 TEXT NOT NULL, cutoff INTEGER NOT NULL,
  first_id INTEGER NOT NULL DEFAULT 0, last_id INTEGER NOT NULL DEFAULT 0,
  prev_hash TEXT NOT NULL DEFAULT '', last_hash TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  UNIQUE(kind, seq)
);
CREATE INDEX IF NOT EXISTS retention_segments_anchor ON retention_segments(kind, status, last_id);
CREATE TRIGGER IF NOT EXISTS retention_segments_no_delete BEFORE DELETE ON retention_segments
BEGIN SELECT RAISE(ABORT, 'retention manifest is append-only'); END;
CREATE TRIGGER IF NOT EXISTS retention_segments_immutable BEFORE UPDATE ON retention_segments
WHEN NEW.id IS NOT OLD.id OR NEW.kind IS NOT OLD.kind OR NEW.seq IS NOT OLD.seq OR NEW.file IS NOT OLD.file
  OR NEW.row_count IS NOT OLD.row_count OR NEW.bytes IS NOT OLD.bytes OR NEW.sha256 IS NOT OLD.sha256
  OR NEW.cutoff IS NOT OLD.cutoff OR NEW.first_id IS NOT OLD.first_id OR NEW.last_id IS NOT OLD.last_id
  OR NEW.prev_hash IS NOT OLD.prev_hash OR NEW.last_hash IS NOT OLD.last_hash OR NEW.created_at IS NOT OLD.created_at
  OR NOT ((OLD.status = 'pending' AND NEW.status IN ('pending', 'archived', 'restored'))
    OR (OLD.status = 'archived' AND NEW.status IN ('archived', 'restored'))
    OR (OLD.status = 'restored' AND NEW.status = 'restored'))
BEGIN SELECT RAISE(ABORT, 'retention manifest is immutable'); END;
`

const auditDeleteTrigger = `CREATE TRIGGER audit_no_delete BEFORE DELETE ON audit
WHEN NOT EXISTS (SELECT 1 FROM retention_segments s WHERE s.kind = 'audit' AND s.status IN ('pending', 'archived')
  AND OLD.id BETWEEN s.first_id AND s.last_id)
BEGIN SELECT RAISE(ABORT, 'audit is append-only'); END;`

func init() { RegisterSchema(retentionSchema) }

func migrateAuditDeleteTrigger(db *sql.DB) error {
	var def string
	err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = 'audit_no_delete'`).Scan(&def)
	if err == nil && strings.Contains(def, "retention_segments") {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	t, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = t.Rollback() }()
	if _, err := t.Exec(`DROP TRIGGER IF EXISTS audit_no_delete`); err != nil {
		return err
	}
	if _, err := t.Exec(auditDeleteTrigger); err != nil {
		return err
	}
	return t.Commit()
}

func (t *tx) auditAnchor() (model.AuditHead, error) {
	var h model.AuditHead
	err := t.q.QueryRowContext(t.ctx, `SELECT last_id, last_hash FROM retention_segments
		WHERE kind = 'audit' AND status IN ('pending', 'archived') ORDER BY last_id DESC LIMIT 1`).Scan(&h.ID, &h.Hash)
	if errors.Is(err, sql.ErrNoRows) {
		return model.AuditHead{}, nil
	}
	return h, err
}

func (s *Store) SetAutocheckpoint(ctx context.Context, pages int) error {
	_, err := s.wdb.ExecContext(ctx, fmt.Sprintf("PRAGMA wal_autocheckpoint = %d", pages))
	return err
}

func (s *Store) Checkpoint(ctx context.Context, truncate bool) (int, error) {
	mode := "PASSIVE"
	if truncate {
		mode = "TRUNCATE"
	}
	var busy, frames, done int
	err := s.rdb.QueryRowContext(ctx, "PRAGMA wal_checkpoint("+mode+")").Scan(&busy, &frames, &done)
	return frames, err
}

func (s *Store) Vacuum(ctx context.Context, pages int, full bool) (int, error) {
	var mode, before int
	if err := s.rdb.QueryRowContext(ctx, "PRAGMA auto_vacuum").Scan(&mode); err != nil {
		return 0, err
	}
	if err := s.rdb.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&before); err != nil {
		return 0, err
	}
	switch {
	case before == 0:
		return 0, nil
	case mode == 2:
		if err := drain(ctx, s.wdb, fmt.Sprintf("PRAGMA incremental_vacuum(%d)", pages)); err != nil {
			return 0, err
		}
	case full:
		if _, err := s.wdb.ExecContext(ctx, "PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
			return 0, err
		}
		if _, err := s.wdb.ExecContext(ctx, "VACUUM"); err != nil {
			return 0, err
		}
	default:
		return 0, nil
	}
	var after int
	if err := s.rdb.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&after); err != nil {
		return 0, err
	}
	return before - after, nil
}

func drain(ctx context.Context, db *sql.DB, q string) error {
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	for rows.Next() {
	}
	return errors.Join(rows.Err(), rows.Close())
}
