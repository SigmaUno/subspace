package forecast

import (
	"encoding/json"
	"net/http"

	"github.com/SigmaUno/subspace/internal/model"
)

func Handler(svc *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /briefs/{id}/forecast", func(w http.ResponseWriter, r *http.Request) {
		f, err := svc.Forecast(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f)
	})
	return mux
}

func writeError(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.HTTPStatus())
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}
