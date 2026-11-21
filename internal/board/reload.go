package board

import (
	"context"
	"fmt"

	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/reload"
	"github.com/SigmaUno/subspace/internal/store"
)

const EnvCapacityFile = "SUBSPACE_CAPACITY_FILE"

var CapacityReloadKeys = []string{EnvCapacityFile}

func (b *Board) PrepareReload(getenv func(string) string) (reload.Plan, error) {
	var cfg capacity.Config
	if path := getenv(EnvCapacityFile); path != "" {
		c, err := capacity.Load(path)
		if err != nil {
			return reload.Plan{}, fmt.Errorf("%s: %w", EnvCapacityFile, err)
		}
		cfg = c
	}
	b.mu.Lock()
	overridden := b.capDB
	b.mu.Unlock()
	plan := reload.Plan{Commit: func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if !b.capDB {
			b.capCfg = cfg
		}
	}}
	if overridden {
		plan.Ignored = []reload.Ignored{{Key: EnvCapacityFile, Reason: "overridden by capacity saved with PUT /api/v1/capacity"}}
	}
	return plan, nil
}

func (b *Board) AuditReload(ctx context.Context, res reload.Result, note string) error {
	after := map[string]any{"applied": res.Applied, "ignored": len(res.Ignored)}
	return b.write(ctx, func(tx store.Tx, w *wctx) error {
		return w.audit(tx, model.Owner, "reload_config", "", nil, after, note)
	})
}
