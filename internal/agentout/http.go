package agentout

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/model"
)

func (s *Service) OutputHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		since, err := intParam(q.Get("since"), 0)
		if err != nil {
			fail(w, model.BadRequest("since must be a non-negative integer"))
			return
		}
		limit, err := intParam(q.Get("limit"), DefaultLimit)
		if err != nil || limit < 1 {
			fail(w, model.BadRequest("limit must be a positive integer"))
			return
		}
		out, err := s.Since(r.Context(), r.PathValue("id"), since, int(min(limit, MaxLimit)), q.Get("task_id"))
		if err != nil {
			fail(w, err)
			return
		}
		write(w, http.StatusOK, out)
	})
}

func (s *Service) LatestHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, err := s.Latest(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		write(w, http.StatusOK, map[string]any{"agents": out})
	})
}

func (s *Service) StreamHandler() http.Handler {
	sse := events.SSEHandler(s.hub, events.TopicsFromQuery)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, mixed := SplitTopics(events.TopicsFromQuery(r)); mixed {
			fail(w, model.BadRequest("output topics cannot be combined with other topics; open a separate stream"))
			return
		}
		sse.ServeHTTP(w, r)
	})
}

func intParam(v string, def int64) (int64, error) {
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, strconv.ErrSyntax
	}
	return n, nil
}

func fail(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	write(w, e.HTTPStatus(), map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
