package chat

import (
	"encoding/json"
	"net/http"

	"github.com/SigmaUno/subspace/internal/model"
)

const maxBody = 64 << 10

func (r *Router) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /messages", r.handleSend)
	mux.HandleFunc("GET /conversations", r.handleList)
	mux.HandleFunc("GET /conversations/{id}", r.handleGet)
	return mux
}

func (r *Router) handleSend(w http.ResponseWriter, req *http.Request) {
	var m Message
	dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		writeError(w, model.BadRequest("invalid JSON body"))
		return
	}
	conv, err := r.Send(req.Context(), m)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, conv)
}

func (r *Router) handleList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"conversations": r.Conversations()})
}

func (r *Router) handleGet(w http.ResponseWriter, req *http.Request) {
	conv, err := r.Conversation(req.Context(), req.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, conv)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	writeJSON(w, e.HTTPStatus(), map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}
