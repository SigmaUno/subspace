package schedule

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/SigmaUno/subspace/internal/model"
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

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequest))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil || dec.More() {
		return model.BadRequest("invalid JSON body")
	}
	return nil
}

func (s *Service) handleCreate(w http.ResponseWriter, r *http.Request) {
	var in Input
	if err := decode(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	sc, err := s.Create(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sc)
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	all, err := s.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": all})
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	sc, err := s.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

func (s *Service) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var p Patch
	if err := decode(w, r, &p); err != nil {
		writeError(w, err)
		return
	}
	sc, err := s.Update(r.Context(), r.PathValue("id"), p)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

func (s *Service) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleRuns(w http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r, "limit", runsLimit, 1000)
	if err != nil {
		writeError(w, err)
		return
	}
	rs, err := s.Runs(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": rs})
}

func (s *Service) handlePreview(w http.ResponseWriter, r *http.Request) {
	count, err := intParam(r, "count", 5, MaxPreview)
	if err != nil {
		writeError(w, err)
		return
	}
	p, err := s.Preview(r.URL.Query().Get("cron"), r.URL.Query().Get("timezone"), count)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func intParam(r *http.Request, name string, def, max int) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > max {
		return 0, model.BadRequest("%s must be an integer between 1 and %d", name, max)
	}
	return n, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	writeJSON(w, e.HTTPStatus(), map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}
