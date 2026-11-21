package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/eval"
)

var root = filepath.Join("..", "..")

func runCLI(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	base := []string{"-fixtures", filepath.Join(root, "testdata", "eval"), "-root", root}
	code := run(context.Background(), append(base, args...), func(k string) string { return env[k] }, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestFakeRunEnforcesFloors(t *testing.T) {
	code, out, errOut := runCLI(t, nil, "-format", "json", "-min-plan", "0.95", "-min-review", "1", "-min-robustness", "1", "-max-unsafe", "0")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var rep eval.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.PlanPrompt != "leader" || rep.ReviewPrompt != "reviewer" || len(rep.Cases) < 52 {
		t.Fatalf("report %s %s %d cases", rep.PlanPrompt, rep.ReviewPrompt, len(rep.Cases))
	}

	code, _, errOut = runCLI(t, nil, "-min-plan", "1.01")
	if code != exitFloors || !strings.Contains(errOut, "plan mean") {
		t.Fatalf("exit %d: %s", code, errOut)
	}
}

func TestCompareTwoPromptVersions(t *testing.T) {
	alt := filepath.Join(t.TempDir(), "review.txt")
	if err := os.WriteFile(alt, []byte("Review the PR and answer with JSON."), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCLI(t, nil, "-format", "json", "-review-prompt-b", "file:"+alt, "-plan-prompt-b", "library:subspace-task-basics")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var res struct {
		A, B       eval.Report
		Comparison []eval.Comparison
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.B.ReviewPrompt != "file:"+alt || res.B.PlanPrompt != "library:subspace-task-basics" || len(res.Comparison) != 3 {
		t.Fatalf("b %s %s, %d comparisons", res.B.PlanPrompt, res.B.ReviewPrompt, len(res.Comparison))
	}
	for _, c := range res.Comparison {
		if c.Pairs == 0 || c.Delta != 0 || c.Significant {
			t.Fatalf("fake model is prompt-independent, got %+v", c)
		}
	}

	code, out, _ = runCLI(t, nil, "-review-prompt-b", "go:cmd/subspace-reviewer/review.go#systemPrompt")
	if code != exitOK || !strings.Contains(out, "95% CI") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestBudgetSkipsCases(t *testing.T) {
	code, out, errOut := runCLI(t, nil, "-format", "json", "-budget-eur", "0.05", "-concurrency", "1")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var rep eval.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Summary(eval.SuiteRobustness).Skipped == 0 || rep.Usage.CostEUR < 0.05 {
		t.Fatalf("usage %+v summaries %+v", rep.Usage, rep.Summaries)
	}
}

func TestUsageErrors(t *testing.T) {
	cases := [][]string{
		{"-model", "gpt"},
		{"-model", "anthropic"},
		{"-model", "anthropic", "-model-name", "claude-x"},
		{"-format", "yaml"},
		{"-plan-prompt", "nope"},
		{"-review-prompt", "go:cmd/subspace-reviewer/review.go#missing"},
	}
	for _, args := range cases {
		if code, _, _ := runCLI(t, map[string]string{}, args...); code != exitUsage {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}
