package spendguard

import (
	"encoding/json"
	"net/http"

	"github.com/SigmaUno/subspace/internal/model"
)

func Handler(g *Guard) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /limits", func(w http.ResponseWriter, r *http.Request) {
		if err := g.Reconcile(r.Context()); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, g.View())
	})
	mux.HandleFunc("PUT /limits", func(w http.ResponseWriter, r *http.Request) {
		var l Limits
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&l); err != nil {
			writeError(w, model.BadRequest("invalid body: %v", err))
			return
		}
		v, err := g.SetLimits(r.Context(), l)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, v)
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
