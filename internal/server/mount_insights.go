package server

import (
	"encoding/json"

	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/insights"
	"github.com/SigmaUno/subspace/internal/model"
)

func init() {
	registerMount(func(d Deps) {
		st, ok := d.Store.(insights.Store)
		if !ok {
			d.Log.Error("agent insights disabled", "err", "store does not keep tool errors")
			return
		}
		rec := insights.NewRecorder(st, insights.RecorderOptions{Now: d.Cfg.Now, Log: d.Log})
		d.Registry.OnError(func(a model.Actor, tool, code, message string, args json.RawMessage) {
			rec.Observe(a.ID, tool, code, message, args)
		})
		go rec.Run(d.Ctx)
		svc := insights.NewService(insights.StoreSource{Store: d.Store, Errors: st}, rec, d.Cfg.Now, insights.DefaultThresholds())
		h := insights.Handler(svc)
		d.API.Mount(d.Mux, "GET /api/v1/insights", h)
		d.API.Mount(d.Mux, "GET /api/v1/agents/{id}/insights", h)
		detach := decisions.Attach(d.Store, svc)
		go func() {
			<-d.Ctx.Done()
			detach()
		}()
	})
}
