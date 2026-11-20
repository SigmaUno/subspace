package planlint

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/SigmaUno/subspace/internal/model"
)

const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

const (
	RuleAcceptance     = "acceptance_criteria"
	RuleVerification   = "verification"
	RuleSpecSize       = "spec_size"
	RuleCycle          = "dependency_cycle"
	RuleUnreachable    = "unreachable_task"
	RuleRepo           = "repo_outside_brief"
	RuleNoTester       = "no_tester"
	RuleDuplicateTitle = "duplicate_title"
	RuleDuplicateRef   = "duplicate_ref"
	RuleBudget         = "budget_missing"
	RuleParallelism    = "parallelism"
)

const (
	warnSpecRunes      = 4000
	maxSpecRunes       = 20000
	minCriterionWords  = 3
	maxCriteriaPerTask = 15
)

type Finding = model.PlanFinding

type Task struct {
	Ref                string
	Title              string
	Spec               string
	AcceptanceCriteria []string
	Repo               string
	Role               string
	DependsOn          []string
	Budget             model.Budget
}

var genericCriteria = map[string]bool{
	"done": true, "works": true, "it works": true, "complete": true, "completed": true, "finished": true,
	"implemented": true, "tests pass": true, "all good": true, "works as expected": true, "as expected": true,
	"task is done": true, "feature works": true, "looks good": true,
}

var verificationWords = []string{
	"test", "verif", "assert", "check", "lint", "build", "passes", "validate", "curl", "benchmark", "reproduc",
}

var riskyWords = []string{
	"auth", "security", "payment", "billing", "migration", "migrate", "delete", "drop table", "credential",
	"secret", "token", "password", "production", "deploy", "crypto", "permission", "encrypt",
}

type adder func(severity, rule string, t *Task, format string, a ...any)

func Lint(brief model.Brief, tasks []Task) []Finding {
	var out []Finding
	add := func(severity, rule string, t *Task, format string, a ...any) {
		f := Finding{Severity: severity, Rule: rule, Message: fmt.Sprintf(format, a...)}
		if t != nil {
			f.Task = label(t)
		}
		out = append(out, f)
	}
	for i := range tasks {
		t := &tasks[i]
		acceptance(t, add)
		verification(t, add)
		specSize(t, add)
		if len(brief.Repos) > 0 && !contains(brief.Repos, t.Repo) {
			add(SeverityError, RuleRepo, t, "repo %q is not part of the brief (%s)", t.Repo, strings.Join(brief.Repos, ", "))
		}
		if brief.Budget.MaxCostEUR > 0 && t.Budget.MaxCostEUR <= 0 && t.Budget.MaxMinutes <= 0 {
			add(SeverityWarning, RuleBudget, t, "no task budget while the brief has max_cost_eur %.2f", brief.Budget.MaxCostEUR)
		}
	}
	graph(tasks, add)
	duplicateTitles(tasks, add)
	missingTester(tasks, brief.CrossReview, add)
	if n := brief.Budget.MaxParallelAgent; n > 0 {
		if ready := readyAtOnce(tasks); ready > n {
			add(SeverityWarning, RuleParallelism, nil, "%d tasks are ready at once but max_parallel_agents is %d", ready, n)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Severity == SeverityError && out[j].Severity != SeverityError })
	return out
}

func Split(fs []Finding) (errs, warns []Finding) {
	for _, f := range fs {
		if f.Severity == SeverityError {
			errs = append(errs, f)
		} else {
			warns = append(warns, f)
		}
	}
	return errs, warns
}

func Format(fs []Finding) string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = f.Rule + ": " + f.Message
		if f.Task != "" {
			parts[i] = f.Rule + " [" + f.Task + "]: " + f.Message
		}
	}
	return strings.Join(parts, "; ")
}

func label(t *Task) string {
	if t.Ref != "" {
		return t.Ref
	}
	return t.Title
}

func acceptance(t *Task, add adder) {
	var items []string
	for _, c := range t.AcceptanceCriteria {
		if c = strings.TrimSpace(c); c != "" {
			items = append(items, c)
		}
	}
	switch {
	case len(items) == 0:
		add(SeverityWarning, RuleAcceptance, t, "no acceptance criteria")
	case len(items) == 1 && vague(items[0]):
		add(SeverityWarning, RuleAcceptance, t, "single generic acceptance criterion %q", items[0])
	case len(items) > maxCriteriaPerTask:
		add(SeverityWarning, RuleSpecSize, t, "%d acceptance criteria; split the task", len(items))
	}
}

func vague(c string) bool {
	n := strings.ToLower(strings.TrimRight(strings.TrimSpace(c), ".!"))
	return genericCriteria[n] || len(strings.Fields(n)) < minCriterionWords
}

func verification(t *Task, add adder) {
	if t.Role == model.RoleTester {
		return
	}
	text := strings.ToLower(t.Spec + " " + strings.Join(t.AcceptanceCriteria, " "))
	if !containsAny(text, verificationWords) {
		add(SeverityWarning, RuleVerification, t, "neither spec nor acceptance criteria say how the result is verified")
	}
}

func specSize(t *Task, add adder) {
	switch n := utf8.RuneCountInString(t.Spec); {
	case n > maxSpecRunes:
		add(SeverityError, RuleSpecSize, t, "spec has %d characters, the limit is %d", n, maxSpecRunes)
	case n > warnSpecRunes:
		add(SeverityWarning, RuleSpecSize, t, "spec has %d characters; consider splitting the task (over %d)", n, warnSpecRunes)
	}
}

func graph(tasks []Task, add adder) {
	idx := map[string]int{}
	for i := range tasks {
		r := tasks[i].Ref
		if r == "" {
			continue
		}
		if _, dup := idx[r]; dup {
			add(SeverityError, RuleDuplicateRef, &tasks[i], "ref %q is used more than once", r)
			continue
		}
		idx[r] = i
	}
	deps := make([][]int, len(tasks))
	for i := range tasks {
		for _, d := range tasks[i].DependsOn {
			if j, ok := idx[d]; ok {
				deps[i] = append(deps[i], j)
			}
		}
	}
	onCycle := cycleMembers(deps)
	for i := range tasks {
		if onCycle[i] {
			add(SeverityError, RuleCycle, &tasks[i], "task is part of a dependency cycle")
		}
	}
	stuck := map[int]bool{}
	var unreachable func(i int) bool
	unreachable = func(i int) bool {
		if onCycle[i] {
			return true
		}
		if v, seen := stuck[i]; seen {
			return v
		}
		stuck[i] = false
		for _, j := range deps[i] {
			if unreachable(j) {
				stuck[i] = true
				break
			}
		}
		return stuck[i]
	}
	for i := range tasks {
		if !onCycle[i] && unreachable(i) {
			add(SeverityError, RuleUnreachable, &tasks[i], "task can never start: it depends on a task in a dependency cycle")
		}
	}
}

func cycleMembers(deps [][]int) map[int]bool {
	index := make([]int, len(deps))
	low := make([]int, len(deps))
	on := make([]bool, len(deps))
	for i := range index {
		index[i] = -1
	}
	var stack []int
	next := 0
	out := map[int]bool{}
	var connect func(v int)
	connect = func(v int) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		on[v] = true
		for _, w := range deps[v] {
			switch {
			case index[w] == -1:
				connect(w)
				low[v] = min(low[v], low[w])
			case on[w]:
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] != index[v] {
			return
		}
		var comp []int
		for {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			on[w] = false
			comp = append(comp, w)
			if w == v {
				break
			}
		}
		if len(comp) > 1 {
			for _, w := range comp {
				out[w] = true
			}
			return
		}
		if contains(deps[v], v) {
			out[v] = true
		}
	}
	for v := range deps {
		if index[v] == -1 {
			connect(v)
		}
	}
	return out
}

func duplicateTitles(tasks []Task, add adder) {
	seen := map[string]bool{}
	for i := range tasks {
		k := strings.Join(strings.Fields(strings.ToLower(tasks[i].Title)), " ")
		if seen[k] {
			add(SeverityWarning, RuleDuplicateTitle, &tasks[i], "title %q is used by more than one task", tasks[i].Title)
		}
		seen[k] = true
	}
}

func missingTester(tasks []Task, crossReview bool, add adder) {
	var risky []string
	for i := range tasks {
		t := &tasks[i]
		if t.Role == model.RoleTester {
			return
		}
		if containsAny(strings.ToLower(t.Title+" "+t.Spec), riskyWords) {
			risky = append(risky, label(t))
		}
	}
	if len(risky) == 0 {
		return
	}
	msg := "risky work (%s) but the plan has no tester task"
	if !crossReview {
		msg += " and cross review is off"
	}
	add(SeverityWarning, RuleNoTester, nil, msg, strings.Join(risky, ", "))
}

func readyAtOnce(tasks []Task) int {
	n := 0
	for i := range tasks {
		if len(tasks[i].DependsOn) == 0 {
			n++
		}
	}
	return n
}

func contains[T comparable](xs []T, x T) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}
