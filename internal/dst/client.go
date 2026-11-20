package dst

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type client struct {
	base  string
	owner string
	hc    *http.Client
}

type reply struct {
	HTTP int
	RPC  string
	Code string
	Body json.RawMessage
}

func (r reply) ok() bool { return r.HTTP == http.StatusOK && r.RPC == "" && r.Code == "" }

func (r reply) outcome() string {
	switch {
	case r.HTTP != http.StatusOK:
		return fmt.Sprintf("http %d", r.HTTP)
	case r.RPC != "":
		return "rpc error: " + r.RPC
	case r.Code != "":
		return r.Code
	}
	return "ok"
}

func (c *client) do(ctx context.Context, method, path, token string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	return resp.StatusCode, data, err
}

func (c *client) rest(ctx context.Context, method, path string, body, out any) (int, string, error) {
	var raw []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, "", err
		}
		raw = b
	}
	code, data, err := c.do(ctx, method, path, c.owner, raw)
	if err != nil {
		return 0, "", fmt.Errorf("%s %s: %w", method, path, err)
	}
	if code >= 300 {
		var e struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		return code, e.Error.Code, nil
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return code, "", fmt.Errorf("%s %s: decode: %w", method, path, err)
		}
	}
	return code, "", nil
}

func (c *client) mcp(ctx context.Context, token, tool string, args json.RawMessage) (reply, error) {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args}})
	if err != nil {
		return reply{}, err
	}
	code, data, err := c.do(ctx, http.MethodPost, "/mcp", token, body)
	if err != nil {
		return reply{}, fmt.Errorf("mcp %s: %w", tool, err)
	}
	r := reply{HTTP: code}
	if code != http.StatusOK {
		return r, nil
	}
	var out struct {
		Result *struct {
			StructuredContent json.RawMessage `json:"structuredContent"`
			IsError           bool            `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return r, fmt.Errorf("mcp %s: decode: %w", tool, err)
	}
	switch {
	case out.Error != nil:
		r.RPC = out.Error.Message
	case out.Result == nil:
		r.RPC = "empty result"
	case out.Result.IsError:
		var e struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(out.Result.StructuredContent, &e)
		r.Code = e.Error.Code
		if r.Code == "" {
			r.Code = "unknown"
		}
	default:
		r.Body = out.Result.StructuredContent
	}
	return r, nil
}
