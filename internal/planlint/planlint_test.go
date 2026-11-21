package planlint_test

import (
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/planlint"
)

func good(ref string, deps ...string) planlint.Task {
	return planlint.Task{
		Ref: ref, Title: "Task " + ref, Spec: "Implement the endpoint and run the unit tests", Repo: "o/r",
		AcceptanceCriteria: []string{"GET /x returns 200", "go test ./... passes"},
		DependsOn:          deps, Budget: model.Budget{MaxMinutes: 60},
	}
}

func brief() model.Brief {
	return model.Brief{Repos: []string{"o/r"}, CrossReview: true}
}

func rules(fs []planlint.Finding, severity string) map[string][]string {
	out := map[string][]string{}
	for _, f := range fs {
		if f.Severity == severity {
			out[f.Rule] = append(out[f.Rule], f.Task)
		}
	}
	return out
}

func TestLint(t *testing.T) {
	long := func(n int) string { return strings.Repeat("x", n) }
	runes := func(n int) string { return "test" + strings.Repeat("é", n-4) }
	cases := []struct {
		name  string
		brief func(*model.Brief)
		tasks func() []planlint.Task
		errs  map[string][]string
		warns map[string][]string
	}{
		{name: "clean plan", tasks: func() []planlint.Task { return []planlint.Task{good("a"), good("b", "a")} }},
		{
			name: "missing acceptance criteria",
			tasks: func() []planlint.Task {
				a := good("a")
				a.AcceptanceCriteria = []string{"", "  "}
				return []planlint.Task{a}
			},
			warns: map[string][]string{planlint.RuleAcceptance: {"a"}},
		},
		{
			name: "single generic criterion",
			tasks: func() []planlint.Task {
				a, b, c := good("a"), good("b"), good("c")
				a.AcceptanceCriteria = []string{"Works."}
				b.AcceptanceCriteria = []string{"all fine"}
				c.AcceptanceCriteria = []string{"Response contains the user id"}
				return []planlint.Task{a, b, c}
			},
			warns: map[string][]string{planlint.RuleAcceptance: {"a", "b"}},
		},
		{
			name: "no verification",
			tasks: func() []planlint.Task {
				a, tester := good("a"), good("t")
				a.Spec, a.AcceptanceCriteria = "Add the login page", []string{"Page shows a form", "Form has two fields"}
				tester.Spec, tester.Role, tester.AcceptanceCriteria = "Add more things", "tester", []string{"Report lists failures"}
				return []planlint.Task{a, tester}
			},
			warns: map[string][]string{planlint.RuleVerification: {"a"}},
		},
		{
			name: "oversized spec",
			tasks: func() []planlint.Task {
				a, b := good("a"), good("b")
				a.Spec = "run the tests " + long(5000)
				b.Spec = "run the tests " + long(25000)
				return []planlint.Task{a, b}
			},
			errs:  map[string][]string{planlint.RuleSpecSize: {"b"}},
			warns: map[string][]string{planlint.RuleSpecSize: {"a"}},
		},
		{
			name: "spec size limits are inclusive and count runes",
			tasks: func() []planlint.Task {
				a, b, c, d := good("a"), good("b"), good("c"), good("d")
				a.Spec, b.Spec, c.Spec, d.Spec = runes(4000), runes(4001), runes(20000), runes(20001)
				return []planlint.Task{a, b, c, d}
			},
			errs:  map[string][]string{planlint.RuleSpecSize: {"d"}},
			warns: map[string][]string{planlint.RuleSpecSize: {"b", "c"}},
		},
		{
			name: "fifteen criteria are allowed",
			tasks: func() []planlint.Task {
				a := good("a")
				a.AcceptanceCriteria = nil
				for i := 0; i < 15; i++ {
					a.AcceptanceCriteria = append(a.AcceptanceCriteria, "criterion number "+string(rune('a'+i)))
				}
				return []planlint.Task{a}
			},
		},
		{
			name: "too many criteria",
			tasks: func() []planlint.Task {
				a := good("a")
				a.AcceptanceCriteria = nil
				for i := 0; i < 16; i++ {
					a.AcceptanceCriteria = append(a.AcceptanceCriteria, "criterion number "+string(rune('a'+i)))
				}
				return []planlint.Task{a}
			},
			warns: map[string][]string{planlint.RuleSpecSize: {"a"}},
		},
		{
			name: "cycle and unreachable",
			tasks: func() []planlint.Task {
				return []planlint.Task{good("a", "c"), good("b", "a"), good("c", "b"), good("d", "c"), good("e", "d"), good("f")}
			},
			errs: map[string][]string{
				planlint.RuleCycle:       {"a", "b", "c"},
				planlint.RuleUnreachable: {"d", "e"},
			},
		},
		{
			name:  "self dependency",
			tasks: func() []planlint.Task { return []planlint.Task{good("a", "a")} },
			errs:  map[string][]string{planlint.RuleCycle: {"a"}},
		},
		{
			name:  "dependency on an existing task is not a cycle",
			tasks: func() []planlint.Task { return []planlint.Task{good("a", "T-4")} },
		},
		{
			name:  "duplicate ref",
			tasks: func() []planlint.Task { return []planlint.Task{good("a"), good("a")} },
			errs:  map[string][]string{planlint.RuleDuplicateRef: {"a"}},
			warns: map[string][]string{planlint.RuleDuplicateTitle: {"a"}},
		},
		{
			name: "repo outside brief",
			tasks: func() []planlint.Task {
				a := good("a")
				a.Repo = "o/other"
				return []planlint.Task{a, good("b")}
			},
			errs: map[string][]string{planlint.RuleRepo: {"a"}},
		},
		{
			name: "any repo allowed when brief has none",
			brief: func(b *model.Brief) {
				b.Repos = nil
			},
			tasks: func() []planlint.Task {
				a := good("a")
				a.Repo = "o/other"
				return []planlint.Task{a}
			},
		},
		{
			name: "risky work without tester",
			tasks: func() []planlint.Task {
				a := good("a")
				a.Title = "Add password reset"
				return []planlint.Task{a, good("b")}
			},
			warns: map[string][]string{planlint.RuleNoTester: {""}},
		},
		{
			name: "risky work with tester",
			tasks: func() []planlint.Task {
				a, tester := good("a"), good("t")
				a.Title, tester.Role = "Add password reset", "tester"
				return []planlint.Task{a, tester}
			},
		},
		{
			name: "duplicate titles",
			tasks: func() []planlint.Task {
				a, b := good("a"), good("b")
				b.Title = "  TASK   a "
				return []planlint.Task{a, b}
			},
			warns: map[string][]string{planlint.RuleDuplicateTitle: {"b"}},
		},
		{
			name:  "budget missing under a cost-capped brief",
			brief: func(b *model.Brief) { b.Budget.MaxCostEUR = 20 },
			tasks: func() []planlint.Task {
				a, b := good("a"), good("b")
				a.Budget = model.Budget{}
				b.Budget = model.Budget{MaxCostEUR: 2}
				return []planlint.Task{a, b}
			},
			warns: map[string][]string{planlint.RuleBudget: {"a"}},
		},
		{
			name:  "small budgets count as budgets",
			brief: func(b *model.Brief) { b.Budget.MaxCostEUR = 0.5 },
			tasks: func() []planlint.Task {
				a, b, c := good("a"), good("b"), good("c")
				a.Budget = model.Budget{}
				b.Budget = model.Budget{MaxCostEUR: 0.5}
				c.Budget = model.Budget{MaxMinutes: 1}
				return []planlint.Task{a, b, c}
			},
			warns: map[string][]string{planlint.RuleBudget: {"a"}},
		},
		{
			name:  "no budget warning without a brief cost cap",
			tasks: func() []planlint.Task { a := good("a"); a.Budget = model.Budget{}; return []planlint.Task{a} },
		},
		{
			name:  "too many ready at once",
			brief: func(b *model.Brief) { b.Budget.MaxParallelAgent = 2 },
			tasks: func() []planlint.Task {
				return []planlint.Task{good("a"), good("b"), good("c"), good("d", "a", "b", "c")}
			},
			warns: map[string][]string{planlint.RuleParallelism: {""}},
		},
		{
			name:  "ready tasks up to the parallel limit",
			brief: func(b *model.Brief) { b.Budget.MaxParallelAgent = 3 },
			tasks: func() []planlint.Task {
				return []planlint.Task{good("a"), good("b"), good("c"), good("d", "a", "b", "c")}
			},
		},
		{
			name:  "single agent brief",
			brief: func(b *model.Brief) { b.Budget.MaxParallelAgent = 1 },
			tasks: func() []planlint.Task { return []planlint.Task{good("a"), good("b")} },
			warns: map[string][]string{planlint.RuleParallelism: {""}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := brief()
			if c.brief != nil {
				c.brief(&b)
			}
			fs := planlint.Lint(b, c.tasks())
			for sev, want := range map[string]map[string][]string{planlint.SeverityError: c.errs, planlint.SeverityWarning: c.warns} {
				got := rules(fs, sev)
				if len(got) != len(want) {
					t.Fatalf("%s findings: got %v want %v (all: %+v)", sev, got, want, fs)
				}
				for rule, tasks := range want {
					if strings.Join(got[rule], ",") != strings.Join(tasks, ",") {
						t.Fatalf("%s %s: got %v want %v", sev, rule, got[rule], tasks)
					}
				}
			}
		})
	}
}

func TestLintOrdersErrorsFirstAndSplits(t *testing.T) {
	a := good("a")
	a.AcceptanceCriteria = nil
	b := good("b")
	b.Repo = "o/other"
	fs := planlint.Lint(brief(), []planlint.Task{a, b})
	if len(fs) != 2 || fs[0].Severity != planlint.SeverityError {
		t.Fatalf("errors must come first: %+v", fs)
	}
	errs, warns := planlint.Split(fs)
	if len(errs) != 1 || len(warns) != 1 {
		t.Fatalf("split: %+v %+v", errs, warns)
	}
	if got := planlint.Format(errs); !strings.Contains(got, planlint.RuleRepo) || !strings.Contains(got, "[b]") {
		t.Fatalf("format: %q", got)
	}
}

func TestNoTesterMessage(t *testing.T) {
	a := good("a")
	a.Title = "Rotate the deploy credentials"
	for _, cross := range []bool{true, false} {
		b := brief()
		b.CrossReview = cross
		fs := planlint.Lint(b, []planlint.Task{a, good("b")})
		if len(fs) != 1 || fs[0].Rule != planlint.RuleNoTester {
			t.Fatalf("findings: %+v", fs)
		}
		want := "risky work (a) but the plan has no tester task"
		if !cross {
			want += " and cross review is off"
		}
		if fs[0].Message != want {
			t.Fatalf("cross_review=%v: message %q, want %q", cross, fs[0].Message, want)
		}
	}
}
