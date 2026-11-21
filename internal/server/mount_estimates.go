package server

import (
	"sync"

	"github.com/SigmaUno/subspace/internal/estimate"
)

var estimators sync.Map

func init() {
	registerMount(func(d Deps) {
		h := estimate.Handler(estimator(d))
		d.API.Mount(d.Mux, "GET /api/v1/briefs/{id}/estimates", h)
		d.API.Mount(d.Mux, "GET /api/v1/plans/{id}/estimates", h)
		go func() {
			<-d.Ctx.Done()
			estimators.Delete(d.Mux)
		}()
	})
}

func estimator(d Deps) *estimate.Service {
	v, _ := estimators.LoadOrStore(d.Mux, estimate.NewService(estimate.StoreSource{Store: d.Store}, estimate.Options{}))
	return v.(*estimate.Service)
}
