package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/audit"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	auditDefaultLimit = 200
	auditCols         = `id, ts, agent_id, role, tool, task_id, before, after, note, hash`
	spawnCols         = `id, brief_id, agent_id, kind, model, provider, status, spawner, error, requested_by, created_at`
)

func auditJSON(m map[string]any) (string, error) {
	if m == nil {
		return "", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func auditUnJSON(s string) (map[string]any, error) {
	if s == "" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func auditStrPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func auditStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func auditMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func auditTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func auditIsDup(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
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
	rec := audit.Record{ID: head.ID + 1, TS: auditMillis(e.TS), AgentID: e.AgentID, Role: e.Role, Tool: e.Tool,
		TaskID: auditStr(e.TaskID), Before: before, After: after, Note: auditStr(e.Note)}
	hash, err := audit.Link(head.Hash, rec)
	if err != nil {
		return err
	}
	_, err = t.q.ExecContext(t.ctx,
		`INSERT INTO audit(`+auditCols+`) VALUES(?,?,?,?,?,?,?,?,?,?)`,
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
		return h, nil
	}
	return h, err
}

func (t *tx) VerifyAudit() (model.AuditCheck, error) {
	rows, err := t.q.QueryContext(t.ctx, `SELECT `+auditCols+` FROM audit ORDER BY id`)
	if err != nil {
		return model.AuditCheck{}, err
	}
	defer func() { _ = rows.Close() }()
	return verifyRows(rows)
}

func verifyRows(rows *sql.Rows) (model.AuditCheck, error) {
	var c audit.Chain
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
	q := `SELECT ` + auditCols + ` FROM audit WHERE id > ?`
	args := []any{f.SinceID}
	if f.AgentID != "" {
		q += ` AND agent_id = ?`
		args = append(args, f.AgentID)
	}
	if f.TaskID != "" {
		q += ` AND task_id = ?`
		args = append(args, f.TaskID)
	}
	if f.Tool != "" {
		q += ` AND tool = ?`
		args = append(args, f.Tool)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = auditDefaultLimit
	}
	q += ` ORDER BY id ASC LIMIT ?`
	args = append(args, limit)

	rows, err := t.q.QueryContext(t.ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*model.AuditEvent{}
	for rows.Next() {
		var (
			e                         model.AuditEvent
			ts                        int64
			taskID, before, after, nt string
		)
		if err := rows.Scan(&e.ID, &ts, &e.AgentID, &e.Role, &e.Tool, &taskID, &before, &after, &nt, &e.Hash); err != nil {
			return nil, err
		}
		e.TS = auditTime(ts)
		e.TaskID = auditStrPtr(taskID)
		e.Note = auditStrPtr(nt)
		if e.Before, err = auditUnJSON(before); err != nil {
			return nil, err
		}
		if e.After, err = auditUnJSON(after); err != nil {
			return nil, err
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

func (t *tx) InsertSpawn(s *model.Spawn) error {
	_, err := t.q.ExecContext(t.ctx,
		`INSERT INTO spawns(`+spawnCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		s.ID, s.BriefID, s.AgentID, s.Kind, s.Model, s.Provider, s.Status, s.Spawner,
		auditStr(s.Error), s.RequestedBy, auditMillis(s.CreatedAt))
	if auditIsDup(err) {
		return store.ErrDuplicate
	}
	return err
}

func (t *tx) UpdateSpawn(s *model.Spawn) error {
	res, err := t.q.ExecContext(t.ctx,
		`UPDATE spawns SET brief_id=?, agent_id=?, kind=?, model=?, provider=?, status=?, spawner=?, error=?,
		 requested_by=?, created_at=? WHERE id=?`,
		s.BriefID, s.AgentID, s.Kind, s.Model, s.Provider, s.Status, s.Spawner,
		auditStr(s.Error), s.RequestedBy, auditMillis(s.CreatedAt), s.ID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (t *tx) ListSpawns(briefID string) ([]*model.Spawn, error) {
	q := `SELECT ` + spawnCols + ` FROM spawns`
	var args []any
	if briefID != "" {
		q += ` WHERE brief_id = ?`
		args = append(args, briefID)
	}
	q += ` ORDER BY created_at, CAST(SUBSTR(id, INSTR(id, '-')+1) AS INTEGER), id`
	rows, err := t.q.QueryContext(t.ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*model.Spawn{}
	for rows.Next() {
		var (
			s       model.Spawn
			errText string
			created int64
		)
		if err := rows.Scan(&s.ID, &s.BriefID, &s.AgentID, &s.Kind, &s.Model, &s.Provider, &s.Status,
			&s.Spawner, &errText, &s.RequestedBy, &created); err != nil {
			return nil, err
		}
		s.Error = auditStrPtr(errText)
		s.CreatedAt = auditTime(created)
		out = append(out, &s)
	}
	return out, rows.Err()
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
