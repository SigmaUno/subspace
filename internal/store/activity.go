package store

import "github.com/SigmaUno/subspace/internal/model"

type ActivityRange struct {
	After  int64
	Before int64
	Limit  int
}

type ActivityTx interface {
	AuditNewest(r ActivityRange) ([]*model.AuditEvent, error)
	SystemMessagesNewest(r ActivityRange) ([]*model.Message, error)
}
