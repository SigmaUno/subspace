package prompts

import (
	"encoding/json"
	"net/http"

	"github.com/SigmaUno/subspace/internal/model"
)

func (l *Library) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /prompts", l.handleList)
	mux.HandleFunc("GET /prompts/{id}", l.handleGet)
	return mux
}

func (l *Library) handleList(w http.ResponseWriter, r *http.Request) {
	list, err := l.List(r.URL.Query().Get("tag"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"prompts": list})
}

func (l *Library) handleGet(w http.ResponseWriter, r *http.Request) {
	t, err := l.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func writeError(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	writeJSON(w, e.HTTPStatus(), map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
