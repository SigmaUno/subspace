package autopilot

import (
	"encoding/json"
	"net/http"

	"github.com/SigmaUno/subspace/internal/model"
)

const maxBody = 1 << 16

type briefFlag struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Enabled bool   `json:"enabled"`
}

func Handler(s *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /autopilot", s.get)
	mux.HandleFunc("POST /autopilot/briefs/{id}", s.set)
	return mux
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	bs, err := s.b.ListBriefs(r.Context(), "open")
	if err != nil {
		fail(w, err)
		return
	}
	flags := make([]briefFlag, 0, len(bs))
	for _, b := range bs {
		flags = append(flags, briefFlag{ID: b.ID, Title: b.Title, Enabled: s.Enabled(b.ID)})
	}
	writeJSON(w, http.StatusOK, struct {
		Status
		Briefs []briefFlag `json:"briefs"`
	}{s.Status(), flags})
}

func (s *Service) set(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&in); err != nil {
		fail(w, model.BadRequest("invalid JSON: %v", err))
		return
	}
	if in.Enabled == nil {
		fail(w, model.BadRequest("enabled is required"))
		return
	}
	b, err := s.b.GetBrief(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	s.SetEnabled(b.ID, *in.Enabled)
	writeJSON(w, http.StatusOK, briefFlag{ID: b.ID, Title: b.Title, Enabled: *in.Enabled})
}

func fail(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	var b struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	b.Error.Code, b.Error.Message = e.Code, e.Message
	writeJSON(w, e.HTTPStatus(), b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
