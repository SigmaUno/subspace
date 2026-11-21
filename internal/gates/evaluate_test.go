package gates

import (
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

func TestEvaluate(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	report := func(id string, passed, failed int, ago time.Duration) TestReport {
		return TestReport{TaskID: id, Result: model.TestResult{Passed: passed, Failed: failed, ReportedAt: now.Add(-ago)}}
	}
	green := &PR{Checks: "success", HeadSHA: "abc", HeadSince: now.Add(-time.Hour), ScopeSynced: true}
	review := &ReviewInfo{Reviewer: "codex-r", ReviewerProvider: "openai", WorkerProvider: "anthropic"}
	all := model.QualityGates{Tests: true, MinTestsPassed: 5, Checks: true, Scope: true, Questions: true, Review: true, Budget: true, MaxPRAgeHours: 24}
	base := func() Input {
		return Input{Config: all, Now: now,
			Task:   &model.Task{ID: "T-1", BriefID: "B-1", AcceptanceCriteria: []string{"a", "b"}, Budget: model.Budget{MaxCostEUR: 5, MaxMinutes: 60}, CostUsed: 1, MinutesUsed: 10},
			Brief:  &model.Brief{ID: "B-1", Budget: model.BriefBudget{MaxCostEUR: 50}},
			Tests:  []TestReport{report("T-1", 10, 0, time.Minute)},
			PR:     green,
			Review: review}
	}
	cases := []struct {
		name     string
		mod      func(*Input)
		fail     string
		evidence string
	}{
		{name: "all pass"},
		{name: "everything off", mod: func(in *Input) { in.Config = model.QualityGates{}; in.Tests, in.PR, in.Review = nil, nil, nil }},
		{name: "no test report", mod: func(in *Input) { in.Tests = nil }, fail: Tests, evidence: "no test report"},
		{name: "failed tests", mod: func(in *Input) { in.Tests = []TestReport{report("T-1", 10, 1, 0)} }, fail: Tests, evidence: "1 failed"},
		{name: "too few passed", mod: func(in *Input) { in.Tests = []TestReport{report("T-1", 4, 0, 0)} }, fail: Tests, evidence: "at least 5"},
		{name: "latest tester report wins", mod: func(in *Input) {
			in.Tests = []TestReport{report("T-1", 10, 0, time.Hour), report("T-2", 9, 2, time.Minute)}
		}, fail: Tests, evidence: "report of T-2"},
		{name: "older failure superseded", mod: func(in *Input) {
			in.Tests = []TestReport{report("T-2", 9, 2, time.Hour), report("T-1", 6, 0, time.Minute)}
		}},
		{name: "checks pending", mod: func(in *Input) { pr := *green; pr.Checks = "pending"; in.PR = &pr }, fail: Checks, evidence: "checks pending"},
		{name: "no pr", mod: func(in *Input) {
			in.PR, in.PRError = nil, "pull request was never synced"
			in.Config.Scope, in.Config.MaxPRAgeHours = false, 0
		}, fail: Checks, evidence: "never synced"},
		{name: "scope errors", mod: func(in *Input) {
			pr := *green
			pr.ScopeErrors = []string{"outside_paths", "protected_path"}
			in.PR = &pr
		}, fail: Scope, evidence: "outside_paths, protected_path"},
		{name: "scope not synced", mod: func(in *Input) { pr := *green; pr.ScopeSynced = false; in.PR = &pr }, fail: Scope, evidence: "no scope report"},
		{name: "open question", mod: func(in *Input) { in.OpenQuestions = []string{"Q-3"} }, fail: Questions, evidence: "Q-3"},
		{name: "no reviewer", mod: func(in *Input) { in.Review = &ReviewInfo{} }, fail: Review, evidence: "no reviewer"},
		{name: "same provider", mod: func(in *Input) {
			in.Review = &ReviewInfo{Reviewer: "claude-r", ReviewerProvider: "anthropic", WorkerProvider: "anthropic"}
		}, fail: Review, evidence: "worker's provider"},
		{name: "criterion failed", mod: func(in *Input) {
			rv := *review
			rv.Comments = "criterion 1: pass\n- AC2 = unmet, missing test"
			in.Review = &rv
		}, fail: Review, evidence: "criteria not met: 2"},
		{name: "criteria met", mod: func(in *Input) { rv := *review; rv.Comments = "criterion 1: met\nAC2: ok"; in.Review = &rv }},
		{name: "criterion outside the task ignored", mod: func(in *Input) { rv := *review; rv.Comments = "criterion 7: fail"; in.Review = &rv }},
		{name: "task cost over", mod: func(in *Input) { in.Task.CostUsed = 5.5 }, fail: Budget, evidence: "task cost"},
		{name: "task cost at limit", mod: func(in *Input) { in.Task.CostUsed = 5 }},
		{name: "task minutes over", mod: func(in *Input) { in.Task.MinutesUsed = 61 }, fail: Budget, evidence: "task time"},
		{name: "brief cost over", mod: func(in *Input) { in.BriefCost = 51 }, fail: Budget, evidence: "brief cost"},
		{name: "pr too old", mod: func(in *Input) { pr := *green; pr.HeadSince = now.Add(-25 * time.Hour); in.PR = &pr }, fail: PRAge, evidence: "unchanged for 25h0m0s"},
		{name: "pr head age unknown", mod: func(in *Input) { pr := *green; pr.HeadSince = time.Time{}; in.PR = &pr }, fail: PRAge, evidence: "unknown"},
		{name: "override all", mod: func(in *Input) {
			in.Tests = nil
			in.Override = &Override{Reason: "flaky ci"}
		}},
		{name: "override other gate", mod: func(in *Input) {
			in.Tests = nil
			in.Override = &Override{Gates: []string{Checks}, Reason: "flaky ci"}
		}, fail: Tests},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			if tc.mod != nil {
				tc.mod(&in)
			}
			r := Evaluate(in)
			if len(r.Gates) != len(All) {
				t.Fatalf("gates %v", r.Gates)
			}
			fails := r.Failures()
			if tc.fail == "" {
				if !r.Passed || len(fails) != 0 || r.Error() != nil {
					t.Fatalf("want pass, got %+v", r.Gates)
				}
				return
			}
			if r.Passed || len(fails) != 1 || fails[0].Gate != tc.fail {
				t.Fatalf("want only %s to fail, got %+v", tc.fail, fails)
			}
			if !strings.Contains(fails[0].Evidence, tc.evidence) {
				t.Fatalf("evidence %q lacks %q", fails[0].Evidence, tc.evidence)
			}
			e := r.Error()
			if e == nil || e.Code != "gates_failed" || e.HTTPStatus() != 409 {
				t.Fatalf("error %+v", e)
			}
			if got := e.Details.(map[string]any)["gates"].([]Result); len(got) != 1 || got[0].Gate != tc.fail {
				t.Fatalf("details %+v", got)
			}
		})
	}
}

func TestEvaluateOverrideMarksGate(t *testing.T) {
	in := Input{Config: model.QualityGates{Tests: true, Questions: true}, Task: &model.Task{ID: "T-1"},
		OpenQuestions: []string{"Q-1"}, Override: &Override{Gates: []string{Tests, Questions}, Reason: "owner call"}}
	r := Evaluate(in)
	if !r.Passed {
		t.Fatalf("overridden report must pass: %+v", r.Gates)
	}
	for _, g := range r.Gates {
		if g.Enabled && (g.Passed || !g.Overridden || !strings.Contains(g.Evidence, "owner call")) {
			t.Fatalf("gate %+v", g)
		}
		if !g.Enabled && (g.Overridden || g.Evidence != "disabled") {
			t.Fatalf("disabled gate %+v", g)
		}
	}
}
