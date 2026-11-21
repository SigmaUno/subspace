package incident

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/SigmaUno/subspace/internal/model"
)

const (
	maxBody      = 64 << 10
	defaultLimit = 100
	maxLimit     = 1000
)

type handler struct {
	s   *Service
	log *slog.Logger
}

func Handler(s *Service, log *slog.Logger) http.Handler {
	h := &handler{s: s, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /incident", h.start)
	mux.HandleFunc("GET /incident", h.list)
	mux.HandleFunc("POST /incident/{id}/end", h.end)
	return mux
}

func (h *handler) start(w http.ResponseWriter, r *http.Request) {
	var req Request
	if !h.decode(w, r, &req, false) {
		return
	}
	out, err := h.s.Start(r.Context(), req)
	if err != nil {
		h.fail(w, err)
		return
	}
	status := http.StatusOK
	if out.Created {
		status = http.StatusCreated
	}
	h.write(w, status, out)
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	limit := defaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxLimit {
			h.fail(w, model.BadRequest("limit must be an integer between 1 and %d", maxLimit))
			return
		}
		limit = n
	}
	list, err := h.s.List(r.Context(), limit)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusOK, map[string]any{"incidents": list})
}

func (h *handler) end(w http.ResponseWriter, r *http.Request) {
	var req EndRequest
	if !h.decode(w, r, &req, true) {
		return
	}
	out, err := h.s.End(r.Context(), r.PathValue("id"), req)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusOK, out)
}

func (h *handler) decode(w http.ResponseWriter, r *http.Request, v any, optional bool) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && (!optional || !errors.Is(err, io.EOF)) {
		h.fail(w, model.BadRequest("invalid JSON body"))
		return false
	}
	return true
}

func (h *handler) fail(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	if e.Code == "internal" {
		h.log.Error("incident", "err", err)
	}
	h.write(w, e.HTTPStatus(), map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}

func (h *handler) write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
