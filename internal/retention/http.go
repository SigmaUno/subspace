package retention

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/SigmaUno/subspace/internal/model"
)

func Handler(s *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /retention", func(w http.ResponseWriter, r *http.Request) {
		st, err := s.Status(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		write(w, http.StatusOK, st)
	})
	mux.HandleFunc("POST /retention/run", func(w http.ResponseWriter, r *http.Request) {
		rep, err := s.RunOnce(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		write(w, http.StatusOK, rep)
	})
	mux.HandleFunc("GET /retention/verify", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Verify(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		write(w, http.StatusOK, v)
	})
	mux.HandleFunc("POST /retention/segments/{id}/restore", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			fail(w, model.NotFound("no retention segment %s", r.PathValue("id")))
			return
		}
		seg, err := s.Restore(r.Context(), id)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, http.StatusOK, seg)
	})
	return mux
}

func fail(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	write(w, e.HTTPStatus(), map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}

func write(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
