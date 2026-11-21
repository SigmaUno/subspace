package webpush

import (
	"encoding/json"
	"net/http"

	"github.com/SigmaUno/subspace/internal/model"
)

const maxBody = 16 << 10

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /push/vapid-key", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, Key{PublicKey: s.PublicKey()})
	})
	mux.HandleFunc("GET /push/subscriptions", s.list)
	mux.HandleFunc("POST /push/subscriptions", s.create)
	mux.HandleFunc("DELETE /push/subscriptions", s.remove)
	return mux
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	subs, err := s.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if subs == nil {
		subs = []*Subscription{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": subs})
}

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return model.BadRequest("invalid JSON body")
	}
	return nil
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	var in Input
	if err := decode(w, r, &in); err != nil {
		s.fail(w, err)
		return
	}
	sub, created, err := s.Subscribe(r.Context(), in)
	if err != nil {
		s.fail(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, sub)
}

func (s *Service) remove(w http.ResponseWriter, r *http.Request) {
	var in Remove
	if err := decode(w, r, &in); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Unsubscribe(r.Context(), in); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) fail(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	if e.Code == "internal" {
		s.o.Log.Error("webpush", "err", err)
	}
	writeJSON(w, e.HTTPStatus(), map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
