package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func newTest(o Options) (*Limiter, *FakeClock) {
	c := NewFakeClock(t0)
	o.Clock = c
	return New(o), c
}

func TestAllowBurstAndRefill(t *testing.T) {
	l, c := newTest(Options{Default: Limit{PerMinute: 60, Burst: 3}})
	for i := 0; i < 3; i++ {
		if !l.Allow("a/m") {
			t.Fatalf("burst call %d denied", i)
		}
	}
	if l.Allow("a/m") {
		t.Fatal("4th call should be denied")
	}
	c.Advance(999 * time.Millisecond)
	if l.Allow("a/m") {
		t.Fatal("token should not be available yet")
	}
	c.Advance(time.Millisecond)
	if !l.Allow("a/m") {
		t.Fatal("token should have refilled after 1s")
	}
	c.Advance(time.Hour)
	for i := 0; i < 3; i++ {
		if !l.Allow("a/m") {
			t.Fatalf("refilled burst call %d denied", i)
		}
	}
	if l.Allow("a/m") {
		t.Fatal("refill must cap at burst")
	}
}

func TestKeysAreIndependent(t *testing.T) {
	l, _ := newTest(Options{Default: Limit{PerMinute: 1, Burst: 1}})
	if !l.Allow(Key("anthropic", "x")) || !l.Allow(Key("openai", "x")) || !l.Allow(Key("anthropic", "y")) {
		t.Fatal("first call per key must pass")
	}
	if l.Allow(Key("anthropic", "x")) {
		t.Fatal("second call on same key must fail")
	}
}

func TestPerKeyLimits(t *testing.T) {
	l, _ := newTest(Options{
		Default: Limit{PerMinute: 60, Burst: 1},
		Limits: map[string]Limit{
			"anthropic/opus": {PerMinute: 60, Burst: 5},
			"openai/*":       {PerMinute: 60, Burst: 2},
		},
	})
	count := func(key string) int {
		n := 0
		for l.Allow(key) {
			n++
		}
		return n
	}
	if n := count("anthropic/opus"); n != 5 {
		t.Fatalf("exact override: got %d", n)
	}
	if n := count("openai/gpt"); n != 2 {
		t.Fatalf("wildcard override: got %d", n)
	}
	if n := count("deepseek/chat"); n != 1 {
		t.Fatalf("default: got %d", n)
	}
	l.SetLimit("deepseek/chat", Limit{PerMinute: 60, Burst: 4})
	if n := count("deepseek/chat"); n != 0 {
		t.Fatalf("SetLimit must keep consumed tokens, got %d", n)
	}
	l.SetLimit("deepseek/new", Limit{PerMinute: 60, Burst: 4})
	if n := count("deepseek/new"); n != 4 {
		t.Fatalf("SetLimit before first use: got %d", n)
	}
}

func TestUnlimited(t *testing.T) {
	l, _ := newTest(Options{Default: Limit{}})
	for i := 0; i < 1000; i++ {
		if !l.Allow("k") || l.Reserve("k") != 0 {
			t.Fatal("PerMinute <= 0 must never limit")
		}
	}
}

func TestReserve(t *testing.T) {
	l, c := newTest(Options{Default: Limit{PerMinute: 60, Burst: 2}})
	if d := l.Reserve("k"); d != 0 {
		t.Fatalf("got %v", d)
	}
	if d := l.Reserve("k"); d != 0 {
		t.Fatalf("got %v", d)
	}
	if d := l.Reserve("k"); d != time.Second {
		t.Fatalf("3rd reserve delay = %v, want 1s", d)
	}
	if d := l.Reserve("k"); d != 2*time.Second {
		t.Fatalf("4th reserve delay = %v, want 2s", d)
	}
	c.Advance(2 * time.Second)
	if d := l.Reserve("k"); d != time.Second {
		t.Fatalf("after advance delay = %v, want 1s", d)
	}
	if l.Allow("k") {
		t.Fatal("Allow must fail while reservations are outstanding")
	}
}

func waitAsync(l *Limiter, ctx context.Context, key string) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- l.Wait(ctx, key) }()
	return ch
}

func waitForTimers(t *testing.T, c *FakeClock, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.Pending() < n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d timers (have %d)", n, c.Pending())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWait(t *testing.T) {
	l, c := newTest(Options{Default: Limit{PerMinute: 60, Burst: 1}})
	ctx := context.Background()
	if err := l.Wait(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	res := waitAsync(l, ctx, "k")
	waitForTimers(t, c, 1)
	select {
	case err := <-res:
		t.Fatalf("Wait returned early: %v", err)
	default:
	}
	c.Advance(time.Second)
	if err := <-res; err != nil {
		t.Fatal(err)
	}
}

func TestWaitContextCancelRefunds(t *testing.T) {
	l, c := newTest(Options{Default: Limit{PerMinute: 60, Burst: 1}})
	l.Allow("k")
	ctx, cancel := context.WithCancel(context.Background())
	res := waitAsync(l, ctx, "k")
	waitForTimers(t, c, 1)
	cancel()
	if err := <-res; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if c.Pending() != 0 {
		t.Fatal("timer not released")
	}
	// The cancelled waiter's token was refunded, so one more second suffices.
	c.Advance(time.Second)
	if !l.Allow("k") {
		t.Fatal("refund missing")
	}
	if l.Allow("k") {
		t.Fatal("only one token expected")
	}
}

func TestWaitDeadlineTooSoon(t *testing.T) {
	l, c := newTest(Options{Default: Limit{PerMinute: 60, Burst: 1}})
	l.Allow("k")
	ctx, cancel := context.WithDeadline(context.Background(), t0.Add(500*time.Millisecond))
	defer cancel()
	if err := l.Wait(ctx, "k"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	c.Advance(time.Second)
	if !l.Allow("k") || l.Allow("k") {
		t.Fatal("failed Wait must not consume a token")
	}
}

func TestWaitCancelledContext(t *testing.T) {
	l, _ := newTest(Options{Default: Limit{PerMinute: 60, Burst: 1}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Wait(ctx, "k"); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if !l.Allow("k") {
		t.Fatal("cancelled Wait must not consume a token")
	}
}

func TestIdleEviction(t *testing.T) {
	l, c := newTest(Options{Default: Limit{PerMinute: 60, Burst: 2}, IdleTTL: time.Minute})
	for i := 0; i < 10; i++ {
		l.Allow(fmt.Sprintf("p/m%d", i))
	}
	if l.Len() != 10 {
		t.Fatalf("len = %d", l.Len())
	}
	if n := l.Sweep(); n != 0 {
		t.Fatalf("nothing idle yet, swept %d", n)
	}
	c.Advance(2 * time.Minute)
	l.Allow("p/m0") // keep one fresh; lazy sweep also fires here
	if l.Len() != 1 {
		t.Fatalf("len after eviction = %d, want 1", l.Len())
	}
	for i := 0; i < 2; i++ {
		if !l.Allow("p/m1") {
			t.Fatal("evicted bucket must restart full")
		}
	}
	if l.Allow("p/m1") {
		t.Fatal("burst exceeded after restart")
	}
}

func TestIdleEvictionKeepsDrainedBuckets(t *testing.T) {
	// Burst 1 at 1/hour: after 2 min idle the bucket is still empty, so it
	// must survive eviction or the limit would be bypassed.
	l, c := newTest(Options{Default: Limit{PerMinute: 1.0 / 60, Burst: 1}, IdleTTL: time.Minute})
	l.Allow("k")
	c.Advance(2 * time.Minute)
	if n := l.Sweep(); n != 0 {
		t.Fatalf("drained bucket evicted (%d)", n)
	}
	if l.Allow("k") {
		t.Fatal("limit bypassed")
	}
}

func TestEvictionDisabled(t *testing.T) {
	l, c := newTest(Options{Default: Limit{PerMinute: 60, Burst: 1}, IdleTTL: -1})
	l.Allow("k")
	c.Advance(24 * time.Hour)
	l.Allow("other")
	if l.Len() != 2 || l.Sweep() != 0 {
		t.Fatal("eviction should be disabled")
	}
}

func TestConcurrentExactBudget(t *testing.T) {
	l, _ := newTest(Options{Default: Limit{PerMinute: 1, Burst: 100}})
	var granted atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < 300; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				if l.Allow("k") {
					granted.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if granted.Load() != 100 {
		t.Fatalf("granted %d, want exactly 100", granted.Load())
	}
}

func TestConcurrentWithEviction(t *testing.T) {
	// Real clock, tiny TTL: exercises the evict/retry path under -race.
	l := New(Options{Default: Limit{PerMinute: 6000000, Burst: 1000}, IdleTTL: time.Millisecond})
	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				l.Allow(fmt.Sprintf("p/m%d", (g+i)%10))
				if i%50 == 0 {
					l.Sweep()
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestRealClockWait(t *testing.T) {
	l := New(Options{Default: Limit{PerMinute: 6000, Burst: 1}}) // 10ms per token
	ctx := context.Background()
	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := l.Wait(ctx, "k"); err != nil {
			t.Fatal(err)
		}
	}
	if el := time.Since(start); el < 15*time.Millisecond {
		t.Fatalf("3 waits finished too fast: %v", el)
	}
}

// BenchmarkAllow300x10 runs 300 goroutines spread over 10 provider/model keys.
func BenchmarkAllow300x10(b *testing.B) {
	benchmark(b, func(l *Limiter, k string) { l.Allow(k) })
}

func BenchmarkReserve300x10(b *testing.B) {
	benchmark(b, func(l *Limiter, k string) { l.Reserve(k) })
}

func benchmark(b *testing.B, op func(*Limiter, string)) {
	const goroutines, nkeys = 300, 10
	keys := make([]string, nkeys)
	for i := range keys {
		keys[i] = Key("provider", fmt.Sprintf("model-%d", i))
	}
	l := New(Options{Default: Limit{PerMinute: 600, Burst: 100}})
	per := b.N/goroutines + 1
	var wg sync.WaitGroup
	b.ReportAllocs()
	b.ResetTimer()
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				op(l, keys[(g+i)%nkeys])
			}
		}(g)
	}
	wg.Wait()
}

func TestSetDefaultRetunesLiveBucketsButNotExplicitLimits(t *testing.T) {
	l, c := newTest(Options{Default: Limit{PerMinute: 60, Burst: 5}, Limits: map[string]Limit{"vip/*": {PerMinute: 60, Burst: 5}}})
	for i := 0; i < 5; i++ {
		l.Allow("a/m")
		l.Allow("vip/m")
	}
	l.SetDefault(Limit{PerMinute: 6, Burst: 2})
	c.Advance(time.Second)
	if l.Allow("a/m") {
		t.Fatal("default bucket kept its old refill rate")
	}
	c.Advance(10 * time.Second)
	if !l.Allow("a/m") || l.Allow("a/m") {
		t.Fatal("default bucket should refill one token per 10s")
	}
	if !l.Allow("vip/m") {
		t.Fatal("explicit limit was retuned")
	}
	l.SetDefault(Limit{})
	for i := 0; i < 100; i++ {
		if !l.Allow("a/m") {
			t.Fatal("unlimited default denied a call")
		}
	}
}
