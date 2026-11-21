package server

import (
	"net/http"

	"github.com/SigmaUno/subspace/internal/httpx"
)

func init() {
	registerMount(func(d Deps) {
		d.API.Mount(d.Mux, "GET /api/v1/backups", httpx.Handle(http.StatusOK, func(r *http.Request) (any, error) {
			list, err := d.Board.ListBackups(r.Context())
			return map[string]any{"backups": list}, err
		}))
		d.API.Mount(d.Mux, "POST /api/v1/backup", httpx.Handle(http.StatusOK, func(r *http.Request) (any, error) {
			path, n, err := d.Board.Backup(r.Context())
			return map[string]any{"path": path, "bytes": n}, err
		}))
	})
}
