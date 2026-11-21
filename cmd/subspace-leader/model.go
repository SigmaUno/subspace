package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const anthropicVersion = "2023-06-01"

var errBudget = errors.New("token budget exhausted")

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Request struct {
	System    string
	Messages  []Message
	MaxTokens int
}

type Usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

type Reply struct {
	ID         string
	Model      string
	Text       string
	StopReason string
	Usage      Usage
}

type Model interface {
	Complete(ctx context.Context, req Request) (*Reply, error)
}

type APIError struct {
	Status  int
	Type    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("anthropic: HTTP %d %s: %s", e.Status, e.Type, e.Message)
}

type Anthropic struct {
	HC          *http.Client
	URL         string
	Key         string
	Model       string
	Attempts    int
	Timeout     time.Duration
	Backoff     time.Duration
	MaxBackoff  time.Duration
	OnRateLimit func(ctx context.Context, requestID string)
}

func retryable(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable,
		http.StatusGatewayTimeout, 529:
		return true
	}
	return false
}

func (a *Anthropic) Complete(ctx context.Context, req Request) (*Reply, error) {
	body, err := json.Marshal(map[string]any{
		"model": a.Model, "max_tokens": req.MaxTokens, "system": req.System, "messages": req.Messages,
	})
	if err != nil {
		return nil, err
	}
	attempts := max(a.Attempts, 1)
	var last error
	for i := 0; i < attempts; i++ {
		rep, wait, err := a.once(ctx, body)
		if err == nil {
			return rep, nil
		}
		last = err
		var ae *APIError
		if errors.As(err, &ae) && !retryable(ae.Status) {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if i == attempts-1 {
			break
		}
		if wait <= 0 {
			wait = a.Backoff << i
		}
		if a.MaxBackoff > 0 {
			wait = min(wait, a.MaxBackoff)
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
	return nil, fmt.Errorf("anthropic: giving up after %d attempts: %w", attempts, last)
}

func (a *Anthropic) once(ctx context.Context, body []byte) (*Reply, time.Duration, error) {
	if a.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.Timeout)
		defer cancel()
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.URL, "/")+"/v1/messages", bytes.NewReader(body)) //nolint:gosec // API base URL is set by the operator
	if err != nil {
		return nil, 0, err
	}
	hr.Header.Set("content-type", "application/json")
	hr.Header.Set("x-api-key", a.Key)
	hr.Header.Set("anthropic-version", anthropicVersion)
	resp, err := a.HC.Do(hr) //nolint:gosec // API base URL is set by the operator
	if err != nil {
		return nil, 0, fmt.Errorf("anthropic: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, 0, fmt.Errorf("anthropic: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var eb struct {
			Error struct{ Type, Message string } `json:"error"`
		}
		_ = json.Unmarshal(raw, &eb)
		if resp.StatusCode == http.StatusTooManyRequests && a.OnRateLimit != nil {
			a.OnRateLimit(ctx, resp.Header.Get("request-id"))
		}
		return nil, retryAfter(resp.Header.Get("retry-after")), &APIError{Status: resp.StatusCode, Type: eb.Error.Type, Message: eb.Error.Message}
	}
	var out struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      Usage  `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, 0, fmt.Errorf("anthropic: decode response: %w", err)
	}
	var sb strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return &Reply{ID: out.ID, Model: out.Model, Text: sb.String(), StopReason: out.StopReason, Usage: out.Usage}, 0, nil
}

func retryAfter(v string) time.Duration {
	if s, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && s >= 0 {
		return time.Duration(s * float64(time.Second))
	}
	return 0
}

type fakeEntry struct {
	Text       string          `json:"text"`
	JSON       json.RawMessage `json:"json"`
	StopReason string          `json:"stop_reason"`
	Usage      Usage           `json:"usage"`
}

type Fake struct {
	model   string
	mu      sync.Mutex
	entries []fakeEntry
	next    int
}

func loadFake(path, mdl string) (*Fake, error) {
	b, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	if err != nil {
		return nil, fmt.Errorf("fake model: %w", err)
	}
	var f struct {
		Responses []fakeEntry `json:"responses"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("fake model %s: %w", path, err)
	}
	if len(f.Responses) == 0 {
		return nil, fmt.Errorf("fake model %s: no responses", path)
	}
	return &Fake{model: mdl, entries: f.Responses}, nil
}

func (f *Fake) Complete(_ context.Context, _ Request) (*Reply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.next >= len(f.entries) {
		return nil, errors.New("fake model: no more canned responses")
	}
	e := f.entries[f.next]
	f.next++
	text := e.Text
	if len(e.JSON) > 0 {
		text = string(e.JSON)
	}
	stop := e.StopReason
	if stop == "" {
		stop = "end_turn"
	}
	return &Reply{ID: fmt.Sprintf("fake-%d", f.next), Model: f.model, Text: text, StopReason: stop, Usage: e.Usage}, nil
}

type Ledger struct {
	Requests    int64 `json:"requests"`
	RateLimited int64 `json:"rate_limited"`
	TokensIn    int64 `json:"tokens_in"`
	TokensOut   int64 `json:"tokens_out"`
	CacheRead   int64 `json:"cache_read"`
	CacheWrite  int64 `json:"cache_write"`
}

func (l Ledger) total() int64 { return l.TokensIn + l.TokensOut + l.CacheRead + l.CacheWrite }

type Metered struct {
	M      Model
	Budget int64
	Report func(ctx context.Context, r *Reply)

	mu sync.Mutex
	l  Ledger
}

func (m *Metered) Complete(ctx context.Context, req Request) (*Reply, error) {
	m.mu.Lock()
	spent := m.l.total()
	m.mu.Unlock()
	if m.Budget > 0 && spent >= m.Budget {
		return nil, fmt.Errorf("%w (%d of %d tokens used)", errBudget, spent, m.Budget)
	}
	r, err := m.M.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.l.Requests++
	m.l.TokensIn += r.Usage.InputTokens
	m.l.TokensOut += r.Usage.OutputTokens
	m.l.CacheRead += r.Usage.CacheReadInputTokens
	m.l.CacheWrite += r.Usage.CacheCreationInputTokens
	m.mu.Unlock()
	if m.Report != nil {
		m.Report(ctx, r)
	}
	return r, nil
}

func (m *Metered) rateLimited() {
	m.mu.Lock()
	m.l.RateLimited++
	m.mu.Unlock()
}

func (m *Metered) Ledger() Ledger {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.l
}
