package gateway

import (
	"encoding/json"
	"net/http"

	"github.com/SigmaUno/subspace/internal/model"
)

const maxBody = 1 << 20

type List struct {
	Gateways []Gateway `json:"gateways"`
	Commands []string  `json:"commands"`
}

type Tools struct {
	Gateway string       `json:"gateway"`
	Tools   []Discovered `json:"tools"`
	Error   string       `json:"error"`
}

type handler struct{ s *Service }

func (s *Service) Handler() http.Handler {
	h := &handler{s: s}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /gateways", h.list)
	mux.HandleFunc("POST /gateways", h.create)
	mux.HandleFunc("GET /gateways/{name}", h.get)
	mux.HandleFunc("PUT /gateways/{name}", h.update)
	mux.HandleFunc("DELETE /gateways/{name}", h.remove)
	mux.HandleFunc("GET /gateways/{name}/tools", h.tools)
	return mux
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	gs, err := h.s.Gateways.List(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusOK, List{Gateways: gs, Commands: h.s.Commands()})
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	g, err := h.s.Gateways.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusOK, g)
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var in Input
	if !h.decode(w, r, &in) {
		return
	}
	g, err := h.s.Gateways.Create(r.Context(), in.Gateway())
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusCreated, g)
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	var in Input
	if !h.decode(w, r, &in) {
		return
	}
	name := r.PathValue("name")
	if in.Name != "" && in.Name != name {
		h.fail(w, model.BadRequest("name cannot be changed"))
		return
	}
	g, err := h.s.Gateways.Update(r.Context(), name, in.Gateway())
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusOK, g)
}

func (h *handler) remove(w http.ResponseWriter, r *http.Request) {
	if err := h.s.Gateways.Delete(r.Context(), r.PathValue("name")); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) tools(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	tools, msg, err := h.s.Discover(r.Context(), name)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.write(w, http.StatusOK, Tools{Gateway: name, Tools: tools, Error: msg})
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
	if e.Code == "internal" && h.s.log != nil {
		h.s.log.Error("gateways", "err", err)
	}
	h.write(w, e.HTTPStatus(), map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}

func (h *handler) write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
