package server

import (
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/prsync"
)

func init() { registerMount(mountScope) }

func mountScope(d Deps) {
	st, ok := d.Store.(prsync.Store)
	if !ok {
		return
	}
	svc := prsync.NewService(d.Board, st)
	d.API.Mount(d.Mux, "GET /api/v1/tasks/{id}/scope", svc.ScopeHandler())
	unbindOnDone(d, mcp.BindScope(d.Board, svc))
}
