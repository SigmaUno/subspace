package alerts

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/SigmaUno/subspace/internal/model"
)

func Handler(e *Engine) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		status := q.Get("status")
		if status != "" && status != StatusActive && status != StatusResolved {
			fail(w, model.BadRequest("status must be active or resolved"))
			return
		}
		limit := 100
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				fail(w, model.BadRequest("limit must be a positive integer"))
				return
			}
			limit = min(n, 1000)
		}
		write(w, http.StatusOK, map[string]any{"alerts": e.List(status, limit)})
	})
}

func fail(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	write(w, e.HTTPStatus(), map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
