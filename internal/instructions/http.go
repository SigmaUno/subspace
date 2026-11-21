package instructions

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
	svc *Service
	log *slog.Logger
}

func Handler(svc *Service, log *slog.Logger) http.Handler {
	h := &handler{svc: svc, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /instructions/effective", h.effective)
	mux.HandleFunc("GET /instructions/{scope}/{kind}", h.get)
	mux.HandleFunc("PUT /instructions/{scope}/{kind}", h.save)
	mux.HandleFunc("GET /instructions/{scope}/{kind}/versions", h.history)
	mux.HandleFunc("GET /instructions/{scope}/{kind}/versions/{n}", h.version)
	mux.HandleFunc("GET /instructions/{scope}/{kind}/diff", h.diff)
	mux.HandleFunc("POST /instructions/{scope}/{kind}/rollback", h.rollback)
	return mux
}

func (h *handler) effective(w http.ResponseWriter, r *http.Request) {
	e, err := h.svc.Effective(r.Context(), r.URL.Query().Get("project"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.Get(r.Context(), r.PathValue("scope"), r.PathValue("kind"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *handler) save(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text    string `json:"text"`
		Message string `json:"message"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	v, err := h.svc.Save(r.Context(), model.Owner, r.PathValue("scope"), r.PathValue("kind"), in.Text, in.Message)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (h *handler) history(w http.ResponseWriter, r *http.Request) {
	ms, err := h.svc.History(r.Context(), r.PathValue("scope"), r.PathValue("kind"))
	if err != nil {
		h.fail(w, err)
		return
	}
	if ms == nil {
		ms = []Meta{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": ms})
}

func (h *handler) version(w http.ResponseWriter, r *http.Request) {
	n, ok := h.number(w, "n", r.PathValue("n"))
	if !ok {
		return
	}
	v, err := h.svc.GetVersion(r.Context(), r.PathValue("scope"), r.PathValue("kind"), n)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *handler) diff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, ok := h.number(w, "from", q.Get("from"))
	if !ok {
		return
	}
	to, ok := h.number(w, "to", q.Get("to"))
	if !ok {
		return
	}
	lines, err := h.svc.Diff(r.Context(), r.PathValue("scope"), r.PathValue("kind"), from, to)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "lines": lines})
}

func (h *handler) rollback(w http.ResponseWriter, r *http.Request) {
	var in struct {
		To      int    `json:"to"`
		Message string `json:"message"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	if in.To < 1 {
		h.fail(w, model.BadRequest("to must be a positive version number"))
		return
	}
	v, err := h.svc.Rollback(r.Context(), model.Owner, r.PathValue("scope"), r.PathValue("kind"), in.To, in.Message)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (h *handler) number(w http.ResponseWriter, name, s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		h.fail(w, model.BadRequest("%s must be a positive version number", name))
		return 0, false
	}
	return n, true
}

func (h *handler) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		h.fail(w, model.BadRequest("request body too large or unreadable"))
		return false
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
		h.log.Error("instructions error", "err", err)
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
