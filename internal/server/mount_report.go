package server

import (
	"time"

	"github.com/SigmaUno/subspace/internal/report"
)

func init() {
	registerMount(func(d Deps) {
		h := report.Handler(report.NewService(report.StoreSource{Store: d.Store}, time.Now))
		d.API.Mount(d.Mux, "GET /api/v1/briefs/{id}/report", h)
		d.API.Mount(d.Mux, "GET /api/v1/briefs/{id}/report.md", h)
	})
}
