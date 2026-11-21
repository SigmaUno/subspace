package webhookout

import (
	"log/slog"
	"net/http"

	"github.com/SigmaUno/subspace/internal/httpx"
)

type Created struct {
	Webhook *Subscription `json:"webhook"`
	Secret  string        `json:"secret"`
}

func (s *Service) Handler() http.Handler {
	h := &handler{s: s, log: s.o.Log}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks-out", h.create)
	mux.HandleFunc("GET /webhooks-out", h.list)
	mux.HandleFunc("GET /webhooks-out/{id}", h.get)
	mux.HandleFunc("DELETE /webhooks-out/{id}", h.delete)
	mux.HandleFunc("GET /webhooks-out/{id}/deliveries", h.deliveries)
	mux.HandleFunc("POST /webhooks-out/{id}/deliveries/{delivery}/redeliver", h.redeliver)
	return mux
}

type handler struct {
	s   *Service
	log *slog.Logger
}

func (h *handler) fail(w http.ResponseWriter, err error) {
	httpx.Fail(w, h.log, "webhooks-out", err)
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if err := httpx.DecodeStrict(w, r, 64<<10, &in); err != nil {
		h.fail(w, err)
		return
	}
	sub, err := h.s.Create(r.Context(), in)
	if err != nil {
		h.fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, Created{Webhook: sub, Secret: sub.Secret})
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	subs, err := h.s.List(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	if subs == nil {
		subs = []*Subscription{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"webhooks": subs})
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	sub, err := h.s.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, sub)
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.s.Delete(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) deliveries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, err := httpx.BoundedInt(q.Get("limit"), "limit", 100, 1000)
	if err != nil {
		h.fail(w, err)
		return
	}
	ds, err := h.s.Deliveries(r.Context(), r.PathValue("id"), q.Get("state"), limit)
	if err != nil {
		h.fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"deliveries": ds})
}

func (h *handler) redeliver(w http.ResponseWriter, r *http.Request) {
	d, err := h.s.Redeliver(r.Context(), r.PathValue("id"), r.PathValue("delivery"))
	if err != nil {
		h.fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, d)
}
