package sqlite

import (
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

// TODO(split A4): implement.
func (t *tx) InsertAgent(a *model.Agent) error                   { return errNotImplemented }
func (t *tx) GetAgent(id string) (*model.Agent, error)           { return nil, errNotImplemented }
func (t *tx) GetAgentByTokenHash(h string) (*model.Agent, error) { return nil, errNotImplemented }
func (t *tx) UpdateAgent(a *model.Agent) error                   { return errNotImplemented }
func (t *tx) ListAgents(f store.AgentFilter) ([]*model.Agent, error) {
	return nil, errNotImplemented
}
func (t *tx) TouchAgents(seen map[string]int64) error { return errNotImplemented }
