package server

import (
	"path/filepath"

	"github.com/SigmaUno/subspace/internal/incident"
)

func init() { registerMount(mountIncident) }

func mountIncident(d Deps) {
	if !incident.SupportsSQL(d.Ctx, d.Store) {
		d.Log.Error("incident mode disabled", "err", "store does not support SQL extensions")
		return
	}
	dir := d.Cfg.DataDir
	if dir == "" && !isPostgres(d.Cfg.DBPath) {
		dir = filepath.Dir(d.Cfg.DBPath)
	}
	svc := incident.New(incident.Options{Store: d.Store, Control: d.Board, Pub: d.Hub, Dir: dir, Now: d.Cfg.Now, Log: d.Log})
	h := incident.Handler(svc, d.Log)
	d.API.Mount(d.Mux, "POST /api/v1/incident", h)
	d.API.Mount(d.Mux, "GET /api/v1/incident", h)
	d.API.Mount(d.Mux, "POST /api/v1/incident/{id}/end", h)
}
