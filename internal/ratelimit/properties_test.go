package ratelimit

import (
	"math/rand"
	"testing"
	"time"
)

const seeds = 300

func TestNeverExceedsRateOverAnyWindow(t *testing.T) {
	start := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for seed := int64(1); seed <= seeds; seed++ {
		r := rand.New(rand.NewSource(seed))
		lim := Limit{PerMinute: float64(1 + r.Intn(600)), Burst: 1 + r.Intn(10)}
		clock := NewFakeClock(start)
		l := New(Options{Default: lim, Clock: clock})

		var allowed []time.Duration
		var elapsed time.Duration
		for i := 0; i < 400; i++ {
			switch r.Intn(4) {
			case 0:
				clock.Advance(time.Duration(r.Int63n(int64(10 * time.Second))))
			case 1:
				clock.Advance(time.Duration(r.Int63n(int64(5 * time.Minute))))
			}
			elapsed = clock.Now().Sub(start)
			if l.Allow("k") {
				allowed = append(allowed, elapsed)
			}
		}

		for i := range allowed {
			for j := i; j < len(allowed); j++ {
				count := float64(j - i + 1)
				bound := float64(lim.Burst) + lim.PerMinute*(allowed[j]-allowed[i]).Minutes()
				if count > bound+1e-6 {
					t.Fatalf("seed %d: %v allowed in [%v, %v] with %+v, bound %.3f", seed, count, allowed[i], allowed[j], lim, bound)
				}
			}
		}
	}
}

func TestRefillsFullBurstAfterIdle(t *testing.T) {
	start := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for seed := int64(1); seed <= seeds; seed++ {
		r := rand.New(rand.NewSource(seed))
		lim := Limit{PerMinute: float64(1 + r.Intn(600)), Burst: 1 + r.Intn(10)}
		clock := NewFakeClock(start)
		l := New(Options{Default: lim, Clock: clock, IdleTTL: -1})

		for round := 0; round < 3; round++ {
			for i := 0; i < lim.Burst; i++ {
				if !l.Allow("k") {
					t.Fatalf("seed %d round %d: call %d of burst %d denied with %+v", seed, round, i+1, lim.Burst, lim)
				}
			}
			if l.Allow("k") {
				t.Fatalf("seed %d round %d: call beyond burst allowed with %+v", seed, round, lim)
			}
			perToken := time.Duration(float64(time.Minute) / lim.PerMinute)
			clock.Advance(perToken - 2*time.Millisecond)
			if l.Allow("k") {
				t.Fatalf("seed %d round %d: token granted before its refill time with %+v", seed, round, lim)
			}
			clock.Advance(time.Duration(lim.Burst)*perToken + time.Second)
		}
	}
}
