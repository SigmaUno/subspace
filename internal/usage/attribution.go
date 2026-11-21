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

type Persisted struct {
	*MemStore
	st      store.Store
	eurPerD float64
}

func NewPersisted(st store.Store, eurToUSD float64) *Persisted {
	if eurToUSD <= 0 {
		eurToUSD = 1
	}
	return &Persisted{MemStore: NewMemStore(), st: st, eurPerD: 1 / eurToUSD}
}

func (p *Persisted) Put(ctx context.Context, recs []Record) error {
	if err := p.MemStore.Put(ctx, recs); err != nil {
		return err
	}
	return p.st.Atomic(ctx, func(tx store.Tx) error {
		ct, ok := tx.(store.CostsTx)
		if !ok {
			return nil
		}
		for _, r := range recs {
			if r.RateLimited {
				continue
			}
			err := ct.PutCost(store.CostRecord{
				Key: r.Key, TS: r.TS, BriefID: r.BriefID, TaskID: r.TaskID, ComponentID: r.ComponentID, AgentID: r.AgentID,
				Kind: r.Kind, Role: r.Role, Provider: r.Provider, Model: r.Model,
				TokensIn: r.TokensIn, TokensOut: r.TokensOut, CacheRead: r.CacheRead, CacheWrite: r.CacheWrite,
				CostEUR: r.CostUSD * p.eurPerD,
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
}
