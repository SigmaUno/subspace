package server

import (
	"time"

	"github.com/SigmaUno/subspace/internal/agentstats"
)

func init() {
	registerMount(func(d Deps) {
		h := agentstats.Handler(agentstats.NewService(agentstats.StoreSource{Store: d.Store}, time.Now))
		d.API.Mount(d.Mux, "GET /api/v1/agent-stats", h)
		d.API.Mount(d.Mux, "GET /api/v1/agents/{id}/stats", h)
	})
}
