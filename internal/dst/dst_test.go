package dst

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

const regressionsFile = "../../testdata/dst/regressions.txt"

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
	if s, err := r.Shrink(ctx, o, 300); err == nil {
		o = s
	}
	var b strings.Builder
	o.WriteTrace(&b)
	t.Fatalf("seed %d: %s\nreplay: go run ./cmd/subspace-dst -seeds 1 -start %d -steps %d\n%s", seed, o.Violation, seed, steps, b.String())
}

func TestSimulation(t *testing.T) {
	seeds, steps := 16, 300
	if testing.Short() {
		seeds, steps = 4, 200
	}
	r := &Runner{Factory: NewBoardSystem, Dir: t.TempDir()}
	for seed := int64(1); seed <= int64(seeds); seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			runSeed(t, r, seed, steps)
		})
	}
}

func TestRegressions(t *testing.T) {
	regs, err := LoadRegressions(regressionsFile)
	if err != nil {
		t.Fatal(err)
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
	r := &Runner{Factory: NewBoardSystem, Dir: t.TempDir()}
	trace := func() string {
		o, err := r.Run(context.Background(), 7, 300)
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

func TestDetectsLeaseBugs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		early  bool
		inv    string
		shrink bool
	}{
		{"sweep_ignores_expiry", false, InvLeaseExpiry, true},
		{"sweep_revokes_early", true, InvLeaseHonored, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &Runner{Dir: t.TempDir(), Factory: func(dir string, c *Clock, lease time.Duration) (System, error) {
				s, err := NewBoardSystem(dir, c, lease)
				if err != nil {
					return nil, err
				}
				return &faultySweep{System: s, clock: c, early: tc.early}, nil
			}}
			ctx := context.Background()
			for seed := int64(1); seed <= 20; seed++ {
				o, err := r.Run(ctx, seed, 500)
				if err != nil {
					t.Fatal(err)
				}
				if !o.Failed() {
					continue
				}
				if o.Violation.Invariant != tc.inv {
					t.Fatalf("seed %d: got %s, want %s", seed, o.Violation, tc.inv)
				}
				if !tc.shrink {
					return
				}
				s, err := r.Shrink(ctx, o, 200)
				if err != nil {
					t.Fatal(err)
				}
				if s.Violation.Invariant != tc.inv || len(s.Steps) > 10 {
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
