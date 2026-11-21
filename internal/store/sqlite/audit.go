package sqlite

import (
	"database/sql"
	"errors"
	"math"

	"github.com/SigmaUno/subspace/internal/audit"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const auditDefaultLimit = 200

var (
	spawnSelect = `SELECT ` + store.Spawns.Select("") + ` FROM spawns`
	spawnInsert = store.Spawns.Insert(ph)
	spawnUpdate = store.Spawns.Update(ph)
)

func (t *tx) InsertAudit(e *model.AuditEvent) error {
	before, err := store.AuditJSON(e.Before)
	if err != nil {
		return err
	}
	after, err := store.AuditJSON(e.After)
	if err != nil {
		return err
	}
	head, err := t.AuditHead(math.MaxInt64)
	if err != nil {
		return err
	}
	rec := audit.Record{ID: head.ID + 1, TS: store.Millis(e.TS), AgentID: e.AgentID, Role: e.Role, Tool: e.Tool,
		TaskID: store.Str(e.TaskID), Before: before, After: after, Note: store.TraceNote(store.Str(e.Note), t.trace)}
	hash, err := audit.Link(head.Hash, rec)
	if err != nil {
		return err
	}
	_, err = t.q.ExecContext(t.ctx,
		`INSERT INTO audit(`+store.AuditCols+`) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		rec.ID, rec.TS, rec.AgentID, rec.Role, rec.Tool, rec.TaskID, rec.Before, rec.After, rec.Note, hash)
	if err != nil {
		return err
	}
	e.ID, e.Hash = rec.ID, hash
	return nil
}

func (t *tx) AuditHead(maxID int64) (model.AuditHead, error) {
	var h model.AuditHead
	err := t.q.QueryRowContext(t.ctx, `SELECT id, hash FROM audit WHERE id <= ? ORDER BY id DESC LIMIT 1`, maxID).Scan(&h.ID, &h.Hash)
	if errors.Is(err, sql.ErrNoRows) {
		return t.auditAnchor()
	}
	return h, err
}

func (t *tx) VerifyAudit() (model.AuditCheck, error) {
	anchor, err := t.auditAnchor()
	if err != nil {
		return model.AuditCheck{}, err
	}
	rows, err := t.q.QueryContext(t.ctx, `SELECT `+store.AuditCols+` FROM audit WHERE id > ? ORDER BY id`, anchor.ID)
	if err != nil {
		return model.AuditCheck{}, err
	}
	defer func() { _ = rows.Close() }()
	return verifyRows(rows, anchor)
}

func verifyRows(rows *sql.Rows, anchor model.AuditHead) (model.AuditCheck, error) {
	c := audit.Chain{HeadID: anchor.ID, HeadHash: anchor.Hash}
	for rows.Next() {
		var (
			r    audit.Record
			hash string
		)
		if err := rows.Scan(&r.ID, &r.TS, &r.AgentID, &r.Role, &r.Tool, &r.TaskID, &r.Before, &r.After, &r.Note, &hash); err != nil {
			return model.AuditCheck{}, err
		}
		if !c.Add(r, hash) {
			return chainCheck(c, r.ID), rows.Err()
		}
	}
	return chainCheck(c, 0), rows.Err()
}

func chainCheck(c audit.Chain, broken int64) model.AuditCheck {
	return model.AuditCheck{OK: broken == 0, Events: c.Events, BrokenID: broken,
		Head: model.AuditHead{ID: c.HeadID, Hash: c.HeadHash}}
}

func (t *tx) ListAudit(f store.AuditFilter) ([]*model.AuditEvent, error) {
	w := where{conds: []string{"id > ?"}, args: []any{f.SinceID}}
	w.eq("agent_id", f.AgentID)
	w.eq("task_id", f.TaskID)
	w.eq("tool", f.Tool)
	limit := f.Limit
	if limit <= 0 {
		limit = auditDefaultLimit
	}
	q := `SELECT ` + store.AuditCols + ` FROM audit` + w.clause() + ` ORDER BY id ASC` + w.limit(limit)
	return list(t, store.ScanAudit, q, w.args...)
}

func (t *tx) InsertSpawn(s *model.Spawn) error {
	args, err := store.Spawns.InsertArgs(s)
	if err != nil {
		return err
	}
	return t.exec(spawnInsert, args...)
}

func (t *tx) UpdateSpawn(s *model.Spawn) error {
	args, err := store.Spawns.UpdateArgs(s)
	if err != nil {
		return err
	}
	return t.update(spawnUpdate, args...)
}

func (t *tx) ListSpawns(f store.SpawnFilter) ([]*model.Spawn, error) {
	var w where
	w.eq("brief_id", f.BriefID)
	w.afterCreatedNum("spawns", f.AfterID)
	q := spawnSelect + w.clause() + ` ORDER BY created_at, ` + idNum + `, id` + w.limit(f.Limit)
	return list(t, store.Spawns.Scan, q, w.args...)
}

func (t *tx) GetKV(key string) (string, bool, error) {
	var v string
	err := t.q.QueryRowContext(t.ctx, `SELECT v FROM kv WHERE k = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (t *tx) SetKV(key, value string) error {
	_, err := t.q.ExecContext(t.ctx,
		`INSERT INTO kv(k, v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, key, value)
	return err
}
