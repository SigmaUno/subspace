package server

import (
	"context"
	"fmt"

	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/prsync"
)

func init() {
	registerMount(mountPRStatus)
}

func mountPRStatus(d Deps) {
	st, ok := d.Store.(prsync.Store)
	if !ok {
		d.Log.Error("pr status sync disabled", "err", "store does not support pr status")
		return
	}
	svc := prsync.NewService(d.Board, st)
	h := svc.Handler()
	d.API.Mount(d.Mux, "GET /api/v1/tasks/{id}/pr", h)
	d.API.Mount(d.Mux, "GET /api/v1/prs", h)
	detach := decisions.Attach(d.Store, svc)
	go func() {
		<-d.Ctx.Done()
		detach()
	}()

	d.Leader.Go(d.Ctx, func(ctx context.Context) {
		superviseRepos(ctx, d, func(ctx context.Context) (func(context.Context), error) {
			cfg, ok, err := resolveRepos(ctx, d, "pr status sync")
			if err != nil || !ok {
				return nil, err
			}
			p, err := prsync.New(prsync.Options{Config: cfg, Board: d.Board, Store: st, Hub: d.Hub, Log: d.Log})
			if err != nil {
				return nil, fmt.Errorf("pr status sync disabled: %w", err)
			}
			return p.Run, nil
		})
	})
}
