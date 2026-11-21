package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

func (s *Server) capabilities() map[string]any {
	push := s.Push != nil
	return map[string]any{
		"tools":     map[string]any{"listChanged": push},
		"prompts":   map[string]any{"listChanged": false},
		"resources": map[string]any{"subscribe": push, "listChanged": false},
	}
}

func (s *Server) serveStream(w http.ResponseWriter, r *http.Request, sid, agent string) {
	if !acceptsEventStream(r) {
		http.Error(w, "Accept must include text/event-stream", http.StatusNotAcceptable)
		return
	}
	if sid == "" {
		http.Error(w, "Mcp-Session-Id required", http.StatusBadRequest)
		return
	}
	ok, err := s.checkSession(r.Context(), sid, agent)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	s.Push.Serve(w, r, sid, agent)
}

func (s *Server) subscription(ctx context.Context, method, sid, agent string, params json.RawMessage) (any, *rpcErr) {
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.URI == "" {
		return nil, &rpcErr{errInvalidResourceArgs, "invalid params: uri required"}
	}
	if sid == "" {
		return nil, &rpcErr{errInvalidResourceArgs, "Mcp-Session-Id required"}
	}
	uri := canonicalResource(p.URI, agent)
	if method == "resources/unsubscribe" {
		s.Push.Unsubscribe(sid, uri)
		return map[string]any{}, nil
	}
	if err := s.Push.Subscribe(ctx, sid, agent, uri); err != nil {
		if errors.Is(err, errNoSuchResource) {
			return nil, &rpcErr{errNoResource, "resource not found"}
		}
		return nil, &rpcErr{errInvalidResourceArgs, err.Error()}
	}
	return map[string]any{}, nil
}

func canonicalResource(uri, agent string) string {
	if uri == resourceAgentThread+"me" {
		return resourceAgentThread + agent
	}
	return uri
}
