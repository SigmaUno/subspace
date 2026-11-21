package sqlite

import (
	"math"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

func activityBounds(r store.ActivityRange) (before int64, limit int) {
	before, limit = r.Before, r.Limit
	if before <= 0 {
		before = math.MaxInt64
	}
	if limit <= 0 {
		limit = auditDefaultLimit
	}
	return before, limit
}

func (t *tx) AuditNewest(r store.ActivityRange) ([]*model.AuditEvent, error) {
	upper, limit := activityBounds(r)
	rows, err := t.q.QueryContext(t.ctx,
		`SELECT `+auditCols+` FROM audit WHERE id > ? AND id < ? ORDER BY id DESC LIMIT ?`, r.After, upper, limit)
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

func (t *tx) SystemMessagesNewest(r store.ActivityRange) ([]*model.Message, error) {
	upper, limit := activityBounds(r)
	rows, err := t.q.QueryContext(t.ctx,
		`SELECT id,thread_kind,thread_id,author_id,author_role,kind,body,created_at
		 FROM messages WHERE kind = ? AND id > ? AND id < ? ORDER BY id DESC LIMIT ?`,
		model.MsgSystem, r.After, upper, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*model.Message{}
	for rows.Next() {
		var m model.Message
		var created int64
		if err := rows.Scan(&m.ID, &m.ThreadKind, &m.ThreadID, &m.AuthorID, &m.AuthorRole, &m.Kind, &m.Body, &created); err != nil {
			return nil, err
		}
		m.CreatedAt = time.UnixMilli(created).UTC()
		out = append(out, &m)
	}
	return out, rows.Err()
}

var _ store.ActivityTx = (*tx)(nil)
