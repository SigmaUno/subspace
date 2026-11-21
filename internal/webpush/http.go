package webpush

import (
	"net/http"

	"github.com/SigmaUno/subspace/internal/httpx"
)

const maxBody = 16 << 10

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /push/vapid-key", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, Key{PublicKey: s.PublicKey()})
	})
	mux.HandleFunc("GET /push/subscriptions", s.list)
	mux.HandleFunc("POST /push/subscriptions", s.create)
	mux.HandleFunc("DELETE /push/subscriptions", s.remove)
	return noStore(mux)
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Service) fail(w http.ResponseWriter, err error) {
	httpx.Fail(w, s.o.Log, "webpush", err)
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
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"subscriptions": subs})
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	var in Input
	if err := httpx.DecodeStrict(w, r, maxBody, &in); err != nil {
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
	httpx.WriteJSON(w, status, sub)
}

func (s *Service) remove(w http.ResponseWriter, r *http.Request) {
	var in Remove
	if err := httpx.DecodeStrict(w, r, maxBody, &in); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Unsubscribe(r.Context(), in); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
