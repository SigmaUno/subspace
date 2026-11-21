package postgres

import (
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

var (
	briefSelect = `SELECT ` + store.Briefs.Select("") + ` FROM briefs`
	briefInsert = store.Briefs.Insert(ph)
	briefUpdate = store.Briefs.Update(ph)
)

func (t *tx) InsertBrief(b *model.Brief) error {
	a, err := store.Briefs.InsertArgs(b)
	if err != nil {
		return err
	}
	return t.exec(briefInsert, a...)
}

func (t *tx) GetBrief(id string) (*model.Brief, error) {
	return get(t, store.Briefs.Scan, briefSelect+` WHERE id = $1`, id)
}

func (t *tx) UpdateBrief(b *model.Brief) error {
	a, err := store.Briefs.UpdateArgs(b)
	if err != nil {
		return err
	}
	return t.update(briefUpdate, a...)
}

func (t *tx) ListBriefs(f store.BriefFilter) ([]*model.Brief, error) {
	var a args
	a.eq("status", f.Status)
	a.afterCreated("briefs", f.AfterID)
	return list(t, store.Briefs.Scan, briefSelect+a.clause()+` ORDER BY created_at, id`+a.limit(f.Limit), a.vals...)
}
