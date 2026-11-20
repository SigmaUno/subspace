package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/model"
)

// ProtocolVersion is the MCP protocol version we speak.
const ProtocolVersion = "2025-06-18"

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Server is the /mcp HTTP handler.
type Server struct {
	Auth     auth.AgentLookup
	Registry *Registry
}

// NewServer builds a transport server.
func NewServer(a auth.AgentLookup, r *Registry) *Server { return &Server{Auth: a, Registry: r} }

// ServeHTTP implements http.Handler.
// TODO(lead): sessions (Mcp-Session-Id), loop guard, audit, paused-agent handling, owner-token 403.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tok, ok := bearer(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	agent, err := s.Auth.LookupToken(tok)
	if err != nil || agent == nil {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var req rpcReq
	if err := json.Unmarshal(body, &req); err != nil || req.JSONRPC != "2.0" {
		writeJSON(w, rpcResp{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcErr{-32700, "parse error"}})
		return
	}
	if len(req.ID) == 0 { // notification
		w.WriteHeader(http.StatusAccepted)
		return
	}
	actor := model.Actor{ID: agent.ID, Role: agent.Role}
	resp := rpcResp{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "subspace", "version": "0.1.0"},
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		var tools []map[string]any
		for _, t := range s.Registry.List(actor.Role) {
			tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.Schema})
		}
		resp.Result = map[string]any{"tools": tools}
	case "tools/call":
		resp.Result, resp.Error = s.call(r.Context(), actor, req.Params)
	default:
		resp.Error = &rpcErr{-32601, "method not found"}
	}
	writeJSON(w, resp)
}

func (s *Server) call(ctx context.Context, a model.Actor, params json.RawMessage) (any, *rpcErr) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcErr{-32602, "invalid params"}
	}
	t, ok := s.Registry.Get(p.Name, a.Role)
	if !ok {
		return nil, &rpcErr{-32601, "tool not available for role"}
	}
	res, err := t.Handler(ctx, &Call{Actor: a, Args: p.Arguments})
	if err != nil {
		e := model.AsError(err)
		msg := e.Message
		if e.Code == "internal" {
			msg = err.Error()
		}
		return toolResult(map[string]any{"error": map[string]any{"code": e.Code, "message": msg}}, true), nil
	}
	return toolResult(res, false), nil
}

func toolResult(v any, isErr bool) map[string]any {
	b, _ := json.Marshal(v)
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(b)}},
		"structuredContent": v,
		"isError":           isErr,
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func bearer(r *http.Request) (string, bool) {
	const p = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(p) && h[:len(p)] == p {
		return h[len(p):], true
	}
	return "", false
}
