package audit

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

func TestLoopGuardMatchesWindowOracle(t *testing.T) {
	for seed := int64(1); seed <= 500; seed++ {
		r := rand.New(rand.NewSource(seed))
		max := 1 + r.Intn(8)
		window := time.Duration(1+r.Intn(20)) * time.Minute
		g := NewLoopGuard(max, window)

		seen := map[[3]string][]time.Time{}
		now := t0
		for i := 0; i < 300; i++ {
			now = now.Add(time.Duration(r.Int63n(int64(window / 3))))
			key := [3]string{fmt.Sprint("agent", r.Intn(3)), fmt.Sprint("tool", r.Intn(2)), fmt.Sprint("args", r.Intn(2))}

			inWindow := 1
			for _, at := range seen[key] {
				if now.Sub(at) <= window {
					inWindow++
				}
			}
			seen[key] = append(seen[key], now)

			want := inWindow > max
			if got := g.Hit(key[0], key[1], key[2], now); got != want {
				t.Fatalf("seed %d call %d: Hit(%v) = %v, want %v (%d calls in %v, max %d)", seed, i, key, got, want, inWindow, window, max)
			}
		}
	}
}

func TestLoopGuardTripsExactlyAfterMax(t *testing.T) {
	for seed := int64(1); seed <= 500; seed++ {
		r := rand.New(rand.NewSource(seed))
		max := 1 + r.Intn(20)
		window := time.Duration(1+r.Intn(30)) * time.Minute
		g := NewLoopGuard(max, window)

		now := t0
		for i := 1; i <= max+3; i++ {
			now = now.Add(window / time.Duration(2*(max+3)))
			if got, want := g.Hit("a", "tool", "h", now), i > max; got != want {
				t.Fatalf("seed %d: call %d of max %d: tripped=%v, want %v", seed, i, max, got, want)
			}
		}

		now = now.Add(window + time.Second)
		if g.Hit("a", "tool", "h", now) {
			t.Fatalf("seed %d: guard still tripped after the window expired", seed)
		}
	}
}
