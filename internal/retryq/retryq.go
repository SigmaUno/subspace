package retryq

import (
	"context"
	"sync"
	"time"
)

type Timer interface {
	Timer(d time.Duration) (<-chan time.Time, func() bool)
}

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

func (SystemClock) Timer(d time.Duration) (<-chan time.Time, func() bool) {
	t := time.NewTimer(d)
	return t.C, t.Stop
}

func Exp(base, limit time.Duration, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt >= 32 || base<<(attempt-1) >= limit || base<<(attempt-1) <= 0 {
		return limit
	}
	return base << (attempt - 1)
}

func HalfJitter(d time.Duration, n func(int64) int64) time.Duration {
	half := d / 2
	return half + time.Duration(n(int64(half)+1))
}

func Spread(d time.Duration, f float64) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*f))
}

func Sleep(ctx context.Context, c Timer, d time.Duration) error {
	ch, stop := c.Timer(d)
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		stop()
		return ctx.Err()
	}
}

type Keyed[V any] struct {
	max  int
	wake chan struct{}

	mu       sync.Mutex
	pending  map[string]V
	order    []string
	inflight map[string]bool
	dropped  int64
}

func NewKeyed[V any](max int) *Keyed[V] {
	return &Keyed[V]{max: max, wake: make(chan struct{}, 1), pending: map[string]V{}, inflight: map[string]bool{}}
}

func (q *Keyed[V]) Push(key string, v V) {
	q.mu.Lock()
	if _, queued := q.pending[key]; !queued {
		if len(q.order) >= q.max {
			delete(q.pending, q.order[0])
			q.order = q.order[1:]
			q.dropped++
		}
		q.order = append(q.order, key)
	}
	q.pending[key] = v
	q.mu.Unlock()
	q.signal()
}

func (q *Keyed[V]) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *Keyed[V]) take() (string, V, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, key := range q.order {
		if q.inflight[key] {
			continue
		}
		q.order = append(q.order[:i], q.order[i+1:]...)
		v := q.pending[key]
		delete(q.pending, key)
		q.inflight[key] = true
		return key, v, true
	}
	var zero V
	return "", zero, false
}

func (q *Keyed[V]) Next(ctx context.Context) (string, V, bool) {
	for {
		if ctx.Err() != nil {
			var zero V
			return "", zero, false
		}
		if key, v, ok := q.take(); ok {
			return key, v, true
		}
		select {
		case <-ctx.Done():
		case <-q.wake:
		}
	}
}

func (q *Keyed[V]) Done(key string) {
	q.mu.Lock()
	delete(q.inflight, key)
	more := len(q.order) > 0
	q.mu.Unlock()
	if more {
		q.signal()
	}
}

func (q *Keyed[V]) Queued(key string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.pending[key]
	return ok
}

func (q *Keyed[V]) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.order)
}

func (q *Keyed[V]) Dropped() int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.dropped
}
