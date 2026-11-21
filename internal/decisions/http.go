package decisions

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/SigmaUno/subspace/internal/model"
)

const maxBody = 1 << 20

type handler struct {
	f   *Feed
	log *slog.Logger
}

func Handler(f *Feed, log *slog.Logger) http.Handler {
	h := &handler{f: f, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /decisions", h.list)
	mux.HandleFunc("GET /decisions/next", h.next)
	mux.HandleFunc("POST /decisions/{id}", h.act)
	return mux
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			h.fail(w, model.BadRequest("limit must be a positive integer"))
			return
		}
		limit = min(n, 1000)
	}
	ds, err := h.f.List(r.Context(), limit)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"decisions": ds})
}

func (h *handler) next(w http.ResponseWriter, r *http.Request) {
	d, err := h.f.Next(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"decision": d})
}

func (h *handler) act(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		h.fail(w, model.BadRequest("request body too large or unreadable"))
		return
	}
	var in struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		h.fail(w, model.BadRequest("invalid JSON body"))
		return
	}
	if in.Action == "" {
		h.fail(w, model.BadRequest("action is required"))
		return
	}
	out, err := h.f.Act(r.Context(), r.PathValue("id"), in.Action, raw)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) fail(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	if e.Code == "internal" && h.log != nil {
		h.log.Error("decisions error", "err", err)
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
