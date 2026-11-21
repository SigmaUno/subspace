package dst

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	seedsFile  = "testdata/seeds.txt"
	freshSeeds = 50
	freshSteps = 200
)

func runSeed(t *testing.T, r *Runner, seed int64, steps int) {
	t.Helper()
	ctx := context.Background()
	o, err := r.Run(ctx, seed, steps)
	if err != nil {
		t.Fatal(err)
	}
	if !o.Failed() {
		return
	}
	if s, err := r.Minimize(ctx, o, 300); err == nil {
		o = s
	}
	var b strings.Builder
	o.WriteTrace(&b)
	t.Fatalf("seed %d: %s\nreplay: go run ./cmd/subspace-dst -seeds 1 -start %d -steps %d\n%s", seed, o.Violation, seed, steps, b.String())
}

func TestSeeds(t *testing.T) {
	regs, err := LoadRegressions(seedsFile)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UnixNano() % 1_000_000_000
	n := freshSeeds
	if testing.Short() {
		n = 8
	}
	for i := range n {
		regs = append(regs, Regression{Seed: start + int64(i), Steps: freshSteps})
	}
	r := &Runner{Factory: NewBoardSystem, Dir: t.TempDir()}
	for _, reg := range regs {
		t.Run(fmt.Sprint(reg.Seed), func(t *testing.T) {
			t.Parallel()
			runSeed(t, r, reg.Seed, reg.Steps)
		})
	}
}

func TestDeterministic(t *testing.T) {
	r := &Runner{Factory: NewBoardSystem, Dir: t.TempDir(), Serial: true}
	trace := func() string {
		o, err := r.Run(context.Background(), 7, 200)
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		o.WriteTrace(&b)
		return b.String()
	}
	if a, b := trace(), trace(); a != b {
		t.Fatalf("seed 7 is not reproducible:\n%s\n---\n%s", a, b)
	}
}

type faultySweep struct {
	System
	clock *Clock
	early bool
}

func (f *faultySweep) Do(ctx context.Context, op Op) Result {
	if op.Kind != OpSweep {
		return f.System.Do(ctx, op)
	}
	if !f.early {
		return Result{}
	}
	f.clock.Advance(time.Minute)
	defer f.clock.Advance(-time.Minute)
	return f.System.Do(ctx, op)
}

type staleTokens struct {
	System
	current map[string]int
}

func (s *staleTokens) Do(ctx context.Context, op Op) Result {
	if op.Authenticated() {
		op.Token = s.current[op.Actor]
	}
	res := s.System.Do(ctx, op)
	if op.Kind == OpRotate && res.Err == nil {
		s.current[op.Target]++
	}
	return res
}

func TestDetectsInjectedBugs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		wrap   func(System, *Clock) System
		inv    string
		shrink bool
	}{
		{"sweep_ignores_expiry", func(s System, c *Clock) System { return &faultySweep{System: s, clock: c} }, InvLeaseExpiry, true},
		{"sweep_revokes_early", func(s System, c *Clock) System { return &faultySweep{System: s, clock: c, early: true} }, InvLeaseHonored, false},
		{"rotation_keeps_old_token", func(s System, _ *Clock) System { return &staleTokens{System: s, current: map[string]int{}} }, InvToken, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &Runner{Dir: t.TempDir(), Serial: true, Factory: func(dir string, c *Clock, lease time.Duration) (System, error) {
				s, err := NewBoardSystem(dir, c, lease)
				if err != nil {
					return nil, err
				}
				return tc.wrap(s, c), nil
			}}
			ctx := context.Background()
			for seed := int64(1); seed <= 20; seed++ {
				o, err := r.Run(ctx, seed, 400)
				if err != nil {
					t.Fatal(err)
				}
				if !o.Failed() {
					continue
				}
				if o.Violation.Invariant != tc.inv {
					var b strings.Builder
					o.WriteTrace(&b)
					t.Fatalf("seed %d: got %s, want %s\n%s", seed, o.Violation, tc.inv, b.String())
				}
				if !tc.shrink {
					return
				}
				s, err := r.Minimize(ctx, o, 40)
				if err != nil {
					t.Fatal(err)
				}
				if s.Violation.Invariant != tc.inv || len(s.Steps) > 15 {
					var b strings.Builder
					s.WriteTrace(&b)
					t.Fatalf("seed %d: shrunk to %d steps:\n%s", seed, len(s.Steps), b.String())
				}
				return
			}
			t.Fatalf("no seed detected %s", tc.inv)
		})
	}
}
