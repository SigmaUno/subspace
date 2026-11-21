package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

const protocolVersion = "2025-06-18"

var (
	errUnavailable = errors.New("upstream unavailable")
	errTooLarge    = errors.New("upstream response too large")
	errResultShape = errors.New("upstream result is not a valid tool result")
	errProtocol    = errors.New("upstream spoke invalid JSON-RPC")
)

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("upstream error %d: %s", e.Code, e.Message) }

type upstream interface {
	call(ctx context.Context, method string, params any) (json.RawMessage, error)
	close()
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

func initParams() map[string]any {
	return map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "subspace-gateway", "version": "1"},
	}
}

func (m *rpcMessage) outcome() (json.RawMessage, error) {
	if m.Error != nil {
		return nil, m.Error
	}
	if len(m.Result) == 0 {
		return nil, errProtocol
	}
	return m.Result, nil
}

func idOf(raw json.RawMessage) (int64, bool) {
	var id int64
	if len(raw) == 0 || json.Unmarshal(raw, &id) != nil {
		return 0, false
	}
	return id, true
}
