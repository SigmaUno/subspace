package incident

import (
	"log/slog"
	"net/http"

	"github.com/SigmaUno/subspace/internal/httpx"
)

const (
	maxBody      = 64 << 10
	defaultLimit = 100
	maxLimit     = 1000
)

type handler struct {
	s *Service
	httpx.Responder
}

func Handler(s *Service, log *slog.Logger) http.Handler {
	h := &handler{s: s, Responder: httpx.Responder{Log: log, Name: "incident"}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /incident", h.start)
	mux.HandleFunc("GET /incident", h.list)
	mux.HandleFunc("POST /incident/{id}/end", h.end)
	return mux
}

func (h *handler) start(w http.ResponseWriter, r *http.Request) {
	var req Request
	if err := httpx.DecodeStrict(w, r, maxBody, &req); err != nil {
		h.Fail(w, err)
		return
	}
	out, err := h.s.Start(r.Context(), req)
	status := http.StatusOK
	if err == nil && out.Created {
		status = http.StatusCreated
	}
	h.Reply(w, status, out, err)
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	limit, err := httpx.BoundedInt(r.URL.Query().Get("limit"), "limit", defaultLimit, maxLimit)
	if err != nil {
		h.Fail(w, err)
		return
	}
	list, err := h.s.List(r.Context(), limit)
	h.Reply(w, http.StatusOK, map[string]any{"incidents": list}, err)
}

func (h *handler) end(w http.ResponseWriter, r *http.Request) {
	var req EndRequest
	if err := httpx.DecodeOptional(w, r, maxBody, &req); err != nil {
		h.Fail(w, err)
		return
	}
	out, err := h.s.End(r.Context(), r.PathValue("id"), req)
	h.Reply(w, http.StatusOK, out, err)
}
