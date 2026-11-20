package usage

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

type Handler struct {
	S   *Service
	mux *http.ServeMux
}

func NewHandler(s *Service) *Handler {
	h := &Handler{S: s, mux: http.NewServeMux()}
	h.mux.HandleFunc("GET /usage/summary", h.summary)
	h.mux.HandleFunc("GET /usage/series", h.series)
	h.mux.HandleFunc("GET /usage/providers", h.providers)
	h.mux.HandleFunc("GET /usage/capacity", h.capacity)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

func (h *Handler) query(r *http.Request) (Query, error) {
	v := r.URL.Query()
	q := Query{
		BriefID: v.Get("brief_id"), AgentID: v.Get("agent_id"), TaskID: v.Get("task_id"),
		Provider: v.Get("provider"), Model: v.Get("model"), Source: v.Get("source"),
	}
	q.To = h.S.now().Truncate(time.Minute).Add(time.Minute)
	if s := v.Get("to"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return q, model.BadRequest("invalid to: %v", err)
		}
		q.To = t.UTC()
	}
	q.From = q.To.Add(-24 * time.Hour)
	if s := v.Get("from"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return q, model.BadRequest("invalid from: %v", err)
		}
		q.From = t.UTC()
	}
	if !q.From.Before(q.To) {
		return q, model.BadRequest("from must be before to")
	}
	return q, nil
}

func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	q, err := h.query(r)
	if err != nil {
		fail(w, err)
		return
	}
	s, err := h.S.Summary(r.Context(), q, r.URL.Query().Get("group_by"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (h *Handler) series(w http.ResponseWriter, r *http.Request) {
	q, err := h.query(r)
	if err != nil {
		fail(w, err)
		return
	}
	bucket, by := r.URL.Query().Get("bucket"), r.URL.Query().Get("group_by")
	if bucket == "" {
		bucket = "hour"
	}
	ss, err := h.S.Series(r.Context(), q, bucket, by)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": q.From, "to": q.To, "bucket": bucket, "group_by": by, "series": ss})
}

func (h *Handler) providers(w http.ResponseWriter, r *http.Request) {
	ps, err := h.S.Providers(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": ps})
}

func (h *Handler) capacity(w http.ResponseWriter, r *http.Request) {
	c, err := h.S.LiveCapacity(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

type errBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func fail(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	var b errBody
	b.Error.Code, b.Error.Message = e.Code, e.Message
	writeJSON(w, e.HTTPStatus(), b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
