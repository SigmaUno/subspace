package server

import (
	"github.com/SigmaUno/subspace/internal/about"
	"github.com/SigmaUno/subspace/internal/board"
)

func init() { registerMount(mountAbout) }

func mountAbout(d Deps) {
	d.API.Mount(d.Mux, "GET /api/v1/about", about.Handler(about.Options{
		Version: board.Version, DBPath: d.Cfg.DBPath, Started: d.Cfg.Now(), Now: d.Cfg.Now,
		Backups: d.Board.ListBackups,
	}))
}
