package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"runtime/metrics"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/dst"
	"github.com/SigmaUno/subspace/internal/loadtest"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

type stats struct {
	total     *loadtest.Recorder
	window    atomic.Pointer[loadtest.Recorder]
	mu        sync.Mutex
	codes     map[string]int64
	internal  int64
	lastFault string
}

func newStats() *stats {
	s := &stats{total: loadtest.NewRecorder(), codes: map[string]int64{}}
	s.window.Store(loadtest.NewRecorder())
	return s
}

func (s *stats) observe(op string, d time.Duration, err error) {
	s.total.Observe(op, d, err)
	s.window.Load().Observe(op, d, err)
	if err == nil {
		return
	}
	code := errCode(err)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codes[code]++
	if code == client.CodeInternal || client.StatusOf(err) >= http.StatusInternalServerError {
		s.internal++
		s.lastFault = op + ": " + err.Error()
	}
}

func errCode(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	if c := client.CodeOf(err); c != "" {
		return c
	}
	var te *client.TransportError
	var ue *url.Error
	if errors.As(err, &te) || errors.As(err, &ue) {
		return "transport"
	}
	return "other"
}

func (s *stats) faults() (int64, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.internal, s.lastFault
}

func (s *stats) errorCodes() map[string]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int64, len(s.codes))
	for k, v := range s.codes {
		out[k] = v
	}
	return out
}

func (s *stats) rotate() loadtest.OpReport {
	o, _, _ := s.window.Swap(loadtest.NewRecorder()).Snapshot()
	return o
}

type Sample struct {
	T           float64 `json:"t_s"`
	Goroutines  int     `json:"goroutines"`
	HeapLive    uint64  `json:"heap_live_bytes"`
	HeapInuse   uint64  `json:"heap_inuse_bytes"`
	FDs         int     `json:"open_fds"`
	WAL         int64   `json:"wal_bytes"`
	WriterQueue int     `json:"writer_queue"`
	Requests    int64   `json:"requests"`
	Errors      int64   `json:"errors"`
	P99ms       float64 `json:"p99_ms"`
	SSE         int64   `json:"sse_streams"`
	Restarted   bool    `json:"restarted"`
	DataBytes   int64   `json:"data_bytes"`
	RetainBytes int64   `json:"retention_bytes"`

	Tables map[string]int64 `json:"tables,omitempty"`
}

var csvHeader = []string{"t_s", "goroutines", "heap_live_bytes", "heap_inuse_bytes", "open_fds", "wal_bytes",
	"writer_queue", "requests", "errors", "p99_ms", "sse_streams", "restarted", "data_bytes", "retention_bytes"}

func (s Sample) record(tables []string) []string {
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }
	i := func(v int64) string { return strconv.FormatInt(v, 10) }
	out := []string{f(s.T), i(int64(s.Goroutines)), strconv.FormatUint(s.HeapLive, 10), strconv.FormatUint(s.HeapInuse, 10),
		i(int64(s.FDs)), i(s.WAL), i(int64(s.WriterQueue)), i(s.Requests), i(s.Errors), f(s.P99ms), i(s.SSE),
		strconv.FormatBool(s.Restarted), i(s.DataBytes), i(s.RetainBytes)}
	for _, t := range tables {
		out = append(out, i(s.Tables[t]))
	}
	return out
}

func tableNames(samples []Sample) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range samples {
		for t := range s.Tables {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	sort.Strings(out)
	return out
}

func growth(samples []Sample, warmup time.Duration) []Trend {
	ys := map[string]func(Sample) float64{
		"p99_ms":          func(s Sample) float64 { return s.P99ms },
		"open_fds":        func(s Sample) float64 { return float64(s.FDs) },
		"wal_mib":         func(s Sample) float64 { return float64(s.WAL) / (1 << 20) },
		"writer_queue":    func(s Sample) float64 { return float64(s.WriterQueue) },
		"data_mib":        func(s Sample) float64 { return float64(s.DataBytes) / (1 << 20) },
		"retention_bytes": func(s Sample) float64 { return float64(s.RetainBytes) },
	}
	for _, t := range tableNames(samples) {
		ys["rows_"+t] = func(s Sample) float64 { return float64(s.Tables[t]) }
	}
	names := make([]string, 0, len(ys))
	for k := range ys {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]Trend, 0, len(names))
	for _, n := range names {
		t := trend(n, "per_min", samples, warmup, 0, ys[n])
		t.Exceeded = false
		out = append(out, t)
	}
	return out
}

func runtimeSample(s *Sample) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	live := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(live)
	s.Goroutines = runtime.NumGoroutine()
	s.HeapInuse = ms.HeapInuse
	if live[0].Value.Kind() == metrics.KindUint64 {
		s.HeapLive = live[0].Value.Uint64()
	}
	s.FDs = openFDs()
}

type Trend struct {
	Metric   string  `json:"metric"`
	Unit     string  `json:"unit"`
	Slope    float64 `json:"slope"`
	Limit    float64 `json:"limit"`
	Points   int     `json:"points"`
	Exceeded bool    `json:"exceeded"`
}

const minTrendPoints = 6

func trend(metric, unit string, samples []Sample, warmup time.Duration, limit float64, y func(Sample) float64) Trend {
	t := Trend{Metric: metric, Unit: unit, Limit: limit}
	var xs, ys []float64
	for _, s := range samples {
		if s.T >= warmup.Seconds() {
			xs, ys = append(xs, s.T/60), append(ys, y(s))
		}
	}
	t.Points = len(xs)
	if len(xs) < minTrendPoints {
		return t
	}
	t.Slope = slope(xs, ys)
	t.Exceeded = t.Slope > limit
	return t
}

func slope(xs, ys []float64) float64 {
	step := (len(xs) + 1023) / 1024
	var ss []float64
	for i := 0; i < len(xs); i += step {
		for j := i + 1; j < len(xs); j += step {
			if dx := xs[j] - xs[i]; dx > 0 {
				ss = append(ss, (ys[j]-ys[i])/dx)
			}
		}
	}
	if len(ss) == 0 {
		return 0
	}
	sort.Float64s(ss)
	if n := len(ss); n%2 == 0 {
		return (ss[n/2-1] + ss[n/2]) / 2
	}
	return ss[len(ss)/2]
}

type Violation struct {
	At        float64 `json:"t_s"`
	Invariant string  `json:"invariant"`
	Detail    string  `json:"detail"`
}

const (
	invChain      = "audit_hash_chain"
	invDone       = "done_is_final"
	invDurable    = "tasks_durable"
	invInternal   = "no_internal_errors"
	invBackup     = "backup_verified"
	invSnapshot   = "snapshot_readable"
	invRestart    = "restart_clean"
	sweepSlack    = 20 * time.Second
	auditPage     = 1000
	snapshotTries = 5
)

type checker struct {
	owner  *client.Owner
	lease  time.Duration
	window []*model.AuditEvent
	done   map[string]string
	counts map[string]int
	prev   *dst.State
}

func newChecker(o *client.Owner, lease time.Duration) *checker {
	return &checker{owner: o, lease: lease, done: map[string]string{}, counts: map[string]int{}}
}

func (c *checker) snapshot(ctx context.Context) (*dst.State, error) {
	for attempt := 0; ; attempt++ {
		st, err := c.read(ctx)
		if err != nil {
			return nil, err
		}
		last := int64(0)
		if n := len(st.Audit); n > 0 {
			last = st.Audit[n-1].ID
		}
		tail, err := c.owner.Audit(ctx, store.AuditFilter{SinceID: last, Limit: 1})
		if err != nil {
			return nil, fmt.Errorf("audit: %w", err)
		}
		if len(tail) == 0 || attempt == snapshotTries {
			return st, nil
		}
	}
}

func (c *checker) read(ctx context.Context) (*dst.State, error) {
	info, err := c.owner.State(ctx)
	if err != nil {
		return nil, fmt.Errorf("state: %w", err)
	}
	st := &dst.State{Now: time.Now(), Paused: info.Paused}
	if st.Capacity, err = c.owner.Capacity(ctx); err != nil {
		return nil, fmt.Errorf("capacity: %w", err)
	}
	since := int64(0)
	if len(c.window) > 0 {
		since = c.window[0].ID - 1
	}
	for {
		evs, err := c.owner.Audit(ctx, store.AuditFilter{SinceID: since, Limit: auditPage})
		if err != nil {
			return nil, fmt.Errorf("audit: %w", err)
		}
		st.Audit = append(st.Audit, evs...)
		if len(evs) < auditPage {
			break
		}
		since = evs[len(evs)-1].ID
	}
	if st.Briefs, err = c.owner.ListBriefs(ctx, "open"); err != nil {
		return nil, fmt.Errorf("briefs: %w", err)
	}
	for _, b := range st.Briefs {
		ts, err := c.owner.ListTasks(ctx, board.TaskQuery{BriefID: b.ID, Limit: 1000})
		if err != nil {
			return nil, fmt.Errorf("tasks of %s: %w", b.ID, err)
		}
		st.Tasks = append(st.Tasks, ts...)
	}
	return st, nil
}

func (c *checker) check(ctx context.Context, gate *sync.RWMutex) ([]Violation, error) {
	gate.Lock()
	st, err := c.snapshot(ctx)
	gate.Unlock()
	if err != nil {
		return nil, err
	}
	var out []Violation
	add := func(inv, detail string) { out = append(out, Violation{Invariant: inv, Detail: detail}) }

	chk := dst.NewChecker(c.lease)
	pre := &dst.State{Now: st.Now, Capacity: st.Capacity, Audit: c.window}
	if c.prev != nil {
		pre.Paused = c.prev.Paused
		reopened := map[string]bool{}
		for _, e := range st.Audit[min(len(c.window), len(st.Audit)):] {
			if e.Tool == "reopen" && e.TaskID != nil {
				reopened[*e.TaskID] = true
			}
		}
		for _, t := range c.prev.Tasks {
			if !reopened[t.ID] {
				pre.Tasks = append(pre.Tasks, t)
			}
		}
	}
	chk.Init(pre)
	st.Now = st.Now.Add(-sweepSlack)
	if v := chk.Check(dst.Op{Kind: dst.OpBatch}, dst.Result{}, pre, st); v != nil {
		add(v.Invariant, v.Detail)
	}
	c.prev = st
	if len(st.Audit) > len(c.window) {
		c.window = st.Audit[len(c.window):]
	}

	open := map[string]bool{}
	per := map[string]int{}
	for _, b := range st.Briefs {
		open[b.ID] = true
		per[b.ID] = 0
	}
	for _, t := range st.Tasks {
		per[t.BriefID]++
		if _, was := c.done[t.ID]; was && t.Status != model.StatusDone {
			add(invDone, fmt.Sprintf("task %s was done, now %s", t.ID, t.Status))
		}
		if t.Status == model.StatusDone {
			c.done[t.ID] = t.BriefID
		}
	}
	for id, n := range per {
		if n < c.counts[id] {
			add(invDurable, fmt.Sprintf("brief %s had %d tasks, now %d", id, c.counts[id], n))
		}
		c.counts[id] = n
	}
	for id, b := range c.done {
		if !open[b] {
			delete(c.done, id)
		}
	}
	for id := range c.counts {
		if !open[id] {
			delete(c.counts, id)
		}
	}
	return out, nil
}

func (c *checker) verifyChain(ctx context.Context) (*Violation, error) {
	var out struct {
		OK       bool   `json:"ok"`
		Events   int64  `json:"events"`
		BrokenID *int64 `json:"broken_id"`
	}
	if err := c.owner.Do(ctx, "audit_verify", http.MethodGet, "/api/v1/audit/verify", nil, &out); err != nil {
		return nil, err
	}
	if out.OK {
		return nil, nil
	}
	broken := "unknown"
	if out.BrokenID != nil {
		broken = strconv.FormatInt(*out.BrokenID, 10)
	}
	return &Violation{Invariant: invChain, Detail: fmt.Sprintf("chain broken at event %s after %d valid events", broken, out.Events)}, nil
}

func sortedCounts(m map[string]int64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
