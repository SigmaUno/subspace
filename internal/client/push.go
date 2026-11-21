package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

const (
	notificationBuffer = 64
	streamIdleTimeout  = 45 * time.Second
	reconnectMin       = 100 * time.Millisecond
	reconnectMax       = 5 * time.Second
)

var (
	ErrStreamUnsupported = errors.New("server does not offer the notification stream")
	ErrStreamReplaced    = errors.New("notification stream replaced by a newer one on the same session")
)

type Notification struct {
	ID     string
	Method string
	Params json.RawMessage
}

type PushEvent struct {
	Type       string `json:"type"`
	Scope      string `json:"scope"`
	TaskID     string `json:"task_id"`
	Thread     string `json:"thread"`
	QuestionID string `json:"question_id"`
	MessageID  int64  `json:"message_id"`
}

func (n Notification) Event() (PushEvent, bool) {
	var e PushEvent
	if n.Method != "notifications/subspace/event" || json.Unmarshal(n.Params, &e) != nil {
		return e, false
	}
	return e, true
}

func (n Notification) ResourceURI() (string, bool) {
	var p struct {
		URI string `json:"uri"`
	}
	if n.Method != "notifications/resources/updated" || json.Unmarshal(n.Params, &p) != nil {
		return "", false
	}
	return p.URI, true
}

func (n Notification) Resync() bool { return n.Method == "notifications/subspace/resync" }

func (m *MCP) Subscribe(ctx context.Context, uri string) error {
	_, err := m.rpc(ctx, "resources/subscribe", map[string]any{"uri": uri})
	return err
}

func (m *MCP) Unsubscribe(ctx context.Context, uri string) error {
	_, err := m.rpc(ctx, "resources/unsubscribe", map[string]any{"uri": uri})
	return err
}

type idleReader struct {
	r io.Reader
	t *time.Timer
	d time.Duration
}

func (i *idleReader) Read(p []byte) (int, error) {
	n, err := i.r.Read(p)
	if n > 0 {
		i.t.Reset(i.d)
	}
	return n, err
}

func (m *MCP) openStream(ctx context.Context, lastID string) (*http.Response, error) {
	if m.SessionID() == "" {
		if err := m.Initialize(ctx); err != nil {
			return nil, err
		}
	}
	req, err := newRequest(ctx, http.MethodGet, m.url, m.token, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	req.Header.Set("Mcp-Session-Id", m.SessionID())
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	hc := *m.cfg.hc
	hc.Timeout = 0
	resp, err := hc.Do(req)
	if err != nil {
		return nil, &TransportError{Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := readBody(resp)
		_ = resp.Body.Close()
		return nil, httpError(resp.StatusCode, b)
	}
	return resp, nil
}

func (m *MCP) StreamAvailable(ctx context.Context) (bool, error) {
	if m.pumping.Load() {
		err := m.NotificationsErr()
		return err == nil, nil
	}
	resp, err := m.openStream(ctx, "")
	if err != nil {
		if StatusOf(err) == http.StatusMethodNotAllowed {
			return false, nil
		}
		return false, err
	}
	_ = resp.Body.Close()
	return true, nil
}

func (m *MCP) Notifications(ctx context.Context) <-chan Notification {
	m.pushOnce.Do(func() {
		m.pumping.Store(true)
		m.push = make(chan Notification, notificationBuffer)
		go m.pump(ctx)
	})
	return m.push
}

func (m *MCP) NotificationsErr() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pushErr
}

func (m *MCP) stopPump(err error) {
	m.mu.Lock()
	m.pushErr = err
	m.mu.Unlock()
	close(m.push)
}

func (m *MCP) pump(ctx context.Context) {
	lastID := ""
	backoff := reconnectMin
	for {
		if ctx.Err() != nil {
			m.stopPump(ctx.Err())
			return
		}
		resp, err := m.openStream(ctx, lastID)
		switch status := StatusOf(err); {
		case err == nil:
			backoff = reconnectMin
			reason, rerr := m.readStream(ctx, resp, &lastID)
			if rerr != nil && ctx.Err() != nil {
				m.stopPump(ctx.Err())
				return
			}
			if reason == "replaced" {
				m.stopPump(ErrStreamReplaced)
				return
			}
			if reason == "session_ended" {
				m.mu.Lock()
				m.session, lastID = "", ""
				m.mu.Unlock()
			}
		case status == http.StatusMethodNotAllowed || status == http.StatusNotAcceptable:
			m.stopPump(ErrStreamUnsupported)
			return
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			m.stopPump(err)
			return
		case status == http.StatusNotFound:
			m.mu.Lock()
			m.session, lastID = "", ""
			m.mu.Unlock()
		}
		if serr := m.cfg.sleep(ctx, backoff); serr != nil {
			m.stopPump(serr)
			return
		}
		backoff = min(backoff*2, reconnectMax)
	}
}

func (m *MCP) readStream(ctx context.Context, resp *http.Response, lastID *string) (string, error) {
	defer func() { _ = resp.Body.Close() }()
	idle := time.AfterFunc(streamIdleTimeout, func() { _ = resp.Body.Close() })
	defer idle.Stop()
	r := newEventReader(&idleReader{r: resp.Body, t: idle, d: streamIdleTimeout})
	for {
		ev, err := r.next()
		if err != nil {
			return "", err
		}
		switch ev.Name {
		case "close":
			var c struct {
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal([]byte(ev.Data), &c)
			return c.Reason, nil
		case "message":
			var msg struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal([]byte(ev.Data), &msg) != nil || msg.Method == "" {
				continue
			}
			select {
			case m.push <- Notification{ID: ev.ID, Method: msg.Method, Params: msg.Params}:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			if ev.ID != "" {
				*lastID = ev.ID
			}
		}
	}
}
