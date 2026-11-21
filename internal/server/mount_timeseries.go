package server

import (
	"time"

	"github.com/SigmaUno/subspace/internal/timeseries"
)

func init() {
	registerMount(func(d Deps) {
		d.API.Mount(d.Mux, "GET /api/v1/timeseries", timeseries.Handler(timeseries.New(d.Store, time.Now)))
	})
}
