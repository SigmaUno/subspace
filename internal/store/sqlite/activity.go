package sqlite

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
	upper, limit := activityBounds(r)
	return list(t, store.ScanAudit,
		`SELECT `+store.AuditCols+` FROM audit WHERE id > ? AND id < ? ORDER BY id DESC LIMIT ?`, r.After, upper, limit)
}

func (t *tx) SystemMessagesNewest(r store.ActivityRange) ([]*model.Message, error) {
	upper, limit := activityBounds(r)
	return list(t, store.ScanMessage, `SELECT `+store.MessageCols+`
		 FROM messages WHERE kind = ? AND id > ? AND id < ? ORDER BY id DESC LIMIT ?`,
		model.MsgSystem, r.After, upper, limit)
}

var _ store.ActivityTx = (*tx)(nil)
