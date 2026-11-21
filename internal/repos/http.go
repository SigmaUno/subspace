package repos

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

const (
	maxBody       = 1 << 20
	healthTimeout = 20 * time.Second
)

type handler struct {
	s    *Store
	http *http.Client
	log  *slog.Logger
}

func Handler(s *Store, hc *http.Client, log *slog.Logger) http.Handler {
	h := &handler{s: s, http: hc, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos", h.list)
	mux.HandleFunc("POST /repos", h.create)
	mux.HandleFunc("GET /repos/{owner}/{name}", h.get)
	mux.HandleFunc("PUT /repos/{owner}/{name}", h.update)
	mux.HandleFunc("DELETE /repos/{owner}/{name}", h.remove)
	mux.HandleFunc("GET /repos/{owner}/{name}/health", h.health)
	return mux
}

func fullName(r *http.Request) string { return r.PathValue("owner") + "/" + r.PathValue("name") }

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	rs, err := h.s.List(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusOK, map[string]any{"repos": rs})
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	repo, err := h.s.Get(r.Context(), fullName(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusOK, repo)
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var in Input
	if !h.decode(w, r, &in) {
		return
	}
	repo, err := h.s.Create(r.Context(), in.Repo())
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusCreated, repo)
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	var in Input
	if !h.decode(w, r, &in) {
		return
	}
	name := fullName(r)
	if in.Name != "" && in.Name != name {
		h.fail(w, model.BadRequest("name cannot be changed"))
		return
	}
	repo, err := h.s.Update(r.Context(), name, in.Repo())
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusOK, repo)
}

func (h *handler) remove(w http.ResponseWriter, r *http.Request) {
	if err := h.s.Delete(r.Context(), fullName(r)); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) health(w http.ResponseWriter, r *http.Request) {
	repo, err := h.s.Get(r.Context(), fullName(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
	defer cancel()
	h.write(w, http.StatusOK, Check(ctx, repo, h.http, h.s.now()))
}

func (h *handler) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		h.fail(w, model.BadRequest("invalid JSON body"))
		return false
	}
	return true
}

func (h *handler) fail(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	if e.Code == "internal" && h.log != nil {
		h.log.Error("repository registry", "err", err)
	}
	h.write(w, e.HTTPStatus(), map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}

func (h *handler) write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
