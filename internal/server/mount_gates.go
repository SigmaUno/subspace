package server

import (
	"os"

	"github.com/SigmaUno/subspace/internal/gates"
	"github.com/SigmaUno/subspace/internal/prsync"
)

func init() {
	registerMount(mountGates)
}

func mountGates(d Deps) {
	var pr gates.PRSource
	if st, ok := d.Store.(prsync.Store); ok {
		pr = st
	}
	svc := gates.NewService(d.Board, d.Store, pr, nil)
	d.Board.SetQualityGate(svc.Gate)
	h := svc.Handler()
	d.API.Mount(d.Mux, "GET /api/v1/tasks/{id}/gates", h)
	d.API.Mount(d.Mux, "POST /api/v1/tasks/{id}/gates/override", h)
	if pr != nil && d.Hub != nil && os.Getenv("SUBSPACE_PRSYNC_CONFIG") != "" {
		go svc.Run(d.Ctx, d.Hub)
	}
}
