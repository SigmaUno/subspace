package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
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
	sessionSweep   = time.Hour
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
	swept    time.Time
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
	if now.Sub(s.swept) >= sessionSweep {
		s.swept = now
		for a := range s.byAgent {
			s.pruneLocked(a, now)
		}
	}
	live := s.pruneLocked(agent, now)
	for len(live) >= maxSessionsPer {
		delete(s.sessions, live[0])
		live = live[1:]
	}
	s.sessions[id] = session{agent: agent, created: now}
	s.byAgent[agent] = append(live, id)
	return id
}

func (s *Server) pruneLocked(agent string, now time.Time) []string {
	list := s.byAgent[agent]
	live := list[:0]
	for _, sid := range list {
		if ss, ok := s.sessions[sid]; ok && now.Sub(ss.created) < sessionTTL {
			live = append(live, sid)
		} else {
			delete(s.sessions, sid)
		}
	}
	if len(live) == 0 {
		delete(s.byAgent, agent)
		return nil
	}
	s.byAgent[agent] = live
	return live
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
	s.byAgent[agent] = slices.DeleteFunc(s.byAgent[agent], func(sid string) bool { return sid == id })
	if len(s.byAgent[agent]) == 0 {
		delete(s.byAgent, agent)
	}
	return true
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func tokenExpired(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token", error_description="token expired"`)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "token_expired", "message": "agent token expired"}})
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
	if errors.Is(err, auth.ErrTokenExpired) {
		tokenExpired(w)
		return
	}
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
	if !validID(req.ID) {
		writeJSON(w, rpcResp{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcErr{-32600, "invalid request: id must be a string or a number"}})
		return
	}
	actor := model.Actor{ID: agent.ID, Role: agent.Role, Kind: agent.Kind}
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
	a := model.Actor{ID: agent.ID, Role: agent.Role, Kind: agent.Kind}
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
		return toolResult(errBody("agent_paused", why+"; ask the owner to resume you", t.Name), true), nil
	}
	if !t.ReadOnly && s.Guard != nil {
		if s.Guard.Hit(a.ID, t.Name, audit.ArgsHash(schemaArgs(t.Schema, p.Arguments)), s.Now()) {
			if s.Hooks != nil {
				_ = s.Hooks.PauseAgent(ctx, a.ID, "loop: same call repeated too often")
			}
			s.Registry.fail(a, t.Name, "agent_paused", "loop protection", p.Arguments)
			return toolResult(errBody("agent_paused", "loop protection: identical "+t.Name+" calls repeated too often; you are paused", t.Name), true), nil
		}
	}
	res, err := t.Handler(ctx, &Call{Actor: a, Args: p.Arguments})
	if err != nil {
		e := model.AsError(err)
		if e.Code == "internal" {
			slog.Error("mcp tool failed", "tool", t.Name, "agent", a.ID, "err", err)
		}
		s.Registry.fail(a, t.Name, e.Code, e.Message, p.Arguments)
		body := errBody(e.Code, e.Message, t.Name)
		if e.Details != nil {
			body["error"].(map[string]any)["details"] = e.Details
		}
		return toolResult(body, true), nil
	}
	return toolResult(res, false), nil
}

func schemaArgs(schema map[string]any, raw json.RawMessage) json.RawMessage {
	props, _ := schema["properties"].(map[string]any)
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return raw
	}
	out := map[string]json.RawMessage{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return raw
		}
		key, _ := tok.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return raw
		}
		if _, ok := props[key]; ok {
			out[key] = v
			continue
		}
		for p := range props {
			if strings.EqualFold(p, key) {
				out[p] = v
				break
			}
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return raw
	}
	return b
}

func validID(id json.RawMessage) bool {
	c := id[0]
	return c == '"' || c == '-' || (c >= '0' && c <= '9')
}

func errBody(code, msg, tool string) map[string]any {
	e := map[string]any{"code": code, "message": msg}
	if h := Hint(code, tool); h != "" {
		e["hint"] = h
	}
	return map[string]any{"error": e}
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
