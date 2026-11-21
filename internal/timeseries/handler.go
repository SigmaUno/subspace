package timeseries

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

func Handler(svc *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /timeseries", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		req := Request{Metric: q.Get("metric"), Bucket: q.Get("bucket"), BriefID: q.Get("brief_id")}
		if v := q.Get("since"); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				writeError(w, model.BadRequest("since must be an RFC 3339 timestamp"))
				return
			}
			req.Since = t
		}
		out, err := svc.Series(r.Context(), req)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	return mux
}

func writeError(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.HTTPStatus())
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}
