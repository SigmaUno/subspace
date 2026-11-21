package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxRespBody = 8 << 20

type Observer func(op string, d time.Duration, err error)

type config struct {
	hc         *http.Client
	obs        Observer
	clientName string
	retries    int
	sleep      func(context.Context, time.Duration) error
	newKey     func() string
}

type Option func(*config)

func WithHTTPClient(hc *http.Client) Option { return func(c *config) { c.hc = hc } }

func WithObserver(o Observer) Option { return func(c *config) { c.obs = o } }

func WithClientName(name string) Option { return func(c *config) { c.clientName = name } }

func WithRetries(n int) Option { return func(c *config) { c.retries = max(n, 0) } }

func WithSleep(f func(context.Context, time.Duration) error) Option {
	return func(c *config) { c.sleep = f }
}

func WithIdempotencyKeys(f func() string) Option { return func(c *config) { c.newKey = f } }

func newConfig(opts []Option) config {
	c := config{clientName: "subspace-client", retries: 3, sleep: sleepCtx, newKey: randomKey}
	for _, o := range opts {
		o(&c)
	}
	if c.hc == nil {
		c.hc = &http.Client{Timeout: 60 * time.Second}
	}
	return c
}

func (c *config) observe(op string, start time.Time, err error) {
	if c.obs != nil {
		c.obs(op, time.Since(start), err)
	}
}

func readBody(resp *http.Response) ([]byte, error) {
	return io.ReadAll(io.LimitReader(resp.Body, maxRespBody))
}

func httpError(status int, body []byte) *Error {
	var eb struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	_ = json.Unmarshal(body, &eb)
	e := &Error{Status: status, Code: eb.Error.Code, Message: eb.Error.Message}
	if e.Code == "" {
		e.Code = statusCode(status)
		e.Message = strings.TrimSpace(string(body))
		if len(e.Message) > 200 {
			e.Message = e.Message[:200]
		}
	}
	return e
}

func newRequest(ctx context.Context, method, url, token string, in any) (*http.Request, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func decodeInto(body []byte, out any, what string) error {
	if out == nil || len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", what, err)
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func randomKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
