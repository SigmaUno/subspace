package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

func (s *Server) capabilities() map[string]any {
	caps := map[string]any{"tools": map[string]any{"listChanged": s.Push != nil}}
	if s.Push != nil {
		caps["resources"] = map[string]any{"subscribe": true, "listChanged": false}
	}
	return caps
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
	if !s.sessionOK(sid, agent) {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	s.Push.Serve(w, r, sid, agent)
}

var resourceTemplates = []map[string]any{
	{"uriTemplate": resourceTask + "{id}", "name": "task", "description": "A task you are assigned to or review.", "mimeType": "application/json"},
	{"uriTemplate": "subspace://thread/{kind}/{id}", "name": "thread", "description": "Your agent thread (kind agent) or the thread of a task of yours (kind task).", "mimeType": "application/json"},
}

func (s *Server) resources(ctx context.Context, method, sid, agent string, params json.RawMessage) (any, *rpcErr) {
	switch method {
	case "resources/list":
		return map[string]any{"resources": []any{}}, nil
	case "resources/templates/list":
		return map[string]any{"resourceTemplates": resourceTemplates}, nil
	}
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.URI == "" {
		return nil, &rpcErr{errInvalidResourceArgs, "invalid params: uri required"}
	}
	if sid == "" {
		return nil, &rpcErr{errInvalidResourceArgs, "Mcp-Session-Id required"}
	}
	if method == "resources/unsubscribe" {
		s.Push.Unsubscribe(sid, p.URI)
		return map[string]any{}, nil
	}
	if err := s.Push.Subscribe(ctx, sid, agent, p.URI); err != nil {
		if errors.Is(err, errNoSuchResource) {
			return nil, &rpcErr{errNoResource, "resource not found"}
		}
		return nil, &rpcErr{errInvalidResourceArgs, err.Error()}
	}
	return map[string]any{}, nil
}
