package announce

import (
	"net/http"

	"github.com/SigmaUno/subspace/internal/httpx"
	"github.com/SigmaUno/subspace/internal/model"
)

const maxRequest = 16 << 10

func Handler(s *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /announcements", s.handlePublish)
	mux.HandleFunc("GET /announcements", s.handleList)
	mux.HandleFunc("DELETE /announcements/{id}", s.handleDelete)
	return mux
}

func (s *Service) handlePublish(w http.ResponseWriter, r *http.Request) {
	var in Input
	if err := httpx.DecodeStrict(w, r, maxRequest, &in); err != nil {
		httpx.WriteError(w, err)
		return
	}
	a, n, err := s.Publish(r.Context(), in)
	httpx.Reply(w, http.StatusCreated, map[string]any{"announcement": a, "delivered": n}, err)
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	var all bool
	switch v := r.URL.Query().Get("status"); v {
	case "", "active":
	case "all":
		all = true
	default:
		httpx.WriteError(w, model.BadRequest("status must be active or all"))
		return
	}
	as, err := s.List(r.Context(), all)
	httpx.Reply(w, http.StatusOK, map[string]any{"announcements": as}, err)
}

func (s *Service) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Delete(r.Context(), r.PathValue("id")); err != nil {
		httpx.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
