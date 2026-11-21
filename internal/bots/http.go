package bots

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/SigmaUno/subspace/internal/model"
)

const maxBody = 1 << 20

type handler struct {
	s   *Service
	log *slog.Logger
}

func Handler(s *Service, log *slog.Logger) http.Handler {
	h := &handler{s: s, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /bots", h.list)
	mux.HandleFunc("POST /bots", h.create)
	mux.HandleFunc("POST /bots/bulk", h.bulk)
	mux.HandleFunc("POST /bots/{id}/revoke", h.revoke)
	mux.HandleFunc("POST /bots/{id}/pause", h.pause)
	mux.HandleFunc("POST /bots/{id}/resume", h.resume)
	mux.HandleFunc("POST /bots/{id}/kill", h.kill)
	mux.HandleFunc("POST /bots/{id}/spawn", h.spawn)
	mux.HandleFunc("DELETE /bots/{id}", h.delete)
	return mux
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	bs, err := h.s.List(r.Context(), q.Get("role"), q.Get("status"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bots": bs})
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if !h.decode(w, r, &in) {
		return
	}
	c, err := h.s.Create(r.Context(), model.Owner, in)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (h *handler) bulk(w http.ResponseWriter, r *http.Request) {
	var in BulkInput
	if !h.decode(w, r, &in) {
		return
	}
	cs, err := h.s.BulkCreate(r.Context(), model.Owner, in)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"bots": cs})
}

func (h *handler) revoke(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	h.respond(w)(h.s.Revoke(r.Context(), model.Owner, r.PathValue("id"), in.Reason))
}

func (h *handler) pause(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	h.respond(w)(h.s.Pause(r.Context(), model.Owner, r.PathValue("id"), in.Reason))
}

func (h *handler) resume(w http.ResponseWriter, r *http.Request) {
	h.respond(w)(h.s.Resume(r.Context(), model.Owner, r.PathValue("id")))
}

func (h *handler) kill(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	h.respond(w)(h.s.Kill(r.Context(), model.Owner, r.PathValue("id"), in.Reason))
}

func (h *handler) spawn(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Repo string `json:"repo"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	h.respond(w)(h.s.Spawn(r.Context(), model.Owner, r.PathValue("id"), in.Repo))
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.s.Delete(r.Context(), model.Owner, r.PathValue("id")); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) respond(w http.ResponseWriter) func(*Bot, error) {
	return func(b *Bot, err error) {
		if err != nil {
			h.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"bot": b})
	}
}

func (h *handler) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		h.fail(w, model.BadRequest("request body too large or unreadable"))
		return false
	}
	if len(raw) == 0 {
		return true
	}
	if err := json.Unmarshal(raw, v); err != nil {
		h.fail(w, model.BadRequest("invalid JSON body"))
		return false
	}
	return true
}

func (h *handler) fail(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	if e.Code == "internal" && h.log != nil {
		h.log.Error("bots error", "err", err)
	}
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
