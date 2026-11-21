package components

import (
	"context"
	"encoding/json"

	"github.com/SigmaUno/subspace/internal/store"
)

const kvPrefix = "components:"

type KV struct{ st store.Store }

func NewKV(st store.Store) *KV { return &KV{st: st} }

func (k *KV) Replace(ctx context.Context, briefID string, comps []Component) error {
	raw, err := json.Marshal(comps)
	if err != nil {
		return err
	}
	return k.st.Atomic(ctx, func(t store.Tx) error { return t.SetKV(kvPrefix+briefID, string(raw)) })
}

func (k *KV) List(ctx context.Context, briefID string) ([]Component, error) {
	var raw string
	var ok bool
	err := k.st.View(ctx, func(t store.Tx) (err error) {
		raw, ok, err = t.GetKV(kvPrefix + briefID)
		return err
	})
	if err != nil || !ok {
		return []Component{}, err
	}
	var comps []Component
	if err := json.Unmarshal([]byte(raw), &comps); err != nil {
		return nil, err
	}
	return cloneAll(comps), nil
}
