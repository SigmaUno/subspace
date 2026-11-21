package insights

import (
	"encoding/json"
	"net/http"

	"github.com/SigmaUno/subspace/internal/model"
)

func Handler(svc *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /insights", func(w http.ResponseWriter, r *http.Request) {
		sev := r.URL.Query().Get("severity")
		if sev != "" && sev != SeverityHigh && sev != SeverityWarning {
			writeError(w, model.BadRequest("severity must be %s or %s", SeverityWarning, SeverityHigh))
			return
		}
		rep, err := svc.All(r.Context(), sev)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, rep)
	})
	mux.HandleFunc("GET /agents/{id}/insights", func(w http.ResponseWriter, r *http.Request) {
		rep, err := svc.Agent(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, rep)
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
