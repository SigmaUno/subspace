package sqlite

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
	args, err := store.Briefs.InsertArgs(b)
	if err != nil {
		return err
	}
	return t.exec(briefInsert, args...)
}

func (t *tx) GetBrief(id string) (*model.Brief, error) {
	return get(t, store.Briefs.Scan, briefSelect+` WHERE id=?`, id)
}

func (t *tx) UpdateBrief(b *model.Brief) error {
	args, err := store.Briefs.UpdateArgs(b)
	if err != nil {
		return err
	}
	return t.update(briefUpdate, args...)
}

func (t *tx) ListBriefs(f store.BriefFilter) ([]*model.Brief, error) {
	var w where
	w.eq("status", f.Status)
	w.afterCreated("briefs", f.AfterID)
	q := briefSelect + w.clause() + ` ORDER BY created_at, id` + w.limit(f.Limit)
	return list(t, store.Briefs.Scan, q, w.args...)
}
