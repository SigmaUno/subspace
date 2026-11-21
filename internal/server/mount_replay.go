package server

import (
	"context"

	"github.com/SigmaUno/subspace/internal/replay"
	"github.com/SigmaUno/subspace/internal/transcript"
)

func init() {
	registerMount(func(d Deps) {
		svc := replay.NewService(d.Store)
		svc.SetTranscripts(func(ctx context.Context, ids []string) (map[string]bool, error) {
			v, ok := transcriptServices.Load(d.Mux)
			if !ok {
				return nil, nil
			}
			stats, err := v.(*transcript.Service).Stats(ctx, ids)
			has := make(map[string]bool, len(stats))
			for id := range stats {
				has[id] = true
			}
			return has, err
		})
		h := replay.Handler(svc)
		d.API.Mount(d.Mux, "GET /api/v1/briefs/{id}/replay", h)
		d.API.Mount(d.Mux, "GET /api/v1/briefs/{id}/replay/timeline", h)
		d.API.Mount(d.Mux, "GET /api/v1/briefs/{id}/replay/diff", h)
	})
}
