package dst

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

const (
	testSteps  = 60
	testAgents = 8
)

func runSeed(t *testing.T, cfg Config) {
	t.Helper()
	res := Check(context.Background(), cfg)
	if res.Err == nil {
		return
	}
	tail := res.Log[max(0, len(res.Log)-60):]
	t.Fatalf("seed %d: %v\n%s\nreproduce: go run ./cmd/subspace-dst -seed %d -steps %d -agents %d",
		cfg.Seed, res.Err, strings.Join(tail, "\n"), cfg.Seed, cfg.Steps, cfg.Agents)
}

func TestFixedSeeds(t *testing.T) {
	t.Parallel()
	for seed := int64(1); seed <= 25; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			runSeed(t, Config{Seed: seed, Steps: testSteps, Agents: testAgents})
		})
	}
}

func TestRegressionSeeds(t *testing.T) {
	t.Parallel()
	f, err := os.Open("testdata/regressions.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var cfg Config
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		if _, err := fmt.Sscan(sc.Text(), &cfg.Seed, &cfg.Steps, &cfg.Agents); err != nil {
			t.Fatalf("bad regression line %q: %v", sc.Text(), err)
		}
		t.Run(fmt.Sprint(cfg.Seed), func(t *testing.T) {
			t.Parallel()
			runSeed(t, cfg)
		})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestDeterministic(t *testing.T) {
	t.Parallel()
	cfg := Config{Seed: 7, Steps: 40, Agents: testAgents}
	a, b := Check(context.Background(), cfg), Check(context.Background(), cfg)
	if a.Err != nil || b.Err != nil {
		t.Fatalf("seed 7 failed: %v / %v", a.Err, b.Err)
	}
	if !slices.Equal(a.Log, b.Log) {
		for i := range min(len(a.Log), len(b.Log)) {
			if a.Log[i] != b.Log[i] {
				t.Fatalf("runs diverge at line %d:\n%s\n%s", i, a.Log[i], b.Log[i])
			}
		}
		t.Fatalf("runs differ in length: %d vs %d", len(a.Log), len(b.Log))
	}
}
