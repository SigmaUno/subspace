package activity

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

func Handler(svc *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /activity", func(w http.ResponseWriter, r *http.Request) {
		v := r.URL.Query()
		q := Query{BriefID: v.Get("brief_id"), Severity: v.Get("severity"), Cursor: v.Get("cursor")}
		if s := v.Get("since"); s != "" {
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				writeError(w, model.BadRequest("since must be an RFC 3339 timestamp"))
				return
			}
			q.Since = t
		}
		if s := v.Get("limit"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				writeError(w, model.BadRequest("limit must be a positive integer"))
				return
			}
			q.Limit = n
		}
		page, err := svc.List(r.Context(), q)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(page)
	})
	return mux
}

func writeError(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.HTTPStatus())
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}
