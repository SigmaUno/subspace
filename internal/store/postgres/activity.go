package postgres

import (
	"math"

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
	before, limit := activityBounds(r)
	return list(t, scanAudit, `SELECT `+auditCols+` FROM audit WHERE id > $1 AND id < $2 ORDER BY id DESC LIMIT $3`,
		r.After, before, limit)
}

func (t *tx) SystemMessagesNewest(r store.ActivityRange) ([]*model.Message, error) {
	before, limit := activityBounds(r)
	return list(t, scanMessage, `SELECT id, thread_kind, thread_id, author_id, author_role, kind, body, created_at
  FROM messages WHERE kind = $1 AND id > $2 AND id < $3 ORDER BY id DESC LIMIT $4`,
		model.MsgSystem, r.After, before, limit)
}

var _ store.ActivityTx = (*tx)(nil)
