package webhookout

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/SigmaUno/subspace/internal/model"
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

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		h.fail(w, model.BadRequest("invalid JSON body"))
		return
	}
	sub, err := h.s.Create(r.Context(), in)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, Created{Webhook: sub, Secret: sub.Secret})
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
	writeJSON(w, http.StatusOK, map[string]any{"webhooks": subs})
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	sub, err := h.s.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sub)
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
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			h.fail(w, model.BadRequest("limit must be between 1 and 1000"))
			return
		}
		limit = n
	}
	ds, err := h.s.Deliveries(r.Context(), r.PathValue("id"), q.Get("state"), limit)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": ds})
}

func (h *handler) redeliver(w http.ResponseWriter, r *http.Request) {
	d, err := h.s.Redeliver(r.Context(), r.PathValue("id"), r.PathValue("delivery"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *handler) fail(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	if e.Code == "internal" {
		h.log.Error("webhooks-out", "err", err)
	}
	writeJSON(w, e.HTTPStatus(), map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
