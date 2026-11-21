package announce

import (
	"encoding/json"
	"net/http"

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
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequest))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, model.BadRequest("invalid JSON body"))
		return
	}
	a, n, err := s.Publish(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"announcement": a, "delivered": n})
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	var all bool
	switch v := r.URL.Query().Get("status"); v {
	case "", "active":
	case "all":
		all = true
	default:
		writeError(w, model.BadRequest("status must be active or all"))
		return
	}
	as, err := s.List(r.Context(), all)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"announcements": as})
}

func (s *Service) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
