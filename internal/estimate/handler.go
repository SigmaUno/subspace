package estimate

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/SigmaUno/subspace/internal/model"
)

func Handler(svc *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /briefs/{id}/estimates", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, svc.Brief)
	})
	mux.HandleFunc("GET /plans/{id}/estimates", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, svc.Plan)
	})
	return mux
}

func serve(w http.ResponseWriter, r *http.Request, get func(ctx context.Context, id string) (Report, error)) {
	rep, err := get(r.Context(), r.PathValue("id"))
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		e := model.AsError(err)
		w.WriteHeader(e.HTTPStatus())
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
		return
	}
	_ = json.NewEncoder(w).Encode(rep)
}
