package postgres

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
	a, err := store.Plans.InsertArgs(p)
	if err != nil {
		return err
	}
	return t.exec(planInsert, a...)
}

func (t *tx) GetPlan(id string) (*model.Plan, error) {
	return get(t, store.Plans.Scan, planSelect+` WHERE id = $1`, id)
}

func (t *tx) UpdatePlan(p *model.Plan) error {
	a, err := store.Plans.UpdateArgs(p)
	if err != nil {
		return err
	}
	return t.update(planUpdate, a...)
}

func (t *tx) ListPlans(f store.PlanFilter) ([]*model.Plan, error) {
	var a args
	a.eq("brief_id", f.BriefID)
	a.eq("status", f.Status)
	a.afterCreated("plans", f.AfterID)
	return list(t, store.Plans.Scan, planSelect+a.clause()+` ORDER BY created_at, id`+a.limit(f.Limit), a.vals...)
}
