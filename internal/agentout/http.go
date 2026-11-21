package agentout

import (
	"net/http"

	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/httpx"
	"github.com/SigmaUno/subspace/internal/model"
)

func (s *Service) OutputHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		since, err := httpx.NonNegInt64(r, "since")
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		limit, err := httpx.PositiveLimit(r, DefaultLimit, MaxLimit)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		out, err := s.Since(r.Context(), r.PathValue("id"), since, limit, r.URL.Query().Get("task_id"))
		httpx.Reply(w, http.StatusOK, out, err)
	})
}

func (s *Service) LatestHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, err := s.Latest(r.Context())
		httpx.Reply(w, http.StatusOK, map[string]any{"agents": out}, err)
	})
}

func (s *Service) StreamHandler() http.Handler {
	sse := events.SSEHandler(s.hub, events.TopicsFromQuery)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, mixed := SplitTopics(events.TopicsFromQuery(r)); mixed {
			httpx.WriteError(w, model.BadRequest("output topics cannot be combined with other topics; open a separate stream"))
			return
		}
		sse.ServeHTTP(w, r)
	})
}
