package server

import (
	"github.com/SigmaUno/subspace/internal/tokens"
)

func init() {
	registerMount(func(d Deps) {
		st, ok := d.Store.(tokens.Store)
		if !ok {
			d.Log.Error("api tokens disabled", "err", "store does not support api tokens")
			return
		}
		svc := tokens.New(st, d.Cfg.Now)
		d.API.Tokens = svc
		h := tokens.Handler(svc, d.Log)
		d.API.Mount(d.Mux, "GET /api/v1/tokens", h)
		d.API.Mount(d.Mux, "POST /api/v1/tokens", h)
		d.API.Mount(d.Mux, "POST /api/v1/tokens/{id}/revoke", h)
	})
}
