package retryq

import (
	"context"
	"errors"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/ratelimit"
)

func TestExpSchedule(t *testing.T) {
	var got []time.Duration
	for a := 1; a <= 8; a++ {
		got = append(got, Exp(time.Second, 30*time.Second, a))
	}
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30, 30}
	for i := range want {
		if got[i] != want[i]*time.Second {
			t.Fatalf("schedule %v", got)
		}
	}
}

func TestExpIsMonotonicAndCapped(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 5))
	for range 5000 {
		base := time.Duration(1 + r.Int64N(int64(time.Hour)))
		limit := time.Duration(1 + r.Int64N(int64(1000*time.Hour)))
		prev := time.Duration(0)
		for a := 1; a <= 100; a++ {
			d := Exp(base, limit, a)
			if d <= 0 || d > limit || d < prev {
				t.Fatalf("Exp(%v, %v, %d) = %v after %v", base, limit, a, d, prev)
			}
			if a == 1 && d != min(base, limit) {
				t.Fatalf("first attempt %v", d)
			}
			prev = d
		}
		if prev != limit {
			t.Fatalf("never reached cap %v: %v", limit, prev)
		}
	}
}

func TestJitterBounds(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 9))
	for range 5000 {
		d := time.Duration(r.Int64N(int64(time.Hour)))
		lo := HalfJitter(d, func(int64) int64 { return 0 })
		hi := HalfJitter(d, func(n int64) int64 { return n - 1 })
		mid := HalfJitter(d, r.Int64N)
		if lo != d/2 || hi != d/2+d/2 || mid < lo || mid > hi {
			t.Fatalf("d=%v lo=%v hi=%v mid=%v", d, lo, hi, mid)
		}
		s := Spread(d, r.Float64())
		if s < Spread(d, 0) || s > Spread(d, 1) || Spread(d, 0.5) != d {
			t.Fatalf("Spread(%v) = %v", d, s)
		}
	}
}

func TestSleepUsesInjectedClock(t *testing.T) {
	clk := ratelimit.NewFakeClock(time.Unix(0, 0))
	done := make(chan error, 1)
	go func() { done <- Sleep(context.Background(), clk, 5*time.Second) }()
	for clk.Pending() == 0 {
		time.Sleep(time.Millisecond)
	}
	clk.Advance(4 * time.Second)
	select {
	case err := <-done:
		t.Fatalf("woke early: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	clk.Advance(time.Second)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSleepShutdownReleasesTimer(t *testing.T) {
	clk := ratelimit.NewFakeClock(time.Unix(0, 0))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Sleep(ctx, clk, time.Hour) }()
	for clk.Pending() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if clk.Pending() != 0 {
		t.Fatalf("%d timers leaked", clk.Pending())
	}
}
