package replay

import (
	"encoding/json"
	"net/http"

	"github.com/SigmaUno/subspace/internal/model"
)

func Handler(svc *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /briefs/{id}/replay", func(w http.ResponseWriter, r *http.Request) {
		snap, err := svc.Snapshot(r.Context(), r.PathValue("id"), r.URL.Query().Get("at"))
		write(w, snap, err)
	})
	mux.HandleFunc("GET /briefs/{id}/replay/timeline", func(w http.ResponseWriter, r *http.Request) {
		tl, err := svc.Timeline(r.Context(), r.PathValue("id"))
		write(w, tl, err)
	})
	mux.HandleFunc("GET /briefs/{id}/replay/diff", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		d, err := svc.Diff(r.Context(), r.PathValue("id"), q.Get("from"), q.Get("to"))
		write(w, d, err)
	})
	return mux
}

func write(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		e := model.AsError(err)
		w.WriteHeader(e.HTTPStatus())
		v = map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}}
	}
	_ = json.NewEncoder(w).Encode(v)
}
