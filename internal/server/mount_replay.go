package server

import "github.com/SigmaUno/subspace/internal/replay"

func init() {
	registerMount(func(d Deps) {
		h := replay.Handler(replay.NewService(d.Store))
		d.API.Mount(d.Mux, "GET /api/v1/briefs/{id}/replay", h)
		d.API.Mount(d.Mux, "GET /api/v1/briefs/{id}/replay/timeline", h)
		d.API.Mount(d.Mux, "GET /api/v1/briefs/{id}/replay/diff", h)
	})
}
