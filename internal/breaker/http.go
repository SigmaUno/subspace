package breaker

import (
	"encoding/json"
	"net/http"

	"github.com/SigmaUno/subspace/internal/model"
)

func Handler(s *Set) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /breakers", func(w http.ResponseWriter, r *http.Request) {
		write(w, http.StatusOK, map[string]any{"breakers": s.List()})
	})
	mux.HandleFunc("POST /breakers/{provider}/reset", func(w http.ResponseWriter, r *http.Request) {
		p := r.PathValue("provider")
		out := s.Reset(p)
		if len(out) == 0 {
			fail(w, model.NotFound("no breaker for provider %s", p))
			return
		}
		write(w, http.StatusOK, map[string]any{"breakers": out})
	})
	return mux
}

func ProbeHandler(p *Prober) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, err := p.List(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		write(w, http.StatusOK, map[string]any{"providers": out})
	})
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
