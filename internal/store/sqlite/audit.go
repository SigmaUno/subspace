package sqlite

import (
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

// TODO(split A6): implement audit, spawns, kv.
func (t *tx) InsertAudit(e *model.AuditEvent) error { return errNotImplemented }
func (t *tx) ListAudit(f store.AuditFilter) ([]*model.AuditEvent, error) {
	return nil, errNotImplemented
}
func (t *tx) InsertSpawn(s *model.Spawn) error { return errNotImplemented }
func (t *tx) UpdateSpawn(s *model.Spawn) error { return errNotImplemented }
func (t *tx) ListSpawns(briefID string) ([]*model.Spawn, error) {
	return nil, errNotImplemented
}
func (t *tx) GetKV(key string) (string, bool, error) { return "", false, errNotImplemented }
func (t *tx) SetKV(key, value string) error          { return errNotImplemented }
