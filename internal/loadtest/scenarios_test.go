package loadtest

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestScenarios(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	think := 5 * time.Millisecond
	const bound = 5 * time.Minute
	cases := []Config{
		{Scenario: ScenarioContention, Agents: 40, Think: think, Duration: bound},
		{Scenario: ScenarioBigPlan, Agents: 20, Tasks: 50, Reviewers: 4, Think: think, Duration: bound},
		{Scenario: ScenarioManyBriefs, Agents: 20, Briefs: 5, Reviewers: 4, Think: think, Duration: bound},
		{Scenario: ScenarioThreadHeavy, Agents: 10, Messages: 200, Think: think, Duration: bound},
		{Scenario: ScenarioReconnectStorm, Agents: 10, SSESubs: 100, Think: think, Duration: 10 * time.Second},
		{Scenario: ScenarioRestart, Agents: 20, SSESubs: 20, TasksPerAgent: 5, Think: think, Duration: bound},
	}
	for _, cfg := range cases {
		t.Run(cfg.Scenario, func(t *testing.T) {
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
