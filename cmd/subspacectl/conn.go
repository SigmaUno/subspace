package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	exitFailure     = 1
	exitUsage       = 2
	exitAuth        = 3
	exitNotFound    = 4
	exitConflict    = 5
	exitUnavailable = 6

	defaultMaxResponse = 32 << 20
	transferMax        = 256 << 20
	defaultTimeout     = 60 * time.Second
	transferTimeout    = 10 * time.Minute
	redacted           = "[redacted]"
)

var agentToken = regexp.MustCompile(`ssp_[A-Za-z0-9_-]{8,}`)

type request struct {
	method      string
	path        string
	query       url.Values
	body        any
	payload     []byte
	contentType string
	accept      string
	maxResponse int
	timeout     time.Duration
}

type response struct {
	status int
	header http.Header
	body   []byte
}

type conn struct {
	base  *url.URL
	token string
	hc    *http.Client
}

type cliError struct {
	exit   int
	status int
	code   string
	msg    string
	json   bool
}

func (e *cliError) Error() string { return e.msg }

func (e *cliError) render() string {
	if !e.json {
		return "subspacectl: " + e.msg
	}
	body := map[string]any{"code": e.code, "message": e.msg, "exit": e.exit}
	if e.code == "" {
		body["code"] = "error"
	}
	if e.status != 0 {
		body["status"] = e.status
	}
	data, _ := json.Marshal(map[string]any{"error": body})
	return string(data)
}

func failf(exit int, format string, args ...any) *cliError {
	return &cliError{exit: exit, msg: fmt.Sprintf(format, args...)}
}

func exitCode(err error) int {
	var ce *cliError
	if errors.As(err, &ce) {
		return ce.exit
	}
	return exitFailure
}

func render(err error) string {
	var ce *cliError
	if errors.As(err, &ce) {
		return ce.render()
	}
	return "subspacectl: " + err.Error()
}

func finish(err error, asJSON bool) error {
	if err == nil {
		return nil
	}
	var ce *cliError
	if !errors.As(err, &ce) {
		ce = &cliError{exit: exitFailure, msg: err.Error()}
	}
	out := *ce
	out.json = asJSON
	return &out
}

func statusExit(status int) int {
	switch status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
		return exitUsage
	case http.StatusUnauthorized, http.StatusForbidden:
		return exitAuth
	case http.StatusNotFound:
		return exitNotFound
	case http.StatusConflict, http.StatusLocked:
		return exitConflict
	}
	return exitFailure
}

func newConn(base, token string) (*conn, error) {
	endpoint, err := url.Parse(base)
	if err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") ||
		endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Opaque != "" {
		return nil, errors.New("the Subspace URL must be an HTTP(S) base URL without credentials, query or fragment")
	}
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &conn{base: endpoint, token: token, hc: hc}, nil
}

func (c *conn) redact(s string) string {
	if c.token != "" {
		s = strings.ReplaceAll(s, c.token, redacted)
	}
	return agentToken.ReplaceAllString(s, redacted)
}

func (c *conn) httpRequest(ctx context.Context, req request, accept string) (*http.Request, error) {
	endpoint := c.base.JoinPath("api/v1", req.path)
	endpoint.RawQuery = req.query.Encode()
	body, contentType := req.payload, req.contentType
	if req.body != nil {
		data, err := json.Marshal(req.body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		body, contentType = data, "application/json"
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.method, endpoint.String(), bytes.NewReader(body)) //nolint:gosec
	if err != nil {
		return nil, err
	}
	if accept == "" {
		accept = "application/json"
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.token)
	httpReq.Header.Set("Accept", accept)
	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}
	return httpReq, nil
}

func (c *conn) do(ctx context.Context, req request) (*response, error) {
	timeout := req.timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	httpReq, err := c.httpRequest(ctx, req, req.accept)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(httpReq) //nolint:gosec
	if err != nil {
		return nil, &cliError{exit: exitUnavailable, code: "unavailable", msg: c.redact("contact Subspace: " + err.Error())}
	}
	defer func() { _ = resp.Body.Close() }()
	limit := req.maxResponse
	if limit == 0 {
		limit = defaultMaxResponse
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, &cliError{exit: exitUnavailable, code: "unavailable", msg: c.redact("read Subspace response: " + err.Error())}
	}
	if len(payload) > limit {
		return nil, failf(exitFailure, "response exceeds %d MiB", limit>>20)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, c.apiError(resp.StatusCode, payload)
	}
	return &response{status: resp.StatusCode, header: resp.Header, body: payload}, nil
}

func (c *conn) apiError(status int, payload []byte) *cliError {
	var failure struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	e := &cliError{exit: statusExit(status), status: status}
	switch {
	case json.Unmarshal(payload, &failure) == nil && failure.Error.Message != "":
		e.code = failure.Error.Code
		if e.code != "" {
			e.msg = fmt.Sprintf("HTTP %d (%s): %s", status, e.code, failure.Error.Message)
		} else {
			e.msg = fmt.Sprintf("HTTP %d: %s", status, failure.Error.Message)
		}
	default:
		e.msg = fmt.Sprintf("unexpected Subspace response: HTTP %d", status)
	}
	e.msg = c.redact(e.msg)
	return e
}

func (r *response) kind() string {
	mt, _, _ := mime.ParseMediaType(r.header.Get("Content-Type"))
	switch {
	case mt == "application/x-ndjson":
		return "ndjson"
	case mt == "application/json", strings.HasSuffix(mt, "+json"), mt == "" && json.Valid(r.body):
		return "json"
	}
	return "text"
}

type printer struct {
	w    io.Writer
	json bool
}

func (p *printer) jsonBytes(data []byte) error {
	var buf bytes.Buffer
	var err error
	if p.json {
		err = json.Compact(&buf, bytes.TrimSpace(data))
	} else {
		err = json.Indent(&buf, bytes.TrimSpace(data), "", "  ")
	}
	if err != nil {
		return fmt.Errorf("invalid JSON in Subspace response: %w", err)
	}
	buf.WriteByte('\n')
	_, err = buf.WriteTo(p.w)
	return err
}

func (p *printer) value(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return p.jsonBytes(data)
}

func (p *printer) text(s string) error {
	if p.json {
		return p.value(map[string]string{"text": s})
	}
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	_, err := io.WriteString(p.w, s)
	return err
}

func (p *printer) response(resp *response) error {
	if len(bytes.TrimSpace(resp.body)) == 0 {
		return p.value(map[string]bool{"ok": true})
	}
	switch resp.kind() {
	case "json":
		return p.jsonBytes(resp.body)
	case "ndjson":
		_, err := p.w.Write(resp.body)
		return err
	}
	return p.text(string(resp.body))
}

func call(req request) command {
	return func(ctx context.Context, c *conn, p *printer) error {
		resp, err := c.do(ctx, req)
		if err != nil {
			return err
		}
		return p.response(resp)
	}
}

func save(path string, data []byte, p *printer, extra map[string]any) error {
	if path == "-" {
		if p.json {
			return failf(exitUsage, "--out - writes raw data and cannot be combined with --json")
		}
		_, err := p.w.Write(data)
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	out := map[string]any{"path": path, "bytes": len(data)}
	for k, v := range extra {
		out[k] = v
	}
	return p.value(out)
}

type options struct {
	json      bool
	url       string
	token     string
	tokenFile string
}

func globals(args []string) (options, []string, error) {
	var o options
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		name, value, hasValue := strings.Cut(arg, "=")
		switch name {
		case "--json":
			o.json = true
			if hasValue {
				on, err := parseBool(value)
				if err != nil {
					return o, nil, err
				}
				o.json = on
			}
		case "--url", "--token", "--token-file":
			if !hasValue {
				if i+1 == len(args) {
					return o, nil, fmt.Errorf("%s requires a value", name)
				}
				i++
				value = args[i]
			}
			switch name {
			case "--url":
				o.url = value
			case "--token":
				o.token = value
			default:
				o.tokenFile = value
			}
		default:
			rest = append(rest, arg)
		}
	}
	return o, rest, nil
}

func parseBool(value string) (bool, error) {
	switch value {
	case "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	}
	return false, fmt.Errorf("invalid boolean %q", value)
}

func resolveToken(o options, getenv func(string) string) (string, error) {
	file := o.tokenFile
	if o.token == "" && file == "" {
		file = getenv("SUBSPACE_OWNER_TOKEN_FILE")
	}
	var token string
	switch {
	case o.token != "":
		token = o.token
	case file != "":
		data, err := os.ReadFile(file) //nolint:gosec
		if err != nil {
			return "", failf(exitAuth, "read token file: %v", err)
		}
		token = string(data)
	default:
		token = getenv("SUBSPACE_OWNER_TOKEN")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", failf(exitAuth, "set SUBSPACE_OWNER_TOKEN, SUBSPACE_OWNER_TOKEN_FILE, --token-file or --token before making API calls")
	}
	return token, nil
}

func connect(o options, getenv func(string) string) (*conn, error) {
	token, err := resolveToken(o, getenv)
	if err != nil {
		return nil, err
	}
	base := o.url
	if base == "" {
		base = strings.TrimSpace(getenv("SUBSPACE_URL"))
	}
	if base == "" {
		base = "http://localhost:8080"
	}
	c, err := newConn(base, token)
	if err != nil {
		return nil, failf(exitUsage, "%s", err)
	}
	return c, nil
}
