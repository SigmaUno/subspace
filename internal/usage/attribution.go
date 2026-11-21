package usage

import (
	"context"
	"errors"

	"github.com/SigmaUno/subspace/internal/store"
)

type Attributor func(ctx context.Context, r *Record) error

func StoreAttributor(st store.Store) Attributor {
	return func(ctx context.Context, r *Record) error {
		if r.TaskID == "" && r.AgentID == "" {
			return nil
		}
		return st.View(ctx, func(tx store.Tx) error {
			if r.TaskID != "" {
				t, err := tx.GetTask(r.TaskID)
				switch {
				case err == nil:
					if r.BriefID == "" {
						r.BriefID = t.BriefID
					}
					if r.ComponentID == "" && t.ComponentID != nil {
						r.ComponentID = *t.ComponentID
					}
				case !errors.Is(err, store.ErrNotFound):
					return err
				}
			}
			if r.AgentID != "" {
				a, err := tx.GetAgent(r.AgentID)
				switch {
				case err == nil:
					if r.Kind == "" {
						r.Kind = a.Kind
					}
					if r.Role == "" {
						r.Role = a.Role
					}
				case !errors.Is(err, store.ErrNotFound):
					return err
				}
			}
			return nil
		})
	}
}
