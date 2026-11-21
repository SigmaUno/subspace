package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const SignatureHeader = "X-Subspace-Signature"

type Sink interface {
	Name() string
	Send(ctx context.Context, n Notification) error
}

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

type httpSink struct {
	name   string
	url    string
	client *http.Client
	build  func(Notification) ([]byte, http.Header, error)
}

func (s *httpSink) Name() string { return s.name }

func (s *httpSink) Send(ctx context.Context, n Notification) error {
	body, hdr, err := s.build(n)
	if err != nil {
		return permanentError{err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return permanentError{err}
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := s.client.Do(req)
	if err != nil {
		if errors.Is(err, ErrBlockedTarget) {
			return permanentError{err}
		}
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return fmt.Errorf("%s: status %d", s.name, resp.StatusCode)
	}
	return permanentError{fmt.Errorf("%s: status %d", s.name, resp.StatusCode)}
}

func NewWebhook(url, secret string, c *http.Client) Sink {
	return &httpSink{name: "webhook", url: url, client: c, build: func(n Notification) ([]byte, http.Header, error) {
		b, err := json.Marshal(n)
		if err != nil {
			return nil, nil, err
		}
		h := http.Header{"Content-Type": {"application/json"}}
		if secret != "" {
			m := hmac.New(sha256.New, []byte(secret))
			m.Write(b)
			h.Set(SignatureHeader, "sha256="+hex.EncodeToString(m.Sum(nil)))
		}
		return b, h, nil
	}}
}

func NewNtfy(url string, c *http.Client) Sink {
	return &httpSink{name: "ntfy", url: url, client: c, build: func(n Notification) ([]byte, http.Header, error) {
		h := http.Header{
			"Content-Type": {"text/plain; charset=utf-8"},
			"Title":        {n.Title},
			"Priority":     {strconv.Itoa(n.Priority)},
			"Tags":         {n.Kind},
		}
		return []byte(n.Text), h, nil
	}}
}

func NewSlack(url string, c *http.Client) Sink {
	return &httpSink{name: "slack", url: url, client: c, build: func(n Notification) ([]byte, http.Header, error) {
		b, err := json.Marshal(map[string]string{"text": "*" + slackEscape(n.Title) + "*\n" + slackEscape(n.Text)})
		return b, http.Header{"Content-Type": {"application/json"}}, err
	}}
}

var slackEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func slackEscape(s string) string { return slackEscaper.Replace(s) }
