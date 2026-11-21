package schedule

import (
	"net/http"

	"github.com/SigmaUno/subspace/internal/httpx"
)

const maxRequest = 256 << 10

func Handler(s *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /schedules", s.handleCreate)
	mux.HandleFunc("GET /schedules", s.handleList)
	mux.HandleFunc("GET /schedules/preview", s.handlePreview)
	mux.HandleFunc("GET /schedules/{id}", s.handleGet)
	mux.HandleFunc("PATCH /schedules/{id}", s.handleUpdate)
	mux.HandleFunc("DELETE /schedules/{id}", s.handleDelete)
	mux.HandleFunc("GET /schedules/{id}/runs", s.handleRuns)
	return mux
}

func (s *Service) handleCreate(w http.ResponseWriter, r *http.Request) {
	var in Input
	if err := httpx.DecodeStrict(w, r, maxRequest, &in); err != nil {
		httpx.WriteError(w, err)
		return
	}
	sc, err := s.Create(r.Context(), in)
	httpx.Reply(w, http.StatusCreated, sc, err)
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	all, err := s.List(r.Context())
	httpx.Reply(w, http.StatusOK, map[string]any{"schedules": all}, err)
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	sc, err := s.Get(r.Context(), r.PathValue("id"))
	httpx.Reply(w, http.StatusOK, sc, err)
}

func (s *Service) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var p Patch
	if err := httpx.DecodeStrict(w, r, maxRequest, &p); err != nil {
		httpx.WriteError(w, err)
		return
	}
	sc, err := s.Update(r.Context(), r.PathValue("id"), p)
	httpx.Reply(w, http.StatusOK, sc, err)
}

func (s *Service) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Delete(r.Context(), r.PathValue("id")); err != nil {
		httpx.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleRuns(w http.ResponseWriter, r *http.Request) {
	limit, err := httpx.BoundedInt(r.URL.Query().Get("limit"), "limit", runsLimit, 1000)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	rs, err := s.Runs(r.Context(), r.PathValue("id"), limit)
	httpx.Reply(w, http.StatusOK, map[string]any{"runs": rs}, err)
}

func (s *Service) handlePreview(w http.ResponseWriter, r *http.Request) {
	count, err := httpx.BoundedInt(r.URL.Query().Get("count"), "count", 5, MaxPreview)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	p, err := s.Preview(r.URL.Query().Get("cron"), r.URL.Query().Get("timezone"), count)
	httpx.Reply(w, http.StatusOK, p, err)
}
