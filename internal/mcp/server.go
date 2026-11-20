package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/audit"
	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/model"
)

const ProtocolVersion = "2025-06-18"

const (
	maxBody        = 1 << 20
	sessionTTL     = 24 * time.Hour
	maxSessionsPer = 8
)

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

type Hooks interface {
	PauseAgent(ctx context.Context, id, reason string) error
	Touch(agentID string)
}

type session struct {
	agent   string
	created time.Time
}

type Server struct {
	Auth       auth.AgentLookup
	Registry   *Registry
	Guard      audit.LoopGuard
	Hooks      Hooks
	OwnerToken string
	Now        func() time.Time

	mu       sync.Mutex
	sessions map[string]session
	byAgent  map[string][]string
}

func NewServer(a auth.AgentLookup, r *Registry) *Server {
	return &Server{Auth: a, Registry: r, Now: time.Now, sessions: map[string]session{}, byAgent: map[string][]string{}}
}

func (s *Server) newSession(agent string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	list := s.byAgent[agent]
	live := list[:0]
	for _, sid := range list {
		if ss, ok := s.sessions[sid]; ok && now.Sub(ss.created) < sessionTTL {
			live = append(live, sid)
		} else {
			delete(s.sessions, sid)
		}
	}
	for len(live) >= maxSessionsPer {
		delete(s.sessions, live[0])
		live = live[1:]
	}
	s.sessions[id] = session{agent: agent, created: now}
	s.byAgent[agent] = append(live, id)
	return id
}

func (s *Server) sessionOK(id, agent string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[id]
	return ok && ss.agent == agent && s.Now().Sub(ss.created) < sessionTTL
}

func (s *Server) dropSession(id, agent string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[id]
	if !ok || ss.agent != agent {
		return false
	}
	delete(s.sessions, id)
	return true
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tok, ok := bearer(r)
	if !ok {
		unauthorized(w)
		return
	}
	if s.OwnerToken != "" && auth.OwnerMatch(tok, s.OwnerToken) {
		http.Error(w, "the owner token is not valid on /mcp", http.StatusForbidden)
		return
	}
	agent, err := s.Auth.LookupToken(tok)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if agent == nil || agent.Status == model.AgentRevoked {
		unauthorized(w)
		return
	}
	sid := r.Header.Get("Mcp-Session-Id")
	if r.Method == http.MethodDelete {
		if sid == "" || !s.dropSession(sid, agent.ID) {
			http.Error(w, "unknown session", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	var req rpcReq
	if err := json.Unmarshal(body, &req); err != nil || req.JSONRPC != "2.0" {
		writeJSON(w, rpcResp{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcErr{-32700, "parse error"}})
		return
	}
	if sid != "" && req.Method != "initialize" && !s.sessionOK(sid, agent.ID) {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	if len(req.ID) == 0 || req.Method == "" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	actor := model.Actor{ID: agent.ID, Role: agent.Role}
	resp := rpcResp{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", s.newSession(agent.ID))
		resp.Result = map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "subspace", "version": "0.1.0"},
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		tools := []map[string]any{}
		for _, t := range s.Registry.List(actor.Role) {
			tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.Schema})
		}
		resp.Result = map[string]any{"tools": tools}
	case "tools/call":
		resp.Result, resp.Error = s.call(r.Context(), agent, req.Params)
	default:
		resp.Error = &rpcErr{-32601, "method not found"}
	}
	writeJSON(w, resp)
}

func (s *Server) call(ctx context.Context, agent *model.Agent, params json.RawMessage) (any, *rpcErr) {
	a := model.Actor{ID: agent.ID, Role: agent.Role}
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
		return nil, &rpcErr{-32602, "invalid params"}
	}
	t, ok := s.Registry.Get(p.Name, a.Role)
	if !ok {
		return nil, &rpcErr{-32601, "tool not available for role"}
	}
	if s.Hooks != nil {
		s.Hooks.Touch(a.ID)
	}
	if agent.Status == model.AgentPaused && !t.ReadOnly {
		why := "agent is paused"
		if agent.PauseReason != nil {
			why += ": " + *agent.PauseReason
		}
		return toolResult(errBody("agent_paused", why+"; ask the owner to resume you"), true), nil
	}
	if !t.ReadOnly && s.Guard != nil {
		if s.Guard.Hit(a.ID, t.Name, audit.ArgsHash(p.Arguments), s.Now()) {
			if s.Hooks != nil {
				_ = s.Hooks.PauseAgent(ctx, a.ID, "loop: same call repeated too often")
			}
			return toolResult(errBody("agent_paused", "loop protection: identical "+t.Name+" calls repeated too often; you are paused"), true), nil
		}
	}
	res, err := t.Handler(ctx, &Call{Actor: a, Args: p.Arguments})
	if err != nil {
		e := model.AsError(err)
		return toolResult(errBody(e.Code, e.Message), true), nil
	}
	return toolResult(res, false), nil
}

func errBody(code, msg string) map[string]any {
	return map[string]any{"error": map[string]any{"code": code, "message": msg}}
}

func toolResult(v any, isErr bool) map[string]any {
	b, err := json.Marshal(v)
	var sc any = json.RawMessage(b)
	if err != nil {
		sc = v
	}
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(b)}},
		"structuredContent": sc,
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
