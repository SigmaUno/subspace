package metrics

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	HeaderRequestID = "X-Request-Id"

	maxCapture   = 1 << 20
	maxRequestID = 64
	eventsPath   = "/api/v1/events"
	laggedPrefix = "event: lagged\n"
)

type idKey struct{}

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(idKey{}).(string)
	return id
}

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, idKey{}, id)
}

type Options struct {
	Log  *slog.Logger
	Role func(token string) string
}

type idGen struct {
	prefix string
	n      atomic.Uint64
}

func newIDGen() *idGen {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return &idGen{prefix: hex.EncodeToString(b[:])}
}

func (g *idGen) next() string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], g.n.Add(1))
	return g.prefix + hex.EncodeToString(b[2:])
}

func validID(id string) bool {
	if id == "" || len(id) > maxRequestID {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

func ValidRequestID(id string) bool { return validID(id) }

func (s *Set) Middleware(next http.Handler, o Options) http.Handler {
	gen := newIDGen()
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := r.Header.Get(HeaderRequestID)
		if !validID(id) {
			id = gen.next()
			r.Header.Set(HeaderRequestID, id)
		}
		w.Header().Set(HeaderRequestID, id)
		req := r.WithContext(WithRequestID(r.Context(), id))
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		if req.URL.Path == eventsPath {
			rec.dropped = s.SSEDropped.With()
		}
		var reqBuf *bytes.Buffer
		var body *teeBody
		if r.URL.Path == "/mcp" && r.Method == http.MethodPost {
			reqBuf, rec.buf = getBuf(), getBuf()
			body = &teeBody{ReadCloser: r.Body, buf: reqBuf}
			req.Body = body
			defer putBuf(reqBuf)
			defer putBuf(rec.buf)
		}
		next.ServeHTTP(rec, req)

		route := req.Pattern
		if route == "" {
			route = "unmatched"
		}
		dur := time.Since(start)
		s.HTTPRequests.With(route, strconv.Itoa(rec.status)).Inc()
		if rec.dropped == nil {
			s.HTTPLatency.With(route).Observe(dur.Seconds())
		}
		if body != nil {
			s.observeMCP(req, body, rec, dur, o.Role)
		}
		if rec.status >= 500 {
			log.LogAttrs(req.Context(), slog.LevelWarn, "request failed", slog.String("request_id", id),
				slog.String("route", route), slog.Int("status", rec.status), slog.Duration("duration", dur))
		} else if log.Enabled(req.Context(), slog.LevelDebug) {
			log.LogAttrs(req.Context(), slog.LevelDebug, "request", slog.String("request_id", id),
				slog.String("route", route), slog.Int("status", rec.status), slog.Duration("duration", dur))
		}
	})
}

type toolCall struct {
	Method string `json:"method"`
	Params struct {
		Name string `json:"name"`
	} `json:"params"`
}

type toolError struct {
	Result struct {
		Structured struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"structuredContent"`
	} `json:"result"`
}

var (
	rpcErrorMark  = []byte(`"error":{"code":-`)
	toolErrorMark = []byte(`"isError":true`)
)

func (s *Set) observeMCP(r *http.Request, body *teeBody, rec *recorder, dur time.Duration, roleOf func(string) string) {
	if body.over {
		return
	}
	var call toolCall
	if err := json.Unmarshal(body.buf.Bytes(), &call); err != nil || call.Method != "tools/call" {
		return
	}
	tool, result := "unknown", "http_error"
	if rec.status == http.StatusOK && !rec.over {
		resp := rec.buf.Bytes()
		switch {
		case bytes.Contains(resp, rpcErrorMark):
			result = "rpc_error"
		case bytes.Contains(resp, toolErrorMark):
			tool, result = call.Params.Name, "error"
			var te toolError
			if json.Unmarshal(resp, &te) == nil && te.Result.Structured.Error.Code != "" {
				result = te.Result.Structured.Error.Code
			}
		default:
			tool, result = call.Params.Name, "ok"
		}
	}
	role := "none"
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && roleOf != nil {
		if v := roleOf(tok); v != "" {
			role = v
		}
	}
	s.MCPCalls.With(tool, role, result).Inc()
	s.MCPLatency.With(tool, role).Observe(dur.Seconds())
}

type teeBody struct {
	io.ReadCloser
	buf  *bytes.Buffer
	over bool
}

func (t *teeBody) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	if n > 0 && !t.over {
		if t.buf.Len()+n > maxCapture {
			t.over = true
		} else {
			t.buf.Write(p[:n])
		}
	}
	return n, err
}

type recorder struct {
	http.ResponseWriter
	status  int
	wrote   bool
	buf     *bytes.Buffer
	over    bool
	dropped *Counter
}

func (c *recorder) WriteHeader(code int) {
	if !c.wrote {
		c.status, c.wrote = code, true
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *recorder) Write(p []byte) (int, error) {
	c.wrote = true
	if c.buf != nil && !c.over {
		if c.buf.Len()+len(p) > maxCapture {
			c.over = true
		} else {
			c.buf.Write(p)
		}
	}
	if c.dropped != nil && bytes.HasPrefix(p, []byte(laggedPrefix)) {
		c.dropped.Add(lagged(p))
	}
	return c.ResponseWriter.Write(p)
}

func (c *recorder) Flush() { _ = http.NewResponseController(c.ResponseWriter).Flush() }

func (c *recorder) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func lagged(p []byte) uint64 {
	var f struct {
		Data struct {
			Dropped uint64 `json:"dropped"`
		} `json:"data"`
	}
	_, data, _ := bytes.Cut(p, []byte("data: "))
	if json.Unmarshal(bytes.TrimSpace(data), &f) != nil {
		return 0
	}
	return f.Data.Dropped
}
