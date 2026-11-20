package sqlite

import "github.com/SigmaUno/subspace/internal/model"

// TODO(split A2): implement.
func (t *tx) InsertPlan(p *model.Plan) error         { return errNotImplemented }
func (t *tx) GetPlan(id string) (*model.Plan, error) { return nil, errNotImplemented }
func (t *tx) UpdatePlan(p *model.Plan) error         { return errNotImplemented }
func (t *tx) ListPlans(briefID, status string) ([]*model.Plan, error) {
	return nil, errNotImplemented
}
