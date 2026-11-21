package playbooks

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/SigmaUno/subspace/internal/model"
)

const maxBody = 1 << 20

type handler struct {
	s     *Store
	inUse InUse
	log   *slog.Logger
}

func Handler(s *Store, inUse InUse, log *slog.Logger) http.Handler {
	h := &handler{s: s, inUse: inUse, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /playbooks", h.list)
	mux.HandleFunc("POST /playbooks", h.create)
	mux.HandleFunc("GET /playbooks/{name}", h.get)
	mux.HandleFunc("PUT /playbooks/{name}", h.update)
	mux.HandleFunc("DELETE /playbooks/{name}", h.remove)
	mux.HandleFunc("POST /playbooks/{name}/clone", h.clone)
	return mux
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	ps, err := h.s.List(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusOK, map[string]any{"playbooks": ps})
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	p, err := h.s.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusOK, p)
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var in Playbook
	if !h.decode(w, r, &in) {
		return
	}
	p, err := h.s.Create(r.Context(), in)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusCreated, p)
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	var in Playbook
	if !h.decode(w, r, &in) {
		return
	}
	name := r.PathValue("name")
	if in.Name != "" && in.Name != name {
		h.fail(w, model.BadRequest("name cannot be changed"))
		return
	}
	p, err := h.s.Update(r.Context(), name, in)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusOK, p)
}

func (h *handler) clone(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	p, err := h.s.Clone(r.Context(), r.PathValue("name"), in.Name, in.Description)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusCreated, p)
}

func (h *handler) remove(w http.ResponseWriter, r *http.Request) {
	if err := h.s.Delete(r.Context(), r.PathValue("name"), h.inUse); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
		h.log.Error("playbooks", "err", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.HTTPStatus())
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}

func (h *handler) write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
