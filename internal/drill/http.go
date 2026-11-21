package drill

import (
	"log/slog"
	"net/http"

	"github.com/SigmaUno/subspace/internal/httpx"
)

const (
	maxBody      = 4 << 10
	defaultLimit = 100
)

type handler struct {
	s *Service
	httpx.Responder
}

func Handler(s *Service, log *slog.Logger) http.Handler {
	h := &handler{s: s, Responder: httpx.Responder{Log: log, Name: "drill"}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /drills", h.list)
	mux.HandleFunc("POST /drills", h.run)
	return mux
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	limit, err := httpx.BoundedInt(r.URL.Query().Get("limit"), "limit", defaultLimit, maxListLimit)
	if err != nil {
		h.Fail(w, err)
		return
	}
	list, err := h.s.List(r.Context(), limit)
	h.Reply(w, http.StatusOK, map[string]any{"drills": list}, err)
}

func (h *handler) run(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if err := httpx.DecodeOptional(w, r, maxBody, &req); err != nil {
		h.Fail(w, err)
		return
	}
	res, err := h.s.RunOnce(r.Context(), TriggerManual)
	h.Reply(w, http.StatusCreated, res, err)
}
