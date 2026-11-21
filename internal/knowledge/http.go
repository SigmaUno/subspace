package knowledge

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/SigmaUno/subspace/internal/model"
)

const maxRequest = 64 << 10

type handler struct {
	s   *Service
	log *slog.Logger
}

func (s *Service) Handler(log *slog.Logger) http.Handler {
	h := &handler{s: s, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /knowledge", h.list)
	mux.HandleFunc("POST /knowledge", h.create)
	mux.HandleFunc("GET /knowledge/{id}", h.get)
	mux.HandleFunc("PATCH /knowledge/{id}", h.patch)
	mux.HandleFunc("DELETE /knowledge/{id}", h.remove)
	mux.HandleFunc("POST /knowledge/{id}/pin", h.pin)
	mux.HandleFunc("POST /knowledge/{id}/approve", h.approve)
	mux.HandleFunc("POST /knowledge/{id}/supersede", h.supersede)
	return mux
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			h.fail(w, model.BadRequest("limit must be a positive integer"))
			return
		}
		limit = n
	}
	es, err := h.s.Search(r.Context(), model.Owner, Query{
		Text: q.Get("q"), Kind: q.Get("kind"), BriefID: q.Get("brief_id"), Status: q.Get("status"), Limit: limit,
	})
	if err != nil {
		h.fail(w, err)
		return
	}
	if es == nil {
		es = []*Entry{}
	}
	h.write(w, http.StatusOK, map[string]any{"entries": es})
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var in Input
	if !h.decode(w, r, &in) {
		return
	}
	e, err := h.s.Add(r.Context(), model.Owner, in)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusCreated, e)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	h.reply(w)(h.s.Get(r.Context(), model.Owner, r.PathValue("id")))
}

func (h *handler) patch(w http.ResponseWriter, r *http.Request) {
	var p Patch
	if !h.decode(w, r, &p) {
		return
	}
	h.reply(w)(h.s.Update(r.Context(), r.PathValue("id"), p))
}

func (h *handler) remove(w http.ResponseWriter, r *http.Request) {
	if err := h.s.Delete(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) pin(w http.ResponseWriter, r *http.Request) {
	in := struct {
		Pinned *bool `json:"pinned"`
	}{}
	if !h.decode(w, r, &in) {
		return
	}
	pinned := in.Pinned == nil || *in.Pinned
	h.reply(w)(h.s.Pin(r.Context(), model.Owner, r.PathValue("id"), pinned))
}

func (h *handler) approve(w http.ResponseWriter, r *http.Request) {
	h.reply(w)(h.s.Approve(r.Context(), r.PathValue("id")))
}

func (h *handler) supersede(w http.ResponseWriter, r *http.Request) {
	var in struct {
		By string `json:"by"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	h.reply(w)(h.s.Supersede(r.Context(), model.Owner, r.PathValue("id"), in.By))
}

func (h *handler) reply(w http.ResponseWriter) func(*Entry, error) {
	return func(e *Entry, err error) {
		if err != nil {
			h.fail(w, err)
			return
		}
		h.write(w, http.StatusOK, e)
	}
}

func (h *handler) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequest))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			h.fail(w, model.Errf("too_large", "request body must be at most %d bytes", maxRequest))
			return false
		}
		h.fail(w, model.BadRequest("request body unreadable"))
		return false
	}
	if len(raw) == 0 {
		return true
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		h.fail(w, model.BadRequest("invalid JSON body"))
		return false
	}
	return true
}

func (h *handler) fail(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	status := e.HTTPStatus()
	switch e.Code {
	case "internal":
		h.log.Error("knowledge", "err", err)
	case "too_large":
		status = http.StatusRequestEntityTooLarge
	}
	h.write(w, status, map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}

func (h *handler) write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
