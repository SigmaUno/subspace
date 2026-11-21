package loadtest

import (
	"bytes"
	"context"
	"os"
	"runtime"
	"testing"
	"time"
)

const fullProcs = 8

func scenarioScale() float64 {
	if os.Getenv("SUBSPACE_FULL") == "1" {
		return 1
	}
	return min(1, max(0.125, float64(runtime.GOMAXPROCS(0))/fullProcs))
}

func scaled(n int, scale float64) int {
	return max(1, int(float64(n)*scale))
}

func TestScenarios(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	scale := scenarioScale()
	n := func(v int) int { return scaled(v, scale) }
	think := 5 * time.Millisecond
	cases := []Config{
		{Scenario: ScenarioContention, Agents: n(40), Think: think},
		{Scenario: ScenarioBigPlan, Agents: n(20), Tasks: n(50), Reviewers: n(4), Think: think},
		{Scenario: ScenarioManyBriefs, Agents: n(20), Briefs: n(5), Reviewers: n(4), Think: think},
		{Scenario: ScenarioThreadHeavy, Agents: n(10), Messages: n(200), Think: think},
		{Scenario: ScenarioReconnectStorm, Agents: n(10), SSESubs: n(100), Duration: 10 * time.Second, Think: think},
		{Scenario: ScenarioRestart, Agents: n(20), SSESubs: n(20), TasksPerAgent: 5, Think: think},
	}
	deadline := time.Duration(float64(10*time.Second) / scale)
	for _, cfg := range cases {
		t.Run(cfg.Scenario, func(t *testing.T) {
			if cfg.Duration == 0 {
				cfg.Duration = deadline
			}
			rep, err := Run(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			rep.WriteText(&buf)
			t.Log(buf.String())
			if len(rep.Invariants) == 0 {
				t.Fatal("scenario checked no invariants")
			}
			if rep.Breached() {
				t.Fatalf("errors %d (%v), failed invariants %d", rep.Errors, rep.ErrorSamples, rep.InvariantsFailed())
			}
		})
	}
}

func TestScenarioValidation(t *testing.T) {
	bad := []Config{
		{Scenario: "nope"},
		{Scenario: ScenarioRestart, URL: "http://x", OwnerToken: "t"},
		{Scenario: ScenarioBigPlan, Tasks: 501},
		{Scenario: ScenarioReconnectStorm, SSESubs: 0},
	}
	for i, c := range bad {
		c.Agents, c.Duration = 5, time.Second
		c.Defaults()
		if c.Validate() == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}
