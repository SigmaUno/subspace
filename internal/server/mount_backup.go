package server

import (
	"encoding/json"
	"net/http"

	"github.com/SigmaUno/subspace/internal/model"
)

func init() {
	registerMount(func(d Deps) {
		d.API.Mount(d.Mux, "GET /api/v1/backups", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			list, err := d.Board.ListBackups(r.Context())
			if err != nil {
				writeAPIError(w, err)
				return
			}
			writeAPIJSON(w, http.StatusOK, map[string]any{"backups": list})
		}))
		d.API.Mount(d.Mux, "POST /api/v1/backup", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path, n, err := d.Board.Backup(r.Context())
			if err != nil {
				writeAPIError(w, err)
				return
			}
			writeAPIJSON(w, http.StatusOK, map[string]any{"path": path, "bytes": n})
		}))
	})
}

func writeAPIJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	body := map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}}
	writeAPIJSON(w, e.HTTPStatus(), body)
}
