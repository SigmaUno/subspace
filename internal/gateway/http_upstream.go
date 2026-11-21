package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/SigmaUno/subspace/internal/netguard"
)

type httpUpstream struct {
	url      string
	secret   string
	client   *http.Client
	maxBytes int64

	next    atomic.Int64
	initMu  sync.Mutex
	ready   bool
	mu      sync.Mutex
	session string
}

func newHTTPUpstream(g Gateway, secret string, guard *netguard.Guard) *httpUpstream {
	if g.AllowPrivateHosts {
		guard = &netguard.Guard{AllowAll: true, Resolver: guard.Resolver, Dial: guard.Dial, DialTimeout: guard.DialTimeout}
	}
	return &httpUpstream{url: g.URL, secret: secret, client: guard.Client(g.Timeout()), maxBytes: int64(g.MaxResultBytes) + 64<<10}
}

func (h *httpUpstream) close() {}

func (h *httpUpstream) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := h.init(ctx); err != nil {
		return nil, err
	}
	res, err := h.request(ctx, method, params, false)
	if errors.Is(err, errSessionGone) {
		h.initMu.Lock()
		h.ready = false
		h.initMu.Unlock()
		h.setSession("")
		if err := h.init(ctx); err != nil {
			return nil, err
		}
		res, err = h.request(ctx, method, params, false)
	}
	return res, err
}

var errSessionGone = errors.New("upstream session expired")

func (h *httpUpstream) init(ctx context.Context) error {
	h.initMu.Lock()
	defer h.initMu.Unlock()
	if h.ready {
		return nil
	}
	if _, err := h.request(ctx, "initialize", initParams(), false); err != nil {
		return err
	}
	if _, err := h.request(ctx, "notifications/initialized", nil, true); err != nil {
		return err
	}
	h.ready = true
	return nil
}

func (h *httpUpstream) sessionID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.session
}

func (h *httpUpstream) setSession(s string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.session = s
}

func (h *httpUpstream) request(ctx context.Context, method string, params any, notify bool) (json.RawMessage, error) {
	msg := rpcRequest{JSONRPC: "2.0", Method: method, Params: params}
	id := h.next.Add(1)
	if !notify {
		msg.ID = &id
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		return nil, errUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if method != "initialize" {
		req.Header.Set("MCP-Protocol-Version", protocolVersion)
	}
	sid := ""
	if method != "initialize" {
		sid = h.sessionID()
	}
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	if h.secret != "" {
		req.Header.Set("Authorization", "Bearer "+h.secret)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.Join(errUnavailable, netguard.Scrub(err))
	}
	defer func() {
		netguard.Drain(resp.Body, 64<<10)
		_ = resp.Body.Close()
	}()
	if method == "initialize" {
		if s := resp.Header.Get("Mcp-Session-Id"); len(s) <= 256 {
			h.setSession(s)
		}
	}
	switch {
	case resp.StatusCode == http.StatusNotFound && sid != "":
		return nil, errSessionGone
	case notify && resp.StatusCode/100 == 2:
		return nil, nil
	case resp.StatusCode/100 != 2:
		return nil, errors.Join(errUnavailable, errors.New("upstream answered HTTP "+resp.Status))
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if ct == "text/event-stream" {
		return h.readStream(resp.Body, id)
	}
	raw, err := netguard.ReadBody(resp.Body, h.maxBytes+1)
	if err != nil {
		return nil, errors.Join(errUnavailable, err)
	}
	if int64(len(raw)) > h.maxBytes {
		return nil, errTooLarge
	}
	var m rpcMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, errProtocol
	}
	if got, ok := idOf(m.ID); !ok || got != id {
		return nil, errProtocol
	}
	return m.outcome()
}

func (h *httpUpstream) readStream(body io.Reader, id int64) (json.RawMessage, error) {
	r := bufio.NewReaderSize(io.LimitReader(body, h.maxBytes*4), 64<<10)
	var data strings.Builder
	for {
		line, err := readLine(r, h.maxBytes)
		if errors.Is(err, errTooLarge) {
			return nil, err
		}
		if err != nil && line == "" {
			return nil, errors.Join(errUnavailable, errors.New("upstream stream ended without a response"))
		}
		switch {
		case strings.HasPrefix(line, "data:"):
			if int64(data.Len()+len(line)) > h.maxBytes {
				return nil, errTooLarge
			}
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		case line == "":
			if data.Len() > 0 {
				var m rpcMessage
				if json.Unmarshal([]byte(data.String()), &m) == nil {
					if got, ok := idOf(m.ID); ok && got == id && m.Method == "" {
						return m.outcome()
					}
				}
				data.Reset()
			}
		}
		if err != nil {
			return nil, errors.Join(errUnavailable, errors.New("upstream stream ended without a response"))
		}
	}
}

func readLine(r *bufio.Reader, limit int64) (string, error) {
	var b []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		b = append(b, chunk...)
		if int64(len(b)) > limit {
			return "", errTooLarge
		}
		if err != nil {
			return string(b), err
		}
		if !isPrefix {
			return string(b), nil
		}
	}
}
