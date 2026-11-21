package components

import (
	"encoding/json"
	"net/http"

	"github.com/SigmaUno/subspace/internal/model"
)

const maxPlanBytes = 1 << 20

func Handler(svc *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /briefs/{id}/architecture", func(w http.ResponseWriter, r *http.Request) {
		g, err := svc.Graph(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, g)
	})
	mux.HandleFunc("PUT /briefs/{id}/components", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Components []Spec `json:"components"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPlanBytes)).Decode(&body); err != nil {
			writeError(w, model.BadRequest("invalid JSON body: %v", err))
			return
		}
		comps, err := svc.SetPlan(r.Context(), r.PathValue("id"), body.Components)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"components": comps})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	writeJSON(w, e.HTTPStatus(), map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}
