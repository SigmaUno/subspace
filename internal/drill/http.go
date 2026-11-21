package drill

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
	maxBody      = 4 << 10
	defaultLimit = 100
)

type handler struct {
	s   *Service
	log *slog.Logger
}

func Handler(s *Service, log *slog.Logger) http.Handler {
	h := &handler{s: s, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /drills", h.list)
	mux.HandleFunc("POST /drills", h.run)
	return mux
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	limit := defaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxListLimit {
			h.fail(w, model.BadRequest("limit must be an integer between 1 and %d", maxListLimit))
			return
		}
		limit = n
	}
	list, err := h.s.List(r.Context(), limit)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusOK, map[string]any{"drills": list})
}

func (h *handler) run(w http.ResponseWriter, r *http.Request) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	var req struct{}
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		h.fail(w, model.BadRequest("invalid JSON body"))
		return
	}
	res, err := h.s.RunOnce(r.Context(), TriggerManual)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusCreated, res)
}

func (h *handler) fail(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	if e.Code == "internal" {
		h.log.Error("drill", "err", err)
	}
	h.write(w, e.HTTPStatus(), map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}

func (h *handler) write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
