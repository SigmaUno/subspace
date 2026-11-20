// Package ratelimit provides a concurrency-safe, keyed token-bucket limiter.
// Keys are typically "provider/model" (see Key).
package ratelimit

import (
	"context"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Limit configures one bucket. PerMinute <= 0 means unlimited.
// Burst is the bucket size; values below 1 are treated as 1.
type Limit struct {
	PerMinute float64
	Burst     int
}

// Options configures a Limiter.
type Options struct {
	// Default applies to keys without a more specific limit.
	Default Limit
	// Limits holds per-key overrides. A key of the form "provider/*"
	// matches every model of that provider.
	Limits map[string]Limit
	// IdleTTL is how long a fully refilled bucket may sit unused before it
	// is evicted. Zero means DefaultIdleTTL; negative disables eviction.
	IdleTTL time.Duration
	// Clock overrides the time source (use NewFakeClock in tests).
	Clock Clock
}

// DefaultIdleTTL is the idle eviction time used when Options.IdleTTL is zero.
const DefaultIdleTTL = 10 * time.Minute

// Key builds the limiter key for a provider and model.
func Key(provider, model string) string { return provider + "/" + model }

type bucket struct {
	mu     sync.Mutex
	limit  Limit
	tokens float64
	last   time.Time // last refill
	used   time.Time // last Allow/Reserve/Wait, for idle eviction
	dead   bool
}

// Limiter is a set of token buckets addressed by key. Buckets are created
// lazily and evicted once idle. The zero value is not usable; use New.
type Limiter struct {
	clock    Clock
	def      Limit
	idleTTL  time.Duration
	sweepGap time.Duration

	mu      sync.RWMutex
	limits  map[string]Limit
	buckets map[string]*bucket

	nextSweep atomic.Int64 // unix nanos on the limiter clock
}

// New returns a Limiter.
func New(o Options) *Limiter {
	l := &Limiter{
		clock:   o.Clock,
		def:     normalize(o.Default),
		idleTTL: o.IdleTTL,
		limits:  make(map[string]Limit, len(o.Limits)),
		buckets: make(map[string]*bucket),
	}
	if l.clock == nil {
		l.clock = realClock{}
	}
	if l.idleTTL == 0 {
		l.idleTTL = DefaultIdleTTL
	}
	if l.idleTTL > 0 {
		l.sweepGap = l.idleTTL / 2
		if l.sweepGap <= 0 {
			l.sweepGap = l.idleTTL
		}
		l.nextSweep.Store(l.clock.Now().Add(l.sweepGap).UnixNano())
	}
	for k, v := range o.Limits {
		l.limits[k] = normalize(v)
	}
	return l
}

func normalize(l Limit) Limit {
	if l.Burst < 1 {
		l.Burst = 1
	}
	if l.PerMinute < 0 || math.IsNaN(l.PerMinute) {
		l.PerMinute = 0
	}
	return l
}

// SetLimit changes the limit for key. An existing bucket keeps its tokens
// (clamped to the new burst) and adopts the new rate.
func (l *Limiter) SetLimit(key string, lim Limit) {
	lim = normalize(lim)
	l.mu.Lock()
	l.limits[key] = lim
	b := l.buckets[key]
	l.mu.Unlock()
	if b == nil {
		return
	}
	now := l.clock.Now()
	b.mu.Lock()
	b.advance(now)
	b.limit = lim
	b.tokens = math.Min(b.tokens, float64(lim.Burst))
	b.mu.Unlock()
}

// Len returns the number of live buckets.
func (l *Limiter) Len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.buckets)
}

// Allow takes one token if available and reports whether it did.
func (l *Limiter) Allow(key string) bool {
	ok := false
	l.with(key, func(b *bucket, _ time.Time) {
		switch {
		case b.limit.PerMinute <= 0:
			ok = true
		case b.tokens >= 1:
			b.tokens--
			ok = true
		}
	})
	return ok
}

// Reserve takes one token unconditionally and returns how long the caller
// must wait before using it (0 when a token was available). The token is
// consumed even when the delay is non-zero.
func (l *Limiter) Reserve(key string) time.Duration {
	var d time.Duration
	l.with(key, func(b *bucket, _ time.Time) { d = b.reserve() })
	return d
}

// Wait blocks until a token is available for key or ctx is done. If the
// context ends first, the reserved token is returned to the bucket and the
// context error is returned.
func (l *Limiter) Wait(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var d time.Duration
	l.with(key, func(b *bucket, now time.Time) {
		d = b.reserve()
		if dl, ok := ctx.Deadline(); ok && d > 0 && now.Add(d).After(dl) {
			// Cannot be served in time; hand the token back and fail fast.
			b.refund()
			d = -1
		}
	})
	switch {
	case d == 0:
		return nil
	case d < 0:
		return context.DeadlineExceeded
	}
	ch, stop := l.clock.Timer(d)
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		stop()
		l.with(key, func(b *bucket, _ time.Time) { b.refund() })
		return ctx.Err()
	}
}

// Sweep evicts idle buckets now and returns how many were removed.
func (l *Limiter) Sweep() int {
	if l.idleTTL < 0 {
		return 0
	}
	now := l.clock.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for k, b := range l.buckets {
		b.mu.Lock()
		if now.Sub(b.used) >= l.idleTTL && b.refilled(now) {
			b.dead = true
			delete(l.buckets, k)
			n++
		}
		b.mu.Unlock()
	}
	return n
}

// with runs fn on the live bucket for key under its lock. A bucket evicted
// between lookup and lock is skipped and the lookup retried.
func (l *Limiter) with(key string, fn func(b *bucket, now time.Time)) {
	for {
		now := l.clock.Now()
		l.maybeSweep(now)
		b := l.get(key, now)
		b.mu.Lock()
		if b.dead {
			b.mu.Unlock()
			continue
		}
		b.advance(now)
		b.used = now
		fn(b, now)
		b.mu.Unlock()
		return
	}
}

func (l *Limiter) get(key string, now time.Time) *bucket {
	l.mu.RLock()
	b := l.buckets[key]
	l.mu.RUnlock()
	if b != nil {
		return b
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if b = l.buckets[key]; b != nil {
		return b
	}
	lim := l.lookup(key)
	b = &bucket{limit: lim, tokens: float64(lim.Burst), last: now, used: now}
	l.buckets[key] = b
	return b
}

// lookup resolves a limit; callers hold l.mu.
func (l *Limiter) lookup(key string) Limit {
	if lim, ok := l.limits[key]; ok {
		return lim
	}
	if i := strings.IndexByte(key, '/'); i >= 0 {
		if lim, ok := l.limits[key[:i]+"/*"]; ok {
			return lim
		}
	}
	return l.def
}

func (l *Limiter) maybeSweep(now time.Time) {
	if l.idleTTL < 0 {
		return
	}
	next := l.nextSweep.Load()
	if now.UnixNano() < next {
		return
	}
	if l.nextSweep.CompareAndSwap(next, now.Add(l.sweepGap).UnixNano()) {
		l.Sweep()
	}
}

// advance refills the bucket up to now. Caller holds b.mu.
func (b *bucket) advance(now time.Time) {
	elapsed := now.Sub(b.last)
	if elapsed <= 0 {
		return
	}
	b.last = now
	if b.limit.PerMinute <= 0 {
		b.tokens = float64(b.limit.Burst)
		return
	}
	b.tokens = math.Min(float64(b.limit.Burst), b.tokens+elapsed.Minutes()*b.limit.PerMinute)
}

// refilled reports whether the bucket would be full at now. Caller holds b.mu.
func (b *bucket) refilled(now time.Time) bool {
	if b.limit.PerMinute <= 0 {
		return true
	}
	t := b.tokens + now.Sub(b.last).Minutes()*b.limit.PerMinute
	return t >= float64(b.limit.Burst)
}

func (b *bucket) reserve() time.Duration {
	if b.limit.PerMinute <= 0 {
		return 0
	}
	b.tokens--
	if b.tokens >= 0 {
		return 0
	}
	secs := -b.tokens * 60 / b.limit.PerMinute
	return time.Duration(math.Ceil(secs * float64(time.Second)))
}

func (b *bucket) refund() {
	if b.limit.PerMinute <= 0 {
		return
	}
	b.tokens = math.Min(float64(b.limit.Burst), b.tokens+1)
}
