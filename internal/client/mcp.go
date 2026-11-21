package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/mcp"
)

const ProtocolVersion = mcp.ProtocolVersion

type MCP struct {
	cfg    config
	url    string
	token  string
	nextID atomic.Int64

	mu      sync.Mutex
	session string
}

func NewMCP(url, token string, opts ...Option) *MCP {
	return &MCP{cfg: newConfig(opts), url: url, token: token}
}

type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (m *MCP) SessionID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.session
}

func (m *MCP) post(ctx context.Context, body map[string]any, sid string) (*http.Response, error) {
	req, err := newRequest(ctx, http.MethodPost, m.url, m.token, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	return m.cfg.hc.Do(req)
}

func (m *MCP) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := m.rpcOnce(ctx, method, params)
	if StatusOf(err) == http.StatusNotFound && method != "initialize" && m.SessionID() != "" {
		if ierr := m.Initialize(ctx); ierr != nil {
			return nil, ierr
		}
		return m.rpcOnce(ctx, method, params)
	}
	return raw, err
}

func (m *MCP) rpcOnce(ctx context.Context, method string, params any) (json.RawMessage, error) {
	msg := map[string]any{"jsonrpc": "2.0", "id": m.nextID.Add(1), "method": method}
	if params != nil {
		msg["params"] = params
	}
	sid := ""
	if method != "initialize" {
		sid = m.SessionID()
	}
	resp, err := m.post(ctx, msg, sid)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	rb, err := readBody(resp)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, httpError(resp.StatusCode, rb)
	}
	if method == "initialize" {
		if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
			m.mu.Lock()
			m.session = s
			m.mu.Unlock()
		}
	}
	var r rpcResponse
	if err := json.Unmarshal(rb, &r); err != nil {
		return nil, fmt.Errorf("decode JSON-RPC response: %w", err)
	}
	if r.Error != nil {
		return nil, &Error{Code: fmt.Sprintf("rpc%d", r.Error.Code), Message: r.Error.Message}
	}
	return r.Result, nil
}

func (m *MCP) Initialize(ctx context.Context) error {
	start := time.Now()
	_, err := m.rpc(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": m.cfg.clientName, "version": "0.1.0"},
	})
	m.cfg.observe("initialize", start, err)
	if err != nil {
		return err
	}
	return m.notify(ctx, "notifications/initialized")
}

func (m *MCP) notify(ctx context.Context, method string) error {
	resp, err := m.post(ctx, map[string]any{"jsonrpc": "2.0", "method": method}, m.SessionID())
	if err != nil {
		return err
	}
	b, _ := readBody(resp)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return httpError(resp.StatusCode, b)
	}
	return nil
}

func (m *MCP) Close(ctx context.Context) error {
	sid := m.SessionID()
	if sid == "" {
		return nil
	}
	req, err := newRequest(ctx, http.MethodDelete, m.url, m.token, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Mcp-Session-Id", sid)
	resp, err := m.cfg.hc.Do(req)
	if err != nil {
		return err
	}
	b, _ := readBody(resp)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return httpError(resp.StatusCode, b)
	}
	m.mu.Lock()
	m.session = ""
	m.mu.Unlock()
	return nil
}

func (m *MCP) Ping(ctx context.Context) error {
	_, err := m.rpc(ctx, "ping", nil)
	return err
}

func (m *MCP) ListTools(ctx context.Context) ([]ToolInfo, error) {
	start := time.Now()
	raw, err := m.rpc(ctx, "tools/list", nil)
	m.cfg.observe("tools/list", start, err)
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []ToolInfo `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode tools/list: %w", err)
	}
	return out.Tools, nil
}

func (m *MCP) Call(ctx context.Context, tool string, args, out any) error {
	if args == nil {
		args = map[string]any{}
	}
	start := time.Now()
	err := m.call(ctx, tool, args, out)
	m.cfg.observe(tool, start, err)
	return err
}

func (m *MCP) call(ctx context.Context, tool string, args, out any) error {
	raw, err := m.rpc(ctx, "tools/call", map[string]any{"name": tool, "arguments": args})
	if err != nil {
		return err
	}
	var res struct {
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("decode tool result: %w", err)
	}
	if res.IsError {
		var eb struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		_ = json.Unmarshal(res.StructuredContent, &eb)
		if eb.Error.Code == "" {
			eb.Error.Code = CodeToolError
		}
		return &Error{Code: eb.Error.Code, Message: eb.Error.Message}
	}
	if out != nil && len(res.StructuredContent) > 0 {
		if err := json.Unmarshal(res.StructuredContent, out); err != nil {
			return fmt.Errorf("decode %s result: %w", tool, err)
		}
	}
	return nil
}
