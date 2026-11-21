package loadtest

import (
	"context"
	"math"
	"runtime/metrics"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Histogram is a lock-free log-bucketed duration histogram (about 2% relative precision).
type Histogram struct {
	buckets [histBuckets]atomic.Int64
	count   atomic.Int64
	sum     atomic.Int64
	max     atomic.Int64
}

const (
	histGrowth  = 1.02
	histBuckets = 1200 // covers 1µs .. >1h
)

var histLogGrowth = math.Log(histGrowth)

func bucketOf(us int64) int {
	if us <= 1 {
		return 0
	}
	i := int(math.Log(float64(us))/histLogGrowth) + 1
	if i >= histBuckets {
		i = histBuckets - 1
	}
	return i
}

// bucketUpper returns the largest value (µs) that falls into bucket i.
func bucketUpper(i int) int64 {
	if i == 0 {
		return 1
	}
	return int64(math.Ceil(math.Pow(histGrowth, float64(i))))
}

// Record adds one sample. Negative samples count as zero.
func (h *Histogram) Record(d time.Duration) {
	us := d.Microseconds()
	if us < 0 {
		us = 0
	}
	h.buckets[bucketOf(us)].Add(1)
	h.count.Add(1)
	h.sum.Add(us)
	for {
		m := h.max.Load()
		if us <= m || h.max.CompareAndSwap(m, us) {
			break
		}
	}
}

// Merge adds all samples of o into h.
func (h *Histogram) Merge(o *Histogram) {
	for i := range o.buckets {
		if n := o.buckets[i].Load(); n > 0 {
			h.buckets[i].Add(n)
		}
	}
	h.count.Add(o.count.Load())
	h.sum.Add(o.sum.Load())
	for {
		m, om := h.max.Load(), o.max.Load()
		if om <= m || h.max.CompareAndSwap(m, om) {
			break
		}
	}
}

// Count is the number of samples.
func (h *Histogram) Count() int64 { return h.count.Load() }

// Max is the largest sample.
func (h *Histogram) Max() time.Duration { return time.Duration(h.max.Load()) * time.Microsecond }

// Mean is the average sample.
func (h *Histogram) Mean() time.Duration {
	n := h.count.Load()
	if n == 0 {
		return 0
	}
	return time.Duration(h.sum.Load()/n) * time.Microsecond
}

// Quantile returns the q-quantile (0..1), never larger than Max.
func (h *Histogram) Quantile(q float64) time.Duration {
	n := h.count.Load()
	if n == 0 {
		return 0
	}
	rank := int64(math.Ceil(q * float64(n)))
	if rank < 1 {
		rank = 1
	}
	var cum int64
	for i := range h.buckets {
		cum += h.buckets[i].Load()
		if cum >= rank {
			v := bucketUpper(i)
			if m := h.max.Load(); v > m {
				v = m
			}
			return time.Duration(v) * time.Microsecond
		}
	}
	return h.Max()
}

const maxErrSamples = 5

type opStats struct {
	hist    Histogram
	errors  atomic.Int64
	mu      sync.Mutex
	samples []string
}

// Recorder collects latency and error statistics per operation.
type Recorder struct {
	mu      sync.RWMutex
	ops     map[string]*opStats
	overall opStats
}

// NewRecorder returns an empty recorder.
func NewRecorder() *Recorder { return &Recorder{ops: map[string]*opStats{}} }

func (r *Recorder) op(name string) *opStats {
	r.mu.RLock()
	s := r.ops[name]
	r.mu.RUnlock()
	if s != nil {
		return s
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if s = r.ops[name]; s == nil {
		s = &opStats{}
		r.ops[name] = s
	}
	return s
}

// Observe records one request of op that took d; err != nil counts as an error.
func (r *Recorder) Observe(op string, d time.Duration, err error) {
	s := r.op(op)
	s.hist.Record(d)
	r.overall.hist.Record(d)
	if err == nil {
		return
	}
	s.errors.Add(1)
	r.overall.errors.Add(1)
	msg := op + ": " + err.Error()
	for _, st := range []*opStats{s, &r.overall} {
		st.mu.Lock()
		if len(st.samples) < maxErrSamples {
			dup := false
			for _, x := range st.samples {
				dup = dup || x == msg
			}
			if !dup {
				st.samples = append(st.samples, msg)
			}
		}
		st.mu.Unlock()
	}
}

// Count returns the number of requests observed so far.
func (r *Recorder) Count() int64 { return r.overall.hist.Count() }

// OpReport summarises one operation.
type OpReport struct {
	Name   string  `json:"name"`
	Count  int64   `json:"count"`
	Errors int64   `json:"errors"`
	P50ms  float64 `json:"p50_ms"`
	P95ms  float64 `json:"p95_ms"`
	P99ms  float64 `json:"p99_ms"`
	MaxMs  float64 `json:"max_ms"`
	MeanMs float64 `json:"mean_ms"`
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func (s *opStats) report(name string) OpReport {
	h := &s.hist
	return OpReport{Name: name, Count: h.Count(), Errors: s.errors.Load(),
		P50ms: ms(h.Quantile(0.50)), P95ms: ms(h.Quantile(0.95)), P99ms: ms(h.Quantile(0.99)),
		MaxMs: ms(h.Max()), MeanMs: ms(h.Mean())}
}

// Snapshot returns the overall report, per-op reports sorted by name and up to a few error samples.
func (r *Recorder) Snapshot() (OpReport, []OpReport, []string) {
	r.mu.RLock()
	names := make([]string, 0, len(r.ops))
	for n := range r.ops {
		names = append(names, n)
	}
	r.mu.RUnlock()
	sort.Strings(names)
	ops := make([]OpReport, 0, len(names))
	for _, n := range names {
		ops = append(ops, r.op(n).report(n))
	}
	r.overall.mu.Lock()
	samples := append([]string(nil), r.overall.samples...)
	r.overall.mu.Unlock()
	return r.overall.report("overall"), ops, samples
}

type HeapReport struct {
	StartMB float64 `json:"live_start_mb"`
	EndMB   float64 `json:"live_end_mb"`
	MaxMB   float64 `json:"live_max_mb"`
}

func sampleHeap(ctx context.Context, from time.Time) func() HeapReport {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan HeapReport, 1)
	go func() {
		var rep HeapReport
		started := false
		sample := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				done <- rep
				return
			case now := <-t.C:
				metrics.Read(sample)
				mb := float64(sample[0].Value.Uint64()) / (1 << 20)
				if !started && !now.Before(from) {
					rep.StartMB, started = mb, true
				}
				rep.EndMB, rep.MaxMB = mb, max(rep.MaxMB, mb)
			}
		}
	}()
	return func() HeapReport {
		cancel()
		return <-done
	}
}
