package postgres

import (
	"database/sql"
	"errors"
	"fmt"
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

func auditJSON(m map[string]any) (any, error) {
	s, err := store.AuditJSON(m)
	if s == "" || err != nil {
		return nil, err
	}
	return s, nil
}

func (t *tx) InsertAudit(e *model.AuditEvent) error {
	before, err := auditJSON(e.Before)
	if err != nil {
		return err
	}
	after, err := auditJSON(e.After)
	if err != nil {
		return err
	}
	head, err := t.AuditHead(math.MaxInt64)
	if err != nil {
		return err
	}
	rec := audit.Record{ID: head.ID + 1, TS: store.Millis(e.TS), AgentID: e.AgentID, Role: e.Role, Tool: e.Tool,
		TaskID: store.Str(e.TaskID), Before: jsonText(before), After: jsonText(after), Note: store.TraceNote(store.Str(e.Note), t.trace)}
	hash, err := audit.Link(head.Hash, rec)
	if err != nil {
		return err
	}
	err = t.exec(`INSERT INTO audit(`+store.AuditCols+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		rec.ID, rec.TS, rec.AgentID, rec.Role, rec.Tool, rec.TaskID, before, after, rec.Note, hash)
	if err != nil {
		return err
	}
	e.ID, e.Hash = rec.ID, hash
	return nil
}

func jsonText(v any) string {
	s, _ := v.(string)
	return s
}

func (t *tx) AuditHead(maxID int64) (model.AuditHead, error) {
	var h model.AuditHead
	err := t.q.QueryRowContext(t.ctx, `SELECT id, hash FROM audit WHERE id <= $1 ORDER BY id DESC LIMIT 1`, maxID).Scan(&h.ID, &h.Hash)
	if errors.Is(err, sql.ErrNoRows) {
		return t.auditAnchor()
	}
	return h, err
}

func (t *tx) auditRecords(after int64, fn func(audit.Record, string) bool) error {
	rows, err := t.q.QueryContext(t.ctx, `SELECT `+store.AuditCols+` FROM audit WHERE id > $1 ORDER BY id`, after)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			r             audit.Record
			hash          string
			before, after []byte
		)
		if err := rows.Scan(&r.ID, &r.TS, &r.AgentID, &r.Role, &r.Tool, &r.TaskID, &before, &after, &r.Note, &hash); err != nil {
			return err
		}
		r.Before, r.After = string(before), string(after)
		if !fn(r, hash) {
			break
		}
	}
	return rows.Err()
}

func (t *tx) VerifyAudit() (model.AuditCheck, error) {
	anchor, err := t.auditAnchor()
	if err != nil {
		return model.AuditCheck{}, err
	}
	var broken int64
	c := audit.Chain{HeadID: anchor.ID, HeadHash: anchor.Hash}
	err = t.auditRecords(anchor.ID, func(r audit.Record, hash string) bool {
		if c.Add(r, hash) {
			return true
		}
		broken = r.ID
		return false
	})
	return model.AuditCheck{OK: broken == 0, Events: c.Events, BrokenID: broken,
		Head: model.AuditHead{ID: c.HeadID, Hash: c.HeadHash}}, err
}

func (t *tx) chainExisting() ([]model.AuditHead, error) {
	var (
		out  []model.AuditHead
		prev string
		lerr error
	)
	err := t.auditRecords(0, func(r audit.Record, _ string) bool {
		h, err := audit.Link(prev, r)
		if err != nil {
			lerr = fmt.Errorf("audit %d: %w", r.ID, err)
			return false
		}
		out = append(out, model.AuditHead{ID: r.ID, Hash: h})
		prev = h
		return true
	})
	return out, errors.Join(err, lerr)
}

func (t *tx) ListAudit(f store.AuditFilter) ([]*model.AuditEvent, error) {
	var a args
	a.where = append(a.where, "id > "+a.add(f.SinceID))
	a.eq("agent_id", f.AgentID)
	a.eq("task_id", f.TaskID)
	a.eq("tool", f.Tool)
	limit := f.Limit
	if limit <= 0 {
		limit = auditDefaultLimit
	}
	return list(t, store.ScanAudit, `SELECT `+store.AuditCols+` FROM audit`+a.clause()+` ORDER BY id LIMIT `+a.add(limit), a.vals...)
}

func (t *tx) InsertSpawn(s *model.Spawn) error {
	a, err := store.Spawns.InsertArgs(s)
	if err != nil {
		return err
	}
	return t.exec(spawnInsert, a...)
}

func (t *tx) UpdateSpawn(s *model.Spawn) error {
	a, err := store.Spawns.UpdateArgs(s)
	if err != nil {
		return err
	}
	return t.update(spawnUpdate, a...)
}

func (t *tx) ListSpawns(f store.SpawnFilter) ([]*model.Spawn, error) {
	var a args
	a.eq("brief_id", f.BriefID)
	a.afterCreatedNum("spawns", f.AfterID)
	return list(t, store.Spawns.Scan, spawnSelect+a.clause()+` ORDER BY created_at, `+idNum+`, id`+a.limit(f.Limit), a.vals...)
}

func (t *tx) GetKV(key string) (string, bool, error) {
	var v string
	err := t.q.QueryRowContext(t.ctx, `SELECT v FROM kv WHERE k = $1`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (t *tx) SetKV(key, value string) error {
	return t.exec(`INSERT INTO kv(k, v) VALUES($1, $2) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, key, value)
}
