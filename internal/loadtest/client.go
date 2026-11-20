package loadtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProtocolVersion is the MCP protocol version the simulated agents speak.
const ProtocolVersion = "2025-06-18"

const maxRespBody = 8 << 20

// APIError is a REST error response (docs/api.md §1).
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return fmt.Sprintf("HTTP %d %s: %s", e.Status, e.Code, e.Message)
}

// ToolError is an MCP tool result with isError:true, or a JSON-RPC error.
type ToolError struct {
	Code    string
	Message string
}

func (e *ToolError) Error() string { return e.Code + ": " + e.Message }

func newHTTPClient(conns int) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConns = conns
	tr.MaxIdleConnsPerHost = conns
	tr.MaxConnsPerHost = 0
	tr.IdleConnTimeout = 90 * time.Second
	return &http.Client{Transport: tr, Timeout: 60 * time.Second}
}

// ownerClient calls the owner REST API.
type ownerClient struct {
	hc    *http.Client
	base  string
	token string
	rec   *Recorder
}

// do sends a JSON request and decodes the JSON response into out (if non-nil). op names the stats bucket.
func (c *ownerClient) do(ctx context.Context, op, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	start := time.Now()
	err := c.send(ctx, method, path, body, out)
	if c.rec != nil {
		c.rec.Observe(op, time.Since(start), err)
	}
	return err
}

func (c *ownerClient) send(ctx context.Context, method, path string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBody))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var eb struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		_ = json.Unmarshal(b, &eb)
		return &APIError{Status: resp.StatusCode, Code: eb.Error.Code, Message: eb.Error.Message}
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("decode %s %s: %w", method, path, err)
		}
	}
	return nil
}

// mcpClient is one simulated agent speaking Streamable HTTP MCP (one JSON-RPC message per POST).
type mcpClient struct {
	hc      *http.Client
	url     string
	agent   string
	token   string
	rec     *Recorder
	nextID  atomic.Int64
	mu      sync.Mutex
	session string
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *mcpClient) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	msg := map[string]any{"jsonrpc": "2.0", "id": c.nextID.Add(1), "method": method}
	if params != nil {
		msg["params"] = params
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	c.mu.Lock()
	sid := c.session
	c.mu.Unlock()
	if sid != "" && method != "initialize" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	rb, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(rb))}
	}
	if method == "initialize" {
		if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
			c.mu.Lock()
			c.session = s
			c.mu.Unlock()
		}
	}
	var r rpcResponse
	if err := json.Unmarshal(rb, &r); err != nil {
		return nil, fmt.Errorf("decode JSON-RPC response: %w", err)
	}
	if r.Error != nil {
		return nil, &ToolError{Code: fmt.Sprintf("rpc%d", r.Error.Code), Message: r.Error.Message}
	}
	return r.Result, nil
}

func (c *mcpClient) observe(op string, start time.Time, err error) {
	if c.rec != nil {
		c.rec.Observe(op, time.Since(start), err)
	}
}

// Initialize performs the MCP handshake (initialize + notifications/initialized).
func (c *mcpClient) Initialize(ctx context.Context) error {
	start := time.Now()
	_, err := c.rpc(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "subspace-loadtest", "version": "0.1.0"},
	})
	c.observe("initialize", start, err)
	if err != nil {
		return err
	}
	return c.notify(ctx, "notifications/initialized")
}

func (c *mcpClient) notify(ctx context.Context, method string) error {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	c.mu.Lock()
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	c.mu.Unlock()
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxRespBody))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return &APIError{Status: resp.StatusCode}
	}
	return nil
}

// Call invokes a tool and decodes structuredContent into out (if non-nil). Tool errors become *ToolError.
func (c *mcpClient) Call(ctx context.Context, tool string, args, out any) error {
	if args == nil {
		args = map[string]any{}
	}
	start := time.Now()
	err := c.call(ctx, tool, args, out)
	c.observe(tool, start, err)
	return err
}

func (c *mcpClient) call(ctx context.Context, tool string, args, out any) error {
	raw, err := c.rpc(ctx, "tools/call", map[string]any{"name": tool, "arguments": args})
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
			eb.Error.Code = "tool_error"
		}
		return &ToolError{Code: eb.Error.Code, Message: eb.Error.Message}
	}
	if out != nil && len(res.StructuredContent) > 0 {
		if err := json.Unmarshal(res.StructuredContent, out); err != nil {
			return fmt.Errorf("decode %s result: %w", tool, err)
		}
	}
	return nil
}
