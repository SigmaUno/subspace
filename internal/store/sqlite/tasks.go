package sqlite

import (
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

// TODO(split A3): implement.
func (t *tx) InsertTask(x *model.Task) error         { return errNotImplemented }
func (t *tx) GetTask(id string) (*model.Task, error) { return nil, errNotImplemented }
func (t *tx) UpdateTask(x *model.Task) error         { return errNotImplemented }
func (t *tx) ListTasks(f store.TaskFilter) ([]*model.Task, error) {
	return nil, errNotImplemented
}
func (t *tx) CountTasksByStatus() (map[string]int, error) { return nil, errNotImplemented }
func (t *tx) BriefCost(briefID string) (float64, error)   { return 0, errNotImplemented }
func (t *tx) ExtendLeases(rows []store.LeaseRow) error    { return errNotImplemented }
