package retention

import (
	"net/http"
	"strconv"

	"github.com/SigmaUno/subspace/internal/httpx"
	"github.com/SigmaUno/subspace/internal/model"
)

func Handler(s *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /retention", func(w http.ResponseWriter, r *http.Request) {
		st, err := s.Status(r.Context())
		httpx.Reply(w, http.StatusOK, st, err)
	})
	mux.HandleFunc("POST /retention/run", func(w http.ResponseWriter, r *http.Request) {
		rep, err := s.RunOnce(r.Context())
		httpx.Reply(w, http.StatusOK, rep, err)
	})
	mux.HandleFunc("GET /retention/verify", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Verify(r.Context())
		httpx.Reply(w, http.StatusOK, v, err)
	})
	mux.HandleFunc("POST /retention/segments/{id}/restore", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			httpx.WriteError(w, model.NotFound("no retention segment %s", r.PathValue("id")))
			return
		}
		seg, err := s.Restore(r.Context(), id)
		httpx.Reply(w, http.StatusOK, seg, err)
	})
	return mux
}
