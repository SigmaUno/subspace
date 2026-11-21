package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxResponseBytes = 8 << 20

type Message struct {
	Role    string
	Content string
}

type Request struct {
	System      string
	Messages    []Message
	MaxTokens   int
	Temperature float64
	JSON        bool
}

type Usage struct {
	InputTokens  int64
	OutputTokens int64
	CacheRead    int64
	CacheWrite   int64
}

type Response struct {
	ID         string
	Model      string
	Text       string
	StopReason string
	Usage      Usage
}

type Provider interface {
	Name() string
	Model() string
	Complete(ctx context.Context, req Request) (*Response, error)
}

type APIError struct {
	Status     int
	Message    string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("llm: status %d: %s", e.Status, e.Message)
}

func IsRateLimited(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusTooManyRequests
}

type Config struct {
	Provider   string
	Model      string
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
	Timeout    time.Duration
	Retries    int
	Backoff    time.Duration
	Sleep      func(context.Context, time.Duration) error
}

func New(cfg Config) (Provider, error) {
	if cfg.Model == "" {
		return nil, errors.New("llm: model is required")
	}
	if cfg.APIKey == "" {
		return nil, errors.New("llm: api key is required")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 2 * time.Minute
	}
	if cfg.Retries < 0 {
		cfg.Retries = 0
	}
	if cfg.Backoff <= 0 {
		cfg.Backoff = time.Second
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleep
	}
	t := transport{cfg: cfg}
	switch cfg.Provider {
	case "anthropic":
		return &anthropic{t: t, base: baseURL(cfg.BaseURL, "https://api.anthropic.com")}, nil
	case "openai":
		return &openAI{t: t, base: baseURL(cfg.BaseURL, "https://api.openai.com/v1")}, nil
	case "deepseek":
		return &openAI{t: t, base: baseURL(cfg.BaseURL, "https://api.deepseek.com")}, nil
	default:
		return nil, fmt.Errorf("llm: unknown provider %q (anthropic, openai, deepseek)", cfg.Provider)
	}
}

func baseURL(v, def string) string {
	if v == "" {
		v = def
	}
	return strings.TrimRight(v, "/")
}

type transport struct {
	cfg Config
}

func (t transport) post(ctx context.Context, url string, headers map[string]string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	var last error
	for attempt := 0; ; attempt++ {
		wait, err := t.once(ctx, url, headers, body, out)
		if err == nil {
			return nil
		}
		last = err
		if wait < 0 || attempt >= t.cfg.Retries || ctx.Err() != nil {
			return last
		}
		if wait == 0 {
			wait = t.cfg.Backoff << attempt
		}
		if err := t.cfg.Sleep(ctx, wait); err != nil {
			return errors.Join(last, err)
		}
	}
}

func (t transport) once(ctx context.Context, url string, headers map[string]string, body []byte, out any) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, t.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return -1, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := t.cfg.HTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		ae := &APIError{Status: resp.StatusCode, Message: errorMessage(raw), RetryAfter: retryAfter(resp.Header.Get("Retry-After"))}
		if retryableStatus(resp.StatusCode) {
			return ae.RetryAfter, ae
		}
		return -1, ae
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return -1, fmt.Errorf("llm: decode response: %w", err)
	}
	return 0, nil
}

func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code == http.StatusRequestTimeout || code >= 500
}

func retryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
		return min(time.Duration(n)*time.Second, time.Minute)
	}
	if at, err := http.ParseTime(v); err == nil {
		return min(max(time.Until(at), 0), time.Minute)
	}
	return 0
}

func errorMessage(raw []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type Func struct {
	ProviderName string
	ModelName    string
	Respond      func(ctx context.Context, req Request) (*Response, error)
}

func (f *Func) Name() string  { return f.ProviderName }
func (f *Func) Model() string { return f.ModelName }
func (f *Func) Complete(ctx context.Context, req Request) (*Response, error) {
	return f.Respond(ctx, req)
}
