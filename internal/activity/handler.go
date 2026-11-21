package activity

import (
	"net/http"
	"strconv"
	"time"

	"github.com/SigmaUno/subspace/internal/httpx"
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
				httpx.WriteError(w, model.BadRequest("since must be an RFC 3339 timestamp"))
				return
			}
			q.Since = t
		}
		if s := v.Get("limit"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				httpx.WriteError(w, model.BadRequest("limit must be a positive integer"))
				return
			}
			q.Limit = n
		}
		page, err := svc.List(r.Context(), q)
		httpx.Reply(w, http.StatusOK, page, err)
	})
	return mux
}
