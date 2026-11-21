package dst

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/testmain"
	"github.com/SigmaUno/subspace/internal/testmode"
)

const (
	regressionsFile = "../../testdata/dst/regressions.txt"
	idempotencyFile = "../../testdata/dst/idempotency.txt"
	freshSeeds      = 8
	freshSteps      = 200
)

func TestMain(m *testing.M) { testmain.Run(m) }

var mcpFaults = MCPOptions{FaultSeed: 1, DropPct: 10, DupPct: 10}

func mcpRunner(dir string, o MCPOptions) *Runner {
	return &Runner{Factory: NewMCPSystem(o), Dir: dir, Serial: true}
}

func runSeed(t *testing.T, r *Runner, seed int64, steps int) {
	t.Helper()
	mode := ModeBoard
	switch {
	case r.Subsystems:
		mode = ModeSubsystems
	case r.World != nil:
		mode = ModeServer
	}
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
	t.Fatalf("seed %d: %s\nreplay: go run ./cmd/subspace-dst -mode %s -seeds 1 -start %d -steps %d\n%s", seed, o.Violation, mode, seed, steps, b.String())
}

func TestSimulation(t *testing.T) {
	t.Parallel()
	seeds, steps, fresh := 5, 300, freshSeeds
	if !testmode.Full() {
		seeds, steps, fresh = 3, 200, 4
	}
	var regs []Regression
	for seed := int64(1); seed <= int64(seeds); seed++ {
		regs = append(regs, Regression{Seed: seed, Steps: steps})
	}
	start := time.Now().UnixNano() % 1_000_000_000
	for i := range fresh {
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

func TestServerSimulation(t *testing.T) {
	t.Parallel()
	seeds, steps := 4, 400
	if !testmode.Full() {
		seeds, steps = 4, 300
	}
	r := ServerRunner(t.TempDir())
	for seed := int64(1); seed <= int64(seeds); seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			runSeed(t, r, seed, steps)
		})
	}
}

func TestSubsystemSimulation(t *testing.T) {
	t.Parallel()
	seeds, steps := 4, 300
	if !testmode.Full() {
		seeds, steps = 2, 250
	}
	r := SubsystemsRunner(t.TempDir())
	for seed := int64(1); seed <= int64(seeds); seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			runSeed(t, r, seed, steps)
		})
	}
}

func TestSimulationWithLeaderHandoffs(t *testing.T) {
	t.Parallel()
	seeds, steps := 3, 300
	if !testmode.Full() {
		seeds, steps = 3, 200
	}
	runners := map[string]*Runner{
		ModeBoard:    {Factory: NewBoardSystem, Dir: t.TempDir()},
		"mcp_faults": mcpRunner(t.TempDir(), mcpFaults),
		ModeServer:   ServerRunner(t.TempDir()),
	}
	for name, r := range runners {
		r.Leaders = true
		for seed := int64(1); seed <= int64(seeds); seed++ {
			t.Run(fmt.Sprintf("%s/%d", name, seed), func(t *testing.T) {
				t.Parallel()
				runSeed(t, r, seed, steps)
			})
		}
	}
}

func TestRegressions(t *testing.T) {
	t.Parallel()
	regs, err := LoadRegressions(regressionsFile)
	if err != nil {
		t.Fatal(err)
	}
	runners := map[string]*Runner{
		ModeBoard:      {Factory: NewBoardSystem, Dir: t.TempDir()},
		ModeServer:     ServerRunner(t.TempDir()),
		ModeSubsystems: SubsystemsRunner(t.TempDir()),
	}
	for _, reg := range regs {
		t.Run(fmt.Sprintf("%s/%d", reg.Mode, reg.Seed), func(t *testing.T) {
			t.Parallel()
			runSeed(t, runners[reg.Mode], reg.Seed, reg.Steps)
		})
	}
}

func TestDeterministic(t *testing.T) {
	t.Parallel()
	runners := map[string]*Runner{
		ModeBoard:    {Factory: NewBoardSystem, Dir: t.TempDir(), Serial: true},
		"mcp_faults": mcpRunner(t.TempDir(), mcpFaults),
		ModeServer:   ServerRunner(t.TempDir()),
	}
	runners[ModeServer].Serial = true
	for mode, r := range runners {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			var traces [2]string
			var wg sync.WaitGroup
			for i := range traces {
				wg.Go(func() {
					o, err := r.Run(context.Background(), 7, 300)
					if err != nil {
						t.Error(err)
						return
					}
					var b strings.Builder
					o.WriteTrace(&b)
					traces[i] = b.String()
				})
			}
			wg.Wait()
			if traces[0] != traces[1] {
				t.Fatalf("seed 7 is not reproducible:\n%s\n---\n%s", traces[0], traces[1])
			}
		})
	}
}

func TestSimulationOverMCPWithDuplicateAndDroppedResponses(t *testing.T) {
	t.Parallel()
	seeds, steps := 4, 300
	if !testmode.Full() {
		seeds, steps = 3, 200
	}
	r := mcpRunner(t.TempDir(), mcpFaults)
	for seed := int64(1); seed <= int64(seeds); seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			runSeed(t, r, seed, steps)
		})
	}
}

func TestIdempotencyRegressions(t *testing.T) {
	t.Parallel()
	regs, err := LoadRegressions(idempotencyFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) == 0 {
		t.Fatal("no idempotency regression seeds")
	}
	without := mcpFaults
	without.NoIdempotency = true
	bare := mcpRunner(t.TempDir(), without)
	r := mcpRunner(t.TempDir(), mcpFaults)
	for _, reg := range regs {
		t.Run(fmt.Sprint(reg.Seed), func(t *testing.T) {
			t.Parallel()
			o, err := bare.Run(context.Background(), reg.Seed, reg.Steps)
			if err != nil {
				t.Fatal(err)
			}
			if !o.Failed() || o.Violation.Invariant != InvAtomic {
				t.Fatalf("seed %d no longer double-applies without idempotency keys: %v", reg.Seed, o.Violation)
			}
			runSeed(t, r, reg.Seed, reg.Steps)
		})
	}
}

func TestDetectsServerFaults(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		o    ServerOptions
		inv  string
		from int64
		v4   bool
	}{
		{"merge_without_gate", ServerOptions{AutoApprove: true}, InvMerge, 4, false},
		{"claim_without_quota", ServerOptions{NoQuotaGate: true}, InvQuota, 3, false},
		{"approve_without_quality_gates", ServerOptions{NoQualityGate: true}, InvGates, 9, false},
		{"leaks_without_scanner", ServerOptions{NoLeakScanner: true}, InvLeak, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := ServerRunner(t.TempDir())
			r.Subsystems = tc.v4
			r.Factory = NewServerFactory(tc.o)
			for seed := tc.from; seed < tc.from+3; seed++ {
				o, err := r.Run(context.Background(), seed, 400)
				if err != nil {
					t.Fatal(err)
				}
				if !o.Failed() {
					continue
				}
				if o.Violation.Invariant != tc.inv {
					t.Fatalf("seed %d: got %s, want %s", seed, o.Violation, tc.inv)
				}
				return
			}
			t.Fatalf("no seed detected %s", tc.inv)
		})
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
	t.Parallel()
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
				s, err := r.Minimize(ctx, o, 60)
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
