package alerts

import (
	"net/http"

	"github.com/SigmaUno/subspace/internal/httpx"
	"github.com/SigmaUno/subspace/internal/model"
)

func Handler(e *Engine) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		status := q.Get("status")
		if status != "" && status != StatusActive && status != StatusResolved {
			httpx.WriteError(w, model.BadRequest("status must be active or resolved"))
			return
		}
		limit, err := httpx.PositiveLimit(r, 100, httpx.MaxListLimit)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"alerts": e.List(status, limit)})
	})
}
