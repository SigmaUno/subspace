package audit

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func TestSixthCallTrips(t *testing.T) {
	g := NewLoopGuard(DefaultMaxRepeats, DefaultWindow)
	for i := 1; i <= 5; i++ {
		if g.Hit("a", "add_note", "h", t0.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("call %d should not trip", i)
		}
	}
	if !g.Hit("a", "add_note", "h", t0.Add(6*time.Second)) {
		t.Fatal("6th call should trip")
	}
}

func TestWindowExpiry(t *testing.T) {
	g := NewLoopGuard(5, 10*time.Minute)
	for i := 0; i < 5; i++ {
		g.Hit("a", "t", "h", t0.Add(time.Duration(i)*time.Minute))
	}

	if g.Hit("a", "t", "h", t0.Add(10*time.Minute+30*time.Second)) {
		t.Fatal("should not trip once oldest call left window")
	}

	if g.Hit("a", "t", "h", t0.Add(11*time.Minute+30*time.Second)) {
		t.Fatal("unexpected trip")
	}
	if !g.Hit("a", "t", "h", t0.Add(11*time.Minute+31*time.Second)) {
		t.Fatal("expected trip")
	}

	if g.Hit("a", "t", "h", t0.Add(time.Hour)) {
		t.Fatal("should reset after window")
	}
}

func TestIsolation(t *testing.T) {
	g := NewLoopGuard(2, time.Minute)
	g.Hit("a", "t", "h", t0)
	g.Hit("a", "t", "h", t0)
	if g.Hit("b", "t", "h", t0) {
		t.Fatal("other agent affected")
	}
	if g.Hit("a", "u", "h", t0) {
		t.Fatal("other tool affected")
	}
	if g.Hit("a", "t", "h2", t0) {
		t.Fatal("other args affected")
	}
	if !g.Hit("a", "t", "h", t0) {
		t.Fatal("expected trip")
	}
}

func TestBoundedMemory(t *testing.T) {
	g := NewLoopGuard(5, time.Hour).(*guard)
	for i := 0; i < 10000; i++ {
		g.Hit("a", "t", "same", t0.Add(time.Duration(i)*time.Millisecond))
	}
	if n := len(g.agents["a"][callKey{"t", "same"}]); n > 6 {
		t.Fatalf("timestamps not bounded: %d", n)
	}
	for i := 0; i < 5000; i++ {
		g.Hit("a", "t", fmt.Sprint("k", i), t0.Add(time.Second))
	}
	if n := len(g.agents["a"]); n > maxKeysPerAgent {
		t.Fatalf("keys not bounded: %d", n)
	}
}

func TestSweepRemovesExpired(t *testing.T) {
	g := NewLoopGuard(5, time.Minute).(*guard)
	for i := 0; i < 50; i++ {
		g.Hit(fmt.Sprint("agent", i), "t", "h", t0)
	}
	g.Hit("x", "t", "h", t0.Add(5*time.Minute))
	if len(g.agents) != 1 {
		t.Fatalf("expected expired agents swept, have %d", len(g.agents))
	}
}

func TestEvictionKeepsLoopingKey(t *testing.T) {
	g := NewLoopGuard(5, time.Hour).(*guard)
	for i := 0; i < 5; i++ {
		g.Hit("a", "t", "loop", t0.Add(time.Duration(i)*time.Second))
	}
	for i := 0; i < maxKeysPerAgent+10; i++ {
		g.Hit("a", "t", fmt.Sprint("n", i), t0.Add(time.Minute+time.Duration(i)*time.Millisecond))
	}
	g.Hit("a", "t", "loop", t0.Add(2*time.Minute))
	for i := 0; i < maxKeysPerAgent-1; i++ {
		g.Hit("a", "t", fmt.Sprint("m", i), t0.Add(3*time.Minute))
	}
	if len(g.agents["a"]) > maxKeysPerAgent {
		t.Fatal("keys exceeded cap")
	}
}

func TestConcurrent(t *testing.T) {
	g := NewLoopGuard(5, time.Minute)
	var wg sync.WaitGroup
	var mu sync.Mutex
	trips := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if g.Hit("a", "t", "h", t0) {
				mu.Lock()
				trips++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if trips != 45 {
		t.Fatalf("trips=%d want 45", trips)
	}
}

func TestArgsHash(t *testing.T) {
	a := ArgsHash(json.RawMessage(`{"b":1,"a":{"y":[1,2],"x":"s"}}`))
	b := ArgsHash(json.RawMessage(" {\n \"a\": {\"x\":\"s\",\"y\":[1,2]}, \"b\": 1 } "))
	if a != b {
		t.Fatalf("key order/whitespace should not matter: %s %s", a, b)
	}
	if a == ArgsHash(json.RawMessage(`{"b":2,"a":{"y":[1,2],"x":"s"}}`)) {
		t.Fatal("different values must differ")
	}
	if ArgsHash(json.RawMessage(`[1,2]`)) == ArgsHash(json.RawMessage(`[2,1]`)) {
		t.Fatal("array order matters")
	}
	if ArgsHash(json.RawMessage(`{"n":12345678901234567890}`)) == ArgsHash(json.RawMessage(`{"n":12345678901234567891}`)) {
		t.Fatal("big numbers must keep precision")
	}

	for _, raw := range []string{``, `not json`, `{"a":1} x`} {
		h1 := ArgsHash(json.RawMessage(raw))
		if h1 != ArgsHash(json.RawMessage(raw)) {
			t.Fatalf("unstable for %q", raw)
		}
	}
	if ArgsHash(nil) == ArgsHash(json.RawMessage(`{}`)) {
		t.Fatal("empty and {} should differ")
	}
}
