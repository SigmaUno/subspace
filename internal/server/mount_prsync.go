package server

import (
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

	cfg, ok, err := prsync.Resolve(d.Ctx, repoSource(d), d.Log)
	if err != nil {
		d.Log.Error("pr status sync disabled", "err", err)
		return
	}
	if !ok {
		return
	}
	p, err := prsync.New(prsync.Options{Config: cfg, Board: d.Board, Store: st, Hub: d.Hub, Log: d.Log})
	if err != nil {
		d.Log.Error("pr status sync disabled", "err", err)
		return
	}
	go p.Run(d.Ctx)
}
