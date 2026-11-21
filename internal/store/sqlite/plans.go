package sqlite

import (
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

var (
	planSelect = `SELECT ` + store.Plans.Select("") + ` FROM plans`
	planInsert = store.Plans.Insert(ph)
	planUpdate = store.Plans.Update(ph)
)

func (t *tx) InsertPlan(p *model.Plan) error {
	args, err := store.Plans.InsertArgs(p)
	if err != nil {
		return err
	}
	return t.exec(planInsert, args...)
}

func (t *tx) GetPlan(id string) (*model.Plan, error) {
	return get(t, store.Plans.Scan, planSelect+` WHERE id=?`, id)
}

func (t *tx) UpdatePlan(p *model.Plan) error {
	args, err := store.Plans.UpdateArgs(p)
	if err != nil {
		return err
	}
	return t.update(planUpdate, args...)
}

func (t *tx) ListPlans(f store.PlanFilter) ([]*model.Plan, error) {
	var w where
	w.eq("brief_id", f.BriefID)
	w.eq("status", f.Status)
	w.afterCreated("plans", f.AfterID)
	q := planSelect + w.clause() + ` ORDER BY created_at, id` + w.limit(f.Limit)
	return list(t, store.Plans.Scan, q, w.args...)
}
