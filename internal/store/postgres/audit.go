package postgres

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/SigmaUno/subspace/internal/audit"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	auditDefaultLimit = 200
	auditCols         = `id, ts, agent_id, role, tool, task_id, before, after, note, hash`
	spawnCols         = `id, brief_id, agent_id, kind, model, provider, status, spawner, error, requested_by, created_at`
)

func auditJSON(m map[string]any) (any, error) {
	if m == nil {
		return nil, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func auditMap(b []byte) (map[string]any, error) {
	if b == nil {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func scanAudit(r scanner) (*model.AuditEvent, error) {
	var (
		e             model.AuditEvent
		ts            int64
		task, note    string
		before, after []byte
	)
	if err := r.Scan(&e.ID, &ts, &e.AgentID, &e.Role, &e.Tool, &task, &before, &after, &note, &e.Hash); err != nil {
		return nil, err
	}
	var err error
	if e.Before, err = auditMap(before); err != nil {
		return nil, err
	}
	if e.After, err = auditMap(after); err != nil {
		return nil, err
	}
	e.TS = fromMS(ts)
	e.TaskID = strPtr(task)
	e.Note = strPtr(note)
	return &e, nil
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
	rec := audit.Record{ID: head.ID + 1, TS: ms(e.TS), AgentID: e.AgentID, Role: e.Role, Tool: e.Tool,
		TaskID: str(e.TaskID), Before: jsonText(before), After: jsonText(after), Note: str(e.Note)}
	hash, err := audit.Link(head.Hash, rec)
	if err != nil {
		return err
	}
	err = t.exec(`INSERT INTO audit(`+auditCols+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
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
		return h, nil
	}
	return h, err
}

func (t *tx) auditRecords(fn func(audit.Record, string) bool) error {
	rows, err := t.q.QueryContext(t.ctx, `SELECT `+auditCols+` FROM audit ORDER BY id`)
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
	var (
		c      audit.Chain
		broken int64
	)
	err := t.auditRecords(func(r audit.Record, hash string) bool {
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
	err := t.auditRecords(func(r audit.Record, _ string) bool {
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
	return list(t, scanAudit, `SELECT `+auditCols+` FROM audit`+a.clause()+` ORDER BY id LIMIT `+a.add(limit), a.vals...)
}

func scanSpawn(r scanner) (*model.Spawn, error) {
	var (
		s       model.Spawn
		errText string
		created int64
	)
	if err := r.Scan(&s.ID, &s.BriefID, &s.AgentID, &s.Kind, &s.Model, &s.Provider, &s.Status, &s.Spawner,
		&errText, &s.RequestedBy, &created); err != nil {
		return nil, err
	}
	s.Error = strPtr(errText)
	s.CreatedAt = fromMS(created)
	return &s, nil
}

func spawnArgs(s *model.Spawn) []any {
	return []any{s.ID, s.BriefID, s.AgentID, s.Kind, s.Model, s.Provider, s.Status, s.Spawner,
		str(s.Error), s.RequestedBy, ms(s.CreatedAt)}
}

func (t *tx) InsertSpawn(s *model.Spawn) error {
	return t.exec(`INSERT INTO spawns(`+spawnCols+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, spawnArgs(s)...)
}

func (t *tx) UpdateSpawn(s *model.Spawn) error {
	return t.update(`UPDATE spawns SET brief_id=$2, agent_id=$3, kind=$4, model=$5, provider=$6, status=$7, spawner=$8,
  error=$9, requested_by=$10, created_at=$11 WHERE id=$1`, spawnArgs(s)...)
}

func (t *tx) ListSpawns(f store.SpawnFilter) ([]*model.Spawn, error) {
	var a args
	a.eq("brief_id", f.BriefID)
	a.afterCreatedNum("spawns", f.AfterID)
	return list(t, scanSpawn, `SELECT `+spawnCols+` FROM spawns`+a.clause()+` ORDER BY created_at, `+idNum+`, id`+a.limit(f.Limit), a.vals...)
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
