package logpipe

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"hash/fnv"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/metrics"
	"github.com/SigmaUno/subspace/internal/trace"
)

const (
	mcpPath    = "/mcp"
	peekLimit  = 2048
	healthPath = "/healthz"
	readyPath  = "/readyz"
	logsPath   = "/api/v1/logs"
)

type AccessOptions struct {
	Log    *slog.Logger
	Now    func() time.Time
	Sample float64
	Route  func(*http.Request) string
	Actor  func(*http.Request) (agent, scope string)
}

func AccessLog(next http.Handler, o AccessOptions) http.Handler {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := now()
		id := r.Header.Get(metrics.HeaderRequestID)
		if !metrics.ValidRequestID(id) {
			id = newRequestID()
			r.Header.Set(metrics.HeaderRequestID, id)
		}
		w.Header().Set(metrics.HeaderRequestID, id)
		var agent, scope string
		if o.Actor != nil {
			agent, scope = o.Actor(r)
		}
		var body *peek
		if r.Method == http.MethodPost && r.URL.Path == mcpPath {
			body = &peek{ReadCloser: r.Body}
			r.Body = body
		}
		cw := &capture{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(cw, r)

		route := "unmatched"
		if o.Route != nil {
			route = routePath(o.Route(r))
		}
		var tool string
		if body != nil {
			tool = body.tool()
		}
		if noisy(r.Method, route, cw.status, tool) && !Sampled(id, o.Sample) {
			return
		}
		level := slog.LevelInfo
		if cw.status >= http.StatusInternalServerError {
			level = slog.LevelWarn
		}
		attrs := []slog.Attr{
			slog.String("method", r.Method),
			slog.String("route", route),
			slog.Int("status", cw.status),
			slog.Duration("duration", now().Sub(start)),
			slog.Int64("bytes", cw.bytes),
			slog.String("request_id", id),
		}
		if tid := trace.TraceID(r.Context()); tid != "" {
			attrs = append(attrs, slog.String("trace_id", tid))
		}
		if agent != "" {
			attrs = append(attrs, slog.String("agent", agent))
		}
		if scope != "" {
			attrs = append(attrs, slog.String("scope", scope))
		}
		o.Log.LogAttrs(r.Context(), level, "request", attrs...)
	})
}

func Sampled(id string, rate float64) bool {
	if rate >= 1 {
		return true
	}
	if rate <= 0 {
		return false
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return float64(h.Sum32()) < rate*(1<<32)
}

func noisy(method, route string, status int, tool string) bool {
	if status < 200 || status > 299 {
		return false
	}
	switch {
	case route == healthPath, route == readyPath:
		return true
	case method == http.MethodGet && route == logsPath:
		return true
	case route == mcpPath:
		return tool == "heartbeat"
	}
	return false
}

func routePath(pattern string) string {
	if pattern == "" {
		return "unmatched"
	}
	if method, path, ok := strings.Cut(pattern, " "); ok && !strings.Contains(method, "/") {
		return path
	}
	return pattern
}

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

type capture struct {
	http.ResponseWriter
	status int
	bytes  int64
	wrote  bool
}

func (c *capture) WriteHeader(code int) {
	if !c.wrote {
		c.status = code
		c.wrote = true
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *capture) Write(p []byte) (int, error) {
	c.wrote = true
	n, err := c.ResponseWriter.Write(p)
	c.bytes += int64(n)
	return n, err
}

func (c *capture) Flush() { _ = http.NewResponseController(c.ResponseWriter).Flush() }

func (c *capture) Unwrap() http.ResponseWriter { return c.ResponseWriter }

type peek struct {
	io.ReadCloser
	buf   []byte
	total int
}

func (p *peek) Read(b []byte) (int, error) {
	n, err := p.ReadCloser.Read(b)
	p.total += n
	if room := peekLimit - len(p.buf); room > 0 {
		p.buf = append(p.buf, b[:min(n, room)]...)
	}
	return n, err
}

func (p *peek) tool() string {
	if p.total == 0 || p.total > peekLimit {
		return ""
	}
	var call struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if json.Unmarshal(p.buf, &call) != nil || call.Method != "tools/call" {
		return ""
	}
	return call.Params.Name
}
