package metrics

import (
	"bufio"
	"context"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	maxLabels        = 4
	defaultMaxSeries = 2048
	overflowLabel    = "other"
	ContentType      = "text/plain; version=0.0.4; charset=utf-8"
)

type key [maxLabels]string

type family interface {
	name() string
	write(w textWriter)
}

type textWriter struct{ w *bufio.Writer }

func (t textWriter) str(s string) { _, _ = t.w.WriteString(s) }

func (t textWriter) char(c byte) { _ = t.w.WriteByte(c) }

type Registry struct {
	mu     sync.Mutex
	fams   []family
	hooks  []func(context.Context)
	scrape sync.Mutex
}

func NewRegistry() *Registry { return &Registry{} }

func (r *Registry) add(f family) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, o := range r.fams {
		if o.name() == f.name() {
			panic("metrics: duplicate family " + f.name())
		}
	}
	r.fams = append(r.fams, f)
}

func (r *Registry) OnScrape(f func(context.Context)) {
	r.mu.Lock()
	r.hooks = append(r.hooks, f)
	r.mu.Unlock()
}

func (r *Registry) WriteText(ctx context.Context, w io.Writer) error {
	r.scrape.Lock()
	defer r.scrape.Unlock()
	r.mu.Lock()
	hooks := slices.Clone(r.hooks)
	fams := slices.Clone(r.fams)
	r.mu.Unlock()
	for _, h := range hooks {
		h(ctx)
	}
	slices.SortFunc(fams, func(a, b family) int { return strings.Compare(a.name(), b.name()) })
	bw := bufio.NewWriter(w)
	tw := textWriter{bw}
	for _, f := range fams {
		f.write(tw)
	}
	return bw.Flush()
}

func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", ContentType)
		if req.Method == http.MethodHead {
			return
		}
		_ = r.WriteText(req.Context(), w)
	})
}

type vec[T any] struct {
	names []string
	max   int
	mk    func() *T
	mu    sync.RWMutex
	m     map[key]*T
}

func newVec[T any](names []string, mk func() *T) *vec[T] {
	if len(names) > maxLabels {
		panic("metrics: too many labels")
	}
	return &vec[T]{names: names, max: defaultMaxSeries, mk: mk, m: make(map[key]*T)}
}

func (v *vec[T]) get(values []string) *T {
	if len(values) != len(v.names) {
		panic("metrics: label count mismatch")
	}
	var k key
	copy(k[:], values)
	v.mu.RLock()
	s, ok := v.m[k]
	v.mu.RUnlock()
	if ok {
		return s
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if s, ok = v.m[k]; ok {
		return s
	}
	if len(v.m) >= v.max {
		k = key{}
		for i := range v.names {
			k[i] = overflowLabel
		}
		if s, ok = v.m[k]; ok {
			return s
		}
	}
	s = v.mk()
	v.m[k] = s
	return s
}

func (v *vec[T]) sorted() ([]key, []*T) {
	v.mu.RLock()
	keys := make([]key, 0, len(v.m))
	for k := range v.m {
		keys = append(keys, k)
	}
	v.mu.RUnlock()
	slices.SortFunc(keys, func(a, b key) int {
		for i := range a {
			if c := strings.Compare(a[i], b[i]); c != 0 {
				return c
			}
		}
		return 0
	})
	series := make([]*T, len(keys))
	v.mu.RLock()
	for i, k := range keys {
		series[i] = v.m[k]
	}
	v.mu.RUnlock()
	return keys, series
}

type Counter struct{ v atomic.Uint64 }

func (c *Counter) Inc()         { c.v.Add(1) }
func (c *Counter) Add(n uint64) { c.v.Add(n) }

type CounterVec struct {
	n, help string
	v       *vec[Counter]
}

func (r *Registry) NewCounterVec(name, help string, labels ...string) *CounterVec {
	c := &CounterVec{n: name, help: help, v: newVec(labels, func() *Counter { return new(Counter) })}
	r.add(c)
	return c
}

func (c *CounterVec) With(values ...string) *Counter { return c.v.get(values) }

func (c *CounterVec) name() string { return c.n }

func (c *CounterVec) write(w textWriter) {
	header(w, c.n, c.help, "counter")
	keys, series := c.v.sorted()
	for i, k := range keys {
		sample(w, c.n, "", c.v.names, k, "", float64(series[i].v.Load()))
	}
}

type Gauge struct{ bits atomic.Uint64 }

func (g *Gauge) Set(v float64) { g.bits.Store(math.Float64bits(v)) }

func (g *Gauge) Add(d float64) {
	for {
		old := g.bits.Load()
		if g.bits.CompareAndSwap(old, math.Float64bits(math.Float64frombits(old)+d)) {
			return
		}
	}
}

func (g *Gauge) Value() float64 { return math.Float64frombits(g.bits.Load()) }

type GaugeVec struct {
	n, help string
	v       *vec[Gauge]
}

func (r *Registry) NewGaugeVec(name, help string, labels ...string) *GaugeVec {
	g := &GaugeVec{n: name, help: help, v: newVec(labels, func() *Gauge { return new(Gauge) })}
	r.add(g)
	return g
}

func (g *GaugeVec) With(values ...string) *Gauge { return g.v.get(values) }

func (g *GaugeVec) name() string { return g.n }

func (g *GaugeVec) write(w textWriter) {
	header(w, g.n, g.help, "gauge")
	keys, series := g.v.sorted()
	for i, k := range keys {
		sample(w, g.n, "", g.v.names, k, "", series[i].Value())
	}
}

type Histogram struct {
	bounds []float64
	counts []atomic.Uint64
	count  atomic.Uint64
	sum    atomic.Uint64
}

func (h *Histogram) Observe(v float64) {
	i, _ := slices.BinarySearch(h.bounds, v)
	h.counts[i].Add(1)
	h.count.Add(1)
	for {
		old := h.sum.Load()
		if h.sum.CompareAndSwap(old, math.Float64bits(math.Float64frombits(old)+v)) {
			return
		}
	}
}

type HistogramVec struct {
	n, help string
	bounds  []float64
	v       *vec[Histogram]
}

func (r *Registry) NewHistogramVec(name, help string, bounds []float64, labels ...string) *HistogramVec {
	if !slices.IsSorted(bounds) {
		panic("metrics: unsorted buckets")
	}
	bounds = slices.Clone(bounds)
	h := &HistogramVec{n: name, help: help, bounds: bounds, v: newVec(labels, func() *Histogram {
		return &Histogram{bounds: bounds, counts: make([]atomic.Uint64, len(bounds)+1)}
	})}
	r.add(h)
	return h
}

func (h *HistogramVec) With(values ...string) *Histogram { return h.v.get(values) }

func (h *HistogramVec) name() string { return h.n }

func (h *HistogramVec) write(w textWriter) {
	header(w, h.n, h.help, "histogram")
	keys, series := h.v.sorted()
	for i, k := range keys {
		s := series[i]
		var cum uint64
		for j := range s.counts {
			cum += s.counts[j].Load()
			le := "+Inf"
			if j < len(h.bounds) {
				le = formatFloat(h.bounds[j])
			}
			sample(w, h.n, "_bucket", h.v.names, k, le, float64(cum))
		}
		sample(w, h.n, "_sum", h.v.names, k, "", math.Float64frombits(s.sum.Load()))
		sample(w, h.n, "_count", h.v.names, k, "", float64(s.count.Load()))
	}
}

func header(w textWriter, name, help, typ string) {
	w.str("# HELP ")
	w.str(name)
	w.char(' ')
	escape(w, help, false)
	w.str("\n# TYPE ")
	w.str(name)
	w.char(' ')
	w.str(typ)
	w.char('\n')
}

func sample(w textWriter, name, suffix string, names []string, k key, le string, v float64) {
	w.str(name)
	w.str(suffix)
	if len(names) > 0 || le != "" {
		w.char('{')
		for i, n := range names {
			if i > 0 {
				w.char(',')
			}
			w.str(n)
			w.str(`="`)
			escape(w, k[i], true)
			w.char('"')
		}
		if le != "" {
			if len(names) > 0 {
				w.char(',')
			}
			w.str(`le="`)
			w.str(le)
			w.char('"')
		}
		w.char('}')
	}
	w.char(' ')
	w.str(formatFloat(v))
	w.char('\n')
}

func escape(w textWriter, s string, quotes bool) {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\':
			w.str(`\\`)
		case c == '\n':
			w.str(`\n`)
		case c == '"' && quotes:
			w.str(`\"`)
		default:
			w.char(c)
		}
	}
}

func formatFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
