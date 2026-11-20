package server

import (
	"time"

	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/forecast"
)

func init() {
	registerMount(func(d Deps) {
		svc := forecast.NewService(forecast.StoreSource{Store: d.Store}, forecast.Options{}, time.Now)
		d.API.Mount(d.Mux, "GET /api/v1/briefs/{id}/forecast", forecast.Handler(svc))
		detach := decisions.Attach(d.Store, svc)
		go func() {
			<-d.Ctx.Done()
			detach()
		}()
	})
}
