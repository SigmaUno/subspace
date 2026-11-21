package server

import (
	"time"

	"github.com/SigmaUno/subspace/internal/usage"
)

func init() {
	registerMount(func(d Deps) {
		d.API.Mount(d.Mux, "GET /api/v1/costs", usage.CostsHandler(usage.NewCostService(d.Store, time.Now)))
	})
}
