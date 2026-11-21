package planlint_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/planlint"
	"github.com/SigmaUno/subspace/internal/playbooks"
)

func builtin(t *testing.T, name string) *planlint.Rules {
	t.Helper()
	for _, p := range playbooks.Builtin() {
		if p.Name == name {
			r := p.Rules
			if err := r.Validate(); err != nil {
				t.Fatalf("built-in %s is invalid: %v", name, err)
			}
			return &r
		}
	}
	t.Fatalf("no built-in playbook %s", name)
	return nil
}

func work(ref, title string, minutes int, criteria ...string) planlint.Task {
	return planlint.Task{Ref: ref, Title: title, Spec: "Implement " + title + " and run the unit tests", Repo: "o/r", Role: model.RoleWorker,
		AcceptanceCriteria: criteria, Budget: model.Budget{MaxMinutes: minutes}}
}

func tester(ref string, minutes int, deps ...string) planlint.Task {
	return planlint.Task{Ref: ref, Title: "Test " + ref, Spec: "Write and run tests for " + ref, Repo: "o/r", Role: model.RoleTester,
		AcceptanceCriteria: []string{"Test report lists every failure"}, DependsOn: deps, Budget: model.Budget{MaxMinutes: minutes}}
}

func playbookFindings(t *testing.T, name string, crossReview bool, tasks ...planlint.Task) (errs, warns []string) {
	t.Helper()
	b := brief()
	b.CrossReview = crossReview
	e, w := planlint.Split(planlint.LintPlaybook(b, tasks, builtin(t, name)))
	key := func(f planlint.Finding) string { return f.Rule + "@" + f.Task }
	for _, f := range e {
		errs = append(errs, key(f))
	}
	for _, f := range w {
		warns = append(warns, key(f))
	}
	slices.Sort(errs)
	slices.Sort(warns)
	return errs, warns
}

func playbookOnly(warns []string) []string {
	var out []string
	for _, w := range warns {
		if len(w) > 9 && w[:9] == "playbook_" {
			out = append(out, w)
		}
	}
	return out
}

func TestPlaybookRules(t *testing.T) {
	regression := "A regression test reproduces the crash"
	cases := []struct {
		name     string
		playbook string
		cross    bool
		tasks    []planlint.Task
		errs     []string
		warns    []string
	}{
		{
			name: "feature passes with cross review and no tester", playbook: "feature", cross: true,
			tasks: []planlint.Task{work("a", "login page", 120, "Unit tests cover the form")},
		},
		{
			name: "feature without cross review needs a tester", playbook: "feature",
			tasks: []planlint.Task{work("a", "login page", 120, "Unit tests cover the form")},
			errs:  []string{"playbook_coverage@a"},
		},
		{
			name: "feature tester covers its dependency only", playbook: "feature",
			tasks: []planlint.Task{work("a", "login page", 120, "Unit tests cover the form"), work("b", "logout page", 120, "Unit tests cover the button"), tester("t", 30, "a")},
			errs:  []string{"playbook_coverage@b"},
		},
		{
			name: "feature oversized task", playbook: "feature", cross: true,
			tasks: []planlint.Task{work("a", "login page", 600, "Unit tests cover the form")},
			errs:  []string{"playbook_task_size@a"},
		},
		{
			name: "feature unknown size warns", playbook: "feature", cross: true,
			tasks: []planlint.Task{work("a", "login page", 0, "Unit tests cover the form")},
			warns: []string{"playbook_task_size@a"},
		},
		{
			name: "feature test criterion is a warning", playbook: "feature", cross: true,
			tasks: []planlint.Task{work("a", "login page", 120, "Form renders two fields")},
			warns: []string{"playbook_criteria@a"},
		},
		{
			name: "hotfix complete", playbook: "hotfix", cross: true,
			tasks: []planlint.Task{
				work("fix", "fix crash", 60, regression), work("undo", "rollback of the fix", 30, "Regression test covers the rollback"),
				tester("t1", 30, "fix"), tester("t2", 30, "undo"),
			},
		},
		{
			name: "hotfix needs a rollback task", playbook: "hotfix", cross: true,
			tasks: []planlint.Task{work("fix", "fix crash", 60, regression), tester("t1", 30, "fix")},
			errs:  []string{"playbook_required_task@"},
		},
		{
			name: "hotfix needs a regression criterion and a tester", playbook: "hotfix", cross: true,
			tasks: []planlint.Task{work("fix", "fix crash and rollback plan", 60, "Crash no longer happens")},
			errs:  []string{"playbook_coverage@fix", "playbook_criteria@fix"},
		},
		{
			name: "hotfix rejects medium-large work", playbook: "hotfix", cross: true,
			tasks: []planlint.Task{work("fix", "fix crash and rollback plan", 300, regression), tester("t", 30, "fix")},
			errs:  []string{"playbook_task_size@fix"},
		},
		{
			name: "refactor needs unchanged behaviour criterion", playbook: "refactor", cross: true,
			tasks: []planlint.Task{work("a", "split store", 120, "Interfaces are smaller"), work("b", "split board", 120, "Existing tests still pass")},
			errs:  []string{"playbook_criteria@a"},
		},
		{
			name: "security audit findings report is a warning", playbook: "security-audit", cross: true,
			tasks: []planlint.Task{work("a", "review authz checks", 90, "Every authz bypass is listed")},
			warns: []string{"playbook_required_task@"},
		},
		{
			name: "performance needs a measurable criterion", playbook: "performance", cross: true,
			tasks: []planlint.Task{work("base", "baseline benchmark", 60, "Benchmark results are stored"), work("opt", "speed up lookups", 120, "Lookups are faster")},
			errs:  []string{"playbook_criteria@opt"},
		},
		{
			name: "performance complete", playbook: "performance", cross: true,
			tasks: []planlint.Task{work("base", "baseline benchmark", 60, "Benchmark results are stored"), work("opt", "speed up lookups", 120, "p95 latency drops below 20ms")},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			errs, warns := playbookFindings(t, c.playbook, c.cross, c.tasks...)
			if got := playbookOnly(errs); !slices.Equal(got, c.errs) {
				t.Errorf("playbook errors = %v, want %v (all: %v)", got, c.errs, errs)
			}
			if got := playbookOnly(warns); !slices.Equal(got, c.warns) {
				t.Errorf("playbook warnings = %v, want %v (all: %v)", got, c.warns, warns)
			}
		})
	}
}

func TestPlaybookLintKeepsBaseFindings(t *testing.T) {
	a := good("a")
	a.Repo = "x/y"
	fs := planlint.LintPlaybook(brief(), []planlint.Task{a}, builtin(t, "feature"))
	var rules []string
	for _, f := range fs {
		rules = append(rules, f.Rule)
	}
	if !slices.Contains(rules, planlint.RuleRepo) {
		t.Errorf("base finding lost: %v", rules)
	}
	if got := planlint.LintPlaybook(brief(), []planlint.Task{good("a")}, nil); len(got) != 0 {
		t.Errorf("nil rules must equal plain lint, got %v", got)
	}
}

func TestRulesValidate(t *testing.T) {
	ok := func() planlint.Rules {
		return planlint.Rules{RequiredTasks: []planlint.RequiredTask{{Name: "rollback", Pattern: "roll ?back"}}}
	}
	cases := map[string]func(*planlint.Rules){
		"bad coverage":      func(r *planlint.Rules) { r.Coverage = "everyone" },
		"bad pattern":       func(r *planlint.Rules) { r.RequiredTasks[0].Pattern = "(" },
		"empty pattern":     func(r *planlint.Rules) { r.RequiredTasks[0].Pattern = " " },
		"long pattern":      func(r *planlint.Rules) { r.RequiredTasks[0].Pattern = fmt.Sprintf("%0250d", 1) },
		"bad rule name":     func(r *planlint.Rules) { r.RequiredTasks[0].Name = "Roll Back" },
		"bad severity":      func(r *planlint.Rules) { r.RequiredTasks[0].Severity = "fatal" },
		"bad role":          func(r *planlint.Rules) { r.RequiredTasks[0].Role = "reviewer" },
		"bad size":          func(r *planlint.Rules) { r.AllowedSizes = []string{"huge"} },
		"duplicate size":    func(r *planlint.Rules) { r.AllowedSizes = []string{"s", "s"} },
		"negative budget":   func(r *planlint.Rules) { r.DefaultTaskBudget.MaxMinutes = -1 },
		"huge budget":       func(r *planlint.Rules) { r.DefaultTaskBudget.MaxCostEUR = 1e9 },
		"tiny multiplier":   func(r *planlint.Rules) { r.BriefCostMultiplier = 0.01 },
		"huge multiplier":   func(r *planlint.Rules) { r.BriefCostMultiplier = 11 },
		"long instructions": func(r *planlint.Rules) { r.Instructions = fmt.Sprintf("%02500d", 1) },
		"too many rules": func(r *planlint.Rules) {
			for range 11 {
				r.CriteriaRules = append(r.CriteriaRules, planlint.CriteriaRule{Name: "x", Pattern: "x"})
			}
		},
	}
	for name, mutate := range cases {
		r := ok()
		mutate(&r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	r := ok()
	if err := r.Validate(); err != nil || r.RequiredTasks[0].Severity != planlint.SeverityError {
		t.Errorf("valid rules: %v, severity %q", err, r.RequiredTasks[0].Severity)
	}
}

func TestDefaultsAndMultiplier(t *testing.T) {
	r := planlint.Rules{DefaultTaskBudget: model.Budget{MaxMinutes: 60, MaxCostEUR: 3}, BriefCostMultiplier: 0.5}
	if got := r.ApplyDefaults(model.Budget{MaxCostEUR: 9}); got != (model.Budget{MaxMinutes: 60, MaxCostEUR: 9}) {
		t.Errorf("defaults = %+v", got)
	}
	if got := r.ScaleCost(40); got != 20 {
		t.Errorf("scaled = %v", got)
	}
	var off planlint.Rules
	if got := off.ScaleCost(40); got != 40 {
		t.Errorf("unscaled = %v", got)
	}
	for minutes, want := range map[int]string{0: "", 1: "s", 60: "s", 61: "m", 180: "m", 181: "l", 480: "l", 481: "xl"} {
		if got := planlint.SizeOf(minutes); got != want {
			t.Errorf("SizeOf(%d) = %q, want %q", minutes, got, want)
		}
	}
}
