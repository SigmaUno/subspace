package drill

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/postgres"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS restore_drills (
  id TEXT PRIMARY KEY, status TEXT NOT NULL, trigger_kind TEXT NOT NULL, backup TEXT NOT NULL DEFAULT '',
  backup_bytes INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '', checks TEXT NOT NULL DEFAULT '[]',
  started_at INTEGER NOT NULL, finished_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS restore_drills_started ON restore_drills(started_at);`

const pgSchema = `
CREATE TABLE IF NOT EXISTS restore_drills (
  id TEXT COLLATE "C" PRIMARY KEY, status TEXT NOT NULL, trigger_kind TEXT NOT NULL, backup TEXT NOT NULL DEFAULT '',
  backup_bytes BIGINT NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '', checks TEXT NOT NULL DEFAULT '[]',
  started_at BIGINT NOT NULL, finished_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS restore_drills_started ON restore_drills(started_at);`

const cols = `id, status, trigger_kind, backup, backup_bytes, error, checks, started_at, finished_at`

func init() {
	sqlite.RegisterSchema(schema)
	postgres.RegisterSchema(pgSchema, "restore_drills")
}

type sqlTx interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
}

var errNoSQL = errors.New("drill: store transaction does not expose SQL")

func sqlOf(tx store.Tx) (sqlTx, error) {
	q, ok := tx.(sqlTx)
	if !ok {
		return nil, errNoSQL
	}
	return q, nil
}

func SupportsSQL(ctx context.Context, st store.Store) bool {
	ok := false
	_ = st.View(ctx, func(tx store.Tx) error {
		_, ok = tx.(sqlTx)
		return nil
	})
	return ok
}

func query(tx store.Tx, tail string, args ...any) ([]*Result, error) {
	q, err := sqlOf(tx)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(`SELECT `+cols+` FROM restore_drills `+tail, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*Result{}
	for rows.Next() {
		var (
			r                 Result
			checks            string
			started, finished int64
		)
		if err := rows.Scan(&r.ID, &r.Status, &r.Trigger, &r.Backup, &r.BackupBytes, &r.Error, &checks, &started, &finished); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(checks), &r.Checks); err != nil {
			return nil, err
		}
		r.StartedAt = time.UnixMilli(started).UTC()
		r.FinishedAt = time.UnixMilli(finished).UTC()
		out = append(out, &r)
	}
	return out, rows.Err()
}

func insert(tx store.Tx, r *Result) error {
	q, err := sqlOf(tx)
	if err != nil {
		return err
	}
	checks, err := json.Marshal(r.Checks)
	if err != nil {
		return err
	}
	_, err = q.Exec(`INSERT INTO restore_drills (`+cols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Status, r.Trigger, r.Backup, r.BackupBytes, r.Error, string(checks), r.StartedAt.UnixMilli(), r.FinishedAt.UnixMilli())
	return err
}

func (s *Service) List(ctx context.Context, limit int) ([]*Result, error) {
	var out []*Result
	err := s.o.Store.View(ctx, func(tx store.Tx) (err error) {
		out, err = query(tx, `ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
		return
	})
	return out, err
}

func (s *Service) latest(ctx context.Context) (*Result, error) {
	rs, err := s.List(ctx, 1)
	if err != nil || len(rs) == 0 {
		return nil, err
	}
	return rs[0], nil
}

func (s *Service) record(ctx context.Context, r *Result) error {
	return s.o.Store.Atomic(ctx, func(tx store.Tx) error {
		id, err := tx.NextID(idPrefix)
		if err != nil {
			return err
		}
		r.ID = id
		if err := insert(tx, r); err != nil {
			return err
		}
		note := r.Trigger + " drill"
		if r.Trigger == TriggerManual {
			note = auth.AuditNote(ctx, note)
		}
		after := map[string]any{"id": r.ID, "status": r.Status, "backup": r.Backup, "trigger": r.Trigger}
		if r.Status == StatusFailed {
			after["failed_check"] = r.failedCheck()
			after["error"] = r.Error
		}
		return tx.InsertAudit(&model.AuditEvent{
			TS: r.FinishedAt, AgentID: "system", Role: "system", Tool: "restore_drill", After: after, Note: &note,
		})
	})
}
