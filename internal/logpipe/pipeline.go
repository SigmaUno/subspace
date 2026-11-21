package logpipe

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/redact"
)

const (
	DefaultCapacity = 2000
	masked          = "****"
)

type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "text":
		return FormatText, nil
	case "json":
		return FormatJSON, nil
	}
	return "", fmt.Errorf("log format must be text or json, got %q", s)
}

func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("log level must be debug, info, warn or error, got %q", s)
}

func LevelName(l slog.Level) string { return strings.ToLower(l.String()) }

var sensitiveKeys = []string{"token", "secret", "password", "passwd", "authorization", "cookie", "api_key", "apikey", "credential"}

func sensitive(key string) bool {
	key = strings.ToLower(key)
	for _, s := range sensitiveKeys {
		if strings.Contains(key, s) {
			return true
		}
	}
	return false
}

type Line struct {
	ID    int64             `json:"id"`
	TS    time.Time         `json:"ts"`
	Level string            `json:"level"`
	Msg   string            `json:"msg"`
	Attrs map[string]string `json:"attrs"`

	level slog.Level
}

type Query struct {
	Min     slog.Level
	SinceID int64
	SinceTS time.Time
	Limit   int
}

type ring struct {
	mu   sync.Mutex
	buf  []Line
	next int
	size int
	seq  int64
}

func newRing(capacity int) *ring {
	return &ring{buf: make([]Line, capacity)}
}

func (r *ring) add(l Line) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	l.ID = r.seq
	r.buf[r.next] = l
	r.next = (r.next + 1) % len(r.buf)
	r.size = min(r.size+1, len(r.buf))
}

func (r *ring) query(q Query) []Line {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Line
	for i := r.size; i > 0; i-- {
		l := r.buf[(r.next-i+len(r.buf))%len(r.buf)]
		if l.level < q.Min || l.ID <= q.SinceID || (!q.SinceTS.IsZero() && !l.TS.After(q.SinceTS)) {
			continue
		}
		out = append(out, l)
	}
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[len(out)-q.Limit:]
	}
	return out
}

type Options struct {
	Format   Format
	Out      io.Writer
	Level    slog.Level
	Capacity int
}

type Pipeline struct {
	level *slog.LevelVar
	ring  *ring
	inner slog.Handler
}

func New(o Options) *Pipeline {
	p := &Pipeline{level: new(slog.LevelVar), ring: newRing(capacityOrDefault(o.Capacity))}
	p.level.Set(o.Level)
	opts := &slog.HandlerOptions{Level: p.level}
	if o.Format == FormatJSON {
		p.inner = slog.NewJSONHandler(o.Out, opts)
	} else {
		p.inner = slog.NewTextHandler(o.Out, opts)
	}
	return p
}

func Wrap(inner slog.Handler, capacity int) *Pipeline {
	p := &Pipeline{level: new(slog.LevelVar), ring: newRing(capacityOrDefault(capacity)), inner: inner}
	lowest := slog.LevelError
	for _, l := range []slog.Level{slog.LevelWarn, slog.LevelInfo, slog.LevelDebug} {
		if inner.Enabled(context.Background(), l) {
			lowest = l
		}
	}
	p.level.Set(lowest)
	return p
}

func capacityOrDefault(n int) int {
	if n <= 0 {
		return DefaultCapacity
	}
	return n
}

func (p *Pipeline) Logger() *slog.Logger { return slog.New(&handler{p: p, inner: p.inner}) }

func (p *Pipeline) Level() slog.Level { return p.level.Level() }

func (p *Pipeline) SetLevel(l slog.Level) { p.level.Set(l) }

func (p *Pipeline) Lines(q Query) []Line { return p.ring.query(q) }

type handler struct {
	p      *Pipeline
	inner  slog.Handler
	prefix string
	attrs  map[string]string
}

func (h *handler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.p.level.Level() }

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	attrs := make(map[string]string, len(h.attrs)+r.NumAttrs())
	for k, v := range h.attrs {
		attrs[k] = v
	}
	r.Attrs(func(a slog.Attr) bool {
		flatten(attrs, h.prefix, a)
		return true
	})
	h.p.ring.add(Line{TS: r.Time, Level: LevelName(r.Level), Msg: redact.String(r.Message), Attrs: attrs, level: r.Level})
	if h.inner.Enabled(ctx, r.Level) {
		return h.inner.Handle(ctx, r)
	}
	return nil
}

func (h *handler) WithAttrs(as []slog.Attr) slog.Handler {
	if len(as) == 0 {
		return h
	}
	attrs := make(map[string]string, len(h.attrs)+len(as))
	for k, v := range h.attrs {
		attrs[k] = v
	}
	for _, a := range as {
		flatten(attrs, h.prefix, a)
	}
	return &handler{p: h.p, inner: h.inner.WithAttrs(as), prefix: h.prefix, attrs: attrs}
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &handler{p: h.p, inner: h.inner.WithGroup(name), prefix: h.prefix + name + ".", attrs: h.attrs}
}

func flatten(out map[string]string, prefix string, a slog.Attr) {
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		if a.Key != "" {
			prefix += a.Key + "."
		}
		for _, g := range v.Group() {
			flatten(out, prefix, g)
		}
		return
	}
	if a.Key == "" {
		return
	}
	key := prefix + a.Key
	if sensitive(key) {
		out[key] = masked
		return
	}
	out[key] = redact.String(v.String())
}
