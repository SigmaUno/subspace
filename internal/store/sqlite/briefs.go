package sqlite

import "github.com/SigmaUno/subspace/internal/model"

// TODO(split A1): implement.
func (t *tx) InsertBrief(b *model.Brief) error                 { return errNotImplemented }
func (t *tx) GetBrief(id string) (*model.Brief, error)         { return nil, errNotImplemented }
func (t *tx) UpdateBrief(b *model.Brief) error                 { return errNotImplemented }
func (t *tx) ListBriefs(status string) ([]*model.Brief, error) { return nil, errNotImplemented }
