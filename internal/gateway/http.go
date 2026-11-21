package gateway

import (
	"net/http"

	"github.com/SigmaUno/subspace/internal/httpx"
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

type handler struct {
	s *Service
	httpx.Responder
}

func (s *Service) Handler() http.Handler {
	h := &handler{s: s, Responder: httpx.Responder{Log: s.log, Name: "gateways"}}
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
	h.Reply(w, http.StatusOK, List{Gateways: gs, Commands: h.s.Commands()}, err)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	g, err := h.s.Gateways.Get(r.Context(), r.PathValue("name"))
	h.Reply(w, http.StatusOK, g, err)
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var in Input
	if err := httpx.DecodeStrict(w, r, maxBody, &in); err != nil {
		h.Fail(w, err)
		return
	}
	g, err := h.s.Gateways.Create(r.Context(), in.Gateway())
	h.Reply(w, http.StatusCreated, g, err)
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	var in Input
	if err := httpx.DecodeStrict(w, r, maxBody, &in); err != nil {
		h.Fail(w, err)
		return
	}
	name := r.PathValue("name")
	if in.Name != "" && in.Name != name {
		h.Fail(w, model.BadRequest("name cannot be changed"))
		return
	}
	g, err := h.s.Gateways.Update(r.Context(), name, in.Gateway())
	h.Reply(w, http.StatusOK, g, err)
}

func (h *handler) remove(w http.ResponseWriter, r *http.Request) {
	if err := h.s.Gateways.Delete(r.Context(), r.PathValue("name")); err != nil {
		h.Fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) tools(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	tools, msg, err := h.s.Discover(r.Context(), name)
	h.Reply(w, http.StatusOK, Tools{Gateway: name, Tools: tools, Error: msg}, err)
}
