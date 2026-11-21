package retryq

import (
	"context"
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
	if attempt >= 32 || base <= 0 || base > limit>>(attempt-1) || base<<(attempt-1) >= limit {
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
