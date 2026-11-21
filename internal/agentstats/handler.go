package agentstats

import (
	"encoding/json"
	"net/http"

	"github.com/SigmaUno/subspace/internal/model"
)

func Handler(svc *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /agent-stats", func(w http.ResponseWriter, r *http.Request) {
		all, err := svc.All(r.Context(), r.URL.Query().Get("role"))
		if err != nil {
			writeError(w, err)
			return
		}
		if all == nil {
			all = []Stats{}
		}
		writeJSON(w, map[string]any{"agents": all})
	})
	mux.HandleFunc("GET /agents/{id}/stats", func(w http.ResponseWriter, r *http.Request) {
		st, err := svc.Agent(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, st)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.HTTPStatus())
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}
