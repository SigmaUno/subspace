package tokens

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/SigmaUno/subspace/internal/model"
)

type Created struct {
	Token  *Token `json:"token"`
	Secret string `json:"secret"`
}

func Handler(s *Service, log *slog.Logger) http.Handler {
	h := &handler{s: s, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tokens", h.list)
	mux.HandleFunc("POST /tokens", h.create)
	mux.HandleFunc("POST /tokens/{id}/revoke", h.revoke)
	return mux
}

type handler struct {
	s   *Service
	log *slog.Logger
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	ts, err := h.s.List(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	if ts == nil {
		ts = []*Token{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": ts})
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		h.fail(w, model.BadRequest("invalid JSON body"))
		return
	}
	t, secret, err := h.s.Create(r.Context(), in)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, Created{Token: t, Secret: secret})
}

func (h *handler) revoke(w http.ResponseWriter, r *http.Request) {
	t, err := h.s.Revoke(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *handler) fail(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	if e.Code == "internal" && h.log != nil {
		h.log.Error("tokens", "err", err)
	}
	writeJSON(w, e.HTTPStatus(), map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
