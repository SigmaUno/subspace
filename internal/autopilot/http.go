package autopilot

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/SigmaUno/subspace/internal/model"
)

const maxBody = 1 << 16

type briefFlag struct {
	ID      string       `json:"id"`
	Title   string       `json:"title"`
	Enabled bool         `json:"enabled"`
	Spawn   *SpawnConfig `json:"spawn,omitempty"`
}

func (s *Service) flag(b *model.Brief) briefFlag {
	f := briefFlag{ID: b.ID, Title: b.Title, Enabled: s.Enabled(b.ID)}
	if c := s.Spawn(b.ID); c != (SpawnConfig{}) {
		f.Spawn = &c
	}
	return f
}

func Handler(s *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /autopilot", s.get)
	mux.HandleFunc("POST /autopilot/briefs/{id}", s.set)
	mux.HandleFunc("PUT /autopilot/briefs/{id}/spawn", s.setSpawn)
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
		flags = append(flags, s.flag(b))
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
		fail(w, model.BadRequest("invalid JSON body"))
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
	writeJSON(w, http.StatusOK, s.flag(b))
}

var spawnKinds = []string{model.KindClaudeCode, model.KindCodex, model.KindOpenCode}

func (s *Service) setSpawn(w http.ResponseWriter, r *http.Request) {
	var c SpawnConfig
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		fail(w, model.BadRequest("invalid JSON body"))
		return
	}
	b, err := s.b.GetBrief(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	if c != (SpawnConfig{}) {
		resolved, err := s.ResolveProfile(r.Context(), c)
		if err != nil {
			if model.AsError(err).Code == "not_found" {
				err = model.BadRequest("unknown profile %q", c.Profile)
			}
			fail(w, err)
			return
		}
		if !slices.Contains(spawnKinds, resolved.Kind) || resolved.Model == "" {
			fail(w, model.BadRequest("worker_kind (claude-code, codex or opencode) and model are required, directly or through a profile"))
			return
		}
	}
	s.SetSpawn(b.ID, c)
	writeJSON(w, http.StatusOK, s.flag(b))
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
