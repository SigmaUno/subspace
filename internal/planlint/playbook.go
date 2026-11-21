package planlint

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/SigmaUno/subspace/internal/model"
)

const (
	RulePlaybookCoverage = "playbook_coverage"
	RulePlaybookTask     = "playbook_required_task"
	RulePlaybookCriteria = "playbook_criteria"
	RulePlaybookSize     = "playbook_task_size"
)

const (
	CoverageNone             = ""
	CoverageTester           = "tester"
	CoverageTesterOrReviewer = "tester_or_reviewer"
)

const (
	maxRuleItems       = 10
	maxPatternBytes    = 200
	maxInstructionsLen = 2000
	maxBudgetMinutes   = 100000
	maxBudgetCostEUR   = 1000000
	minMultiplier      = 0.1
	maxMultiplier      = 10
)

var Sizes = []string{"s", "m", "l", "xl"}

var ruleNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

type RequiredTask struct {
	Name     string `json:"name"`
	Pattern  string `json:"pattern"`
	Role     string `json:"role"`
	Severity string `json:"severity"`
}

type CriteriaRule struct {
	Name     string `json:"name"`
	Pattern  string `json:"pattern"`
	Role     string `json:"role"`
	Severity string `json:"severity"`
}

type Rules struct {
	Coverage            string         `json:"coverage"`
	RequiredTasks       []RequiredTask `json:"required_tasks"`
	CriteriaRules       []CriteriaRule `json:"criteria_rules"`
	AllowedSizes        []string       `json:"allowed_sizes"`
	DefaultTaskBudget   model.Budget   `json:"default_task_budget"`
	BriefCostMultiplier float64        `json:"brief_cost_multiplier"`
	CrossReview         bool           `json:"cross_review"`
	Instructions        string         `json:"instructions"`
}

func SizeOf(minutes int) string {
	switch {
	case minutes <= 0:
		return ""
	case minutes <= 60:
		return "s"
	case minutes <= 180:
		return "m"
	case minutes <= 480:
		return "l"
	}
	return "xl"
}

func (r *Rules) Normalize() {
	r.Instructions = strings.TrimSpace(r.Instructions)
	if r.RequiredTasks == nil {
		r.RequiredTasks = []RequiredTask{}
	}
	if r.CriteriaRules == nil {
		r.CriteriaRules = []CriteriaRule{}
	}
	if r.AllowedSizes == nil {
		r.AllowedSizes = []string{}
	}
	for i := range r.RequiredTasks {
		t := &r.RequiredTasks[i]
		t.Name, t.Pattern = strings.TrimSpace(t.Name), strings.TrimSpace(t.Pattern)
		if t.Severity == "" {
			t.Severity = SeverityError
		}
	}
	for i := range r.CriteriaRules {
		c := &r.CriteriaRules[i]
		c.Name, c.Pattern = strings.TrimSpace(c.Name), strings.TrimSpace(c.Pattern)
		if c.Severity == "" {
			c.Severity = SeverityError
		}
	}
}

func (r *Rules) Validate() error {
	r.Normalize()
	if !slices.Contains([]string{CoverageNone, CoverageTester, CoverageTesterOrReviewer}, r.Coverage) {
		return model.BadRequest("coverage must be empty, %s or %s", CoverageTester, CoverageTesterOrReviewer)
	}
	if len(r.RequiredTasks) > maxRuleItems || len(r.CriteriaRules) > maxRuleItems {
		return model.BadRequest("at most %d required_tasks and %d criteria_rules", maxRuleItems, maxRuleItems)
	}
	for _, t := range r.RequiredTasks {
		if err := checkPattern("required_tasks", t.Name, t.Pattern, t.Role, t.Severity); err != nil {
			return err
		}
	}
	for _, c := range r.CriteriaRules {
		if err := checkPattern("criteria_rules", c.Name, c.Pattern, c.Role, c.Severity); err != nil {
			return err
		}
	}
	for i, s := range r.AllowedSizes {
		if !slices.Contains(Sizes, s) || slices.Index(r.AllowedSizes, s) != i {
			return model.BadRequest("allowed_sizes must be distinct values of %s", strings.Join(Sizes, ", "))
		}
	}
	b := r.DefaultTaskBudget
	if b.MaxMinutes < 0 || b.MaxMinutes > maxBudgetMinutes || b.MaxCostEUR < 0 || b.MaxCostEUR > maxBudgetCostEUR {
		return model.BadRequest("default_task_budget is out of range")
	}
	if m := r.BriefCostMultiplier; m != 0 && (m < minMultiplier || m > maxMultiplier) {
		return model.BadRequest("brief_cost_multiplier must be 0 (off) or between %g and %g", minMultiplier, float64(maxMultiplier))
	}
	if len([]rune(r.Instructions)) > maxInstructionsLen {
		return model.BadRequest("instructions must be at most %d characters", maxInstructionsLen)
	}
	return nil
}

func checkPattern(list, name, pattern, role, severity string) error {
	if !ruleNameRe.MatchString(name) {
		return model.BadRequest("%s name must match [a-z][a-z0-9_]{0,31}", list)
	}
	if pattern == "" || len(pattern) > maxPatternBytes {
		return model.BadRequest("%s %s: pattern must be 1..%d bytes", list, name, maxPatternBytes)
	}
	if _, err := compile(pattern); err != nil {
		return model.BadRequest("%s %s: invalid pattern: %v", list, name, err)
	}
	if role != "" && role != model.RoleWorker && role != model.RoleTester {
		return model.BadRequest("%s %s: role must be empty, worker or tester", list, name)
	}
	if severity != SeverityError && severity != SeverityWarning {
		return model.BadRequest("%s %s: severity must be error or warning", list, name)
	}
	return nil
}

func compile(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile("(?i)" + pattern)
}

func (r *Rules) ApplyDefaults(b model.Budget) model.Budget {
	if b.MaxMinutes <= 0 {
		b.MaxMinutes = r.DefaultTaskBudget.MaxMinutes
	}
	if b.MaxCostEUR <= 0 {
		b.MaxCostEUR = r.DefaultTaskBudget.MaxCostEUR
	}
	return b
}

func (r *Rules) ScaleCost(v float64) float64 {
	if r.BriefCostMultiplier <= 0 {
		return v
	}
	return v * r.BriefCostMultiplier
}

func LintPlaybook(brief model.Brief, tasks []Task, r *Rules) []Finding {
	out := Lint(brief, tasks)
	if r == nil {
		return out
	}
	add := collect(&out)
	coverage(r, brief, tasks, add)
	requiredTasks(r, tasks, add)
	criteria(r, tasks, add)
	sizes(r, tasks, add)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Severity == SeverityError && out[j].Severity != SeverityError })
	return out
}

func LintPlaybookTask(t Task, r *Rules) []Finding {
	var out []Finding
	if r == nil {
		return out
	}
	add := collect(&out)
	criteria(r, []Task{t}, add)
	sizes(r, []Task{t}, add)
	return out
}

func collect(out *[]Finding) adder {
	return func(severity, rule string, t *Task, format string, a ...any) {
		f := Finding{Severity: severity, Rule: rule, Message: fmt.Sprintf(format, a...)}
		if t != nil {
			f.Task = label(t)
		}
		*out = append(*out, f)
	}
}

func isWorker(t *Task) bool { return t.Role == "" || t.Role == model.RoleWorker }

func coverage(r *Rules, brief model.Brief, tasks []Task, add adder) {
	if r.Coverage == CoverageNone || (r.Coverage == CoverageTesterOrReviewer && brief.CrossReview) {
		return
	}
	tested := map[string]bool{}
	for i := range tasks {
		if tasks[i].Role == model.RoleTester {
			for _, d := range tasks[i].DependsOn {
				tested[d] = true
			}
		}
	}
	for i := range tasks {
		t := &tasks[i]
		if isWorker(t) && (t.Ref == "" || !tested[t.Ref]) {
			add(SeverityError, RulePlaybookCoverage, t, "no tester task depends on this task (playbook requires a tester task or a reviewer)")
		}
	}
}

func requiredTasks(r *Rules, tasks []Task, add adder) {
	for _, req := range r.RequiredTasks {
		re, err := compile(req.Pattern)
		if err != nil {
			add(SeverityError, RulePlaybookTask, nil, "required task %s has an invalid pattern", req.Name)
			continue
		}
		found := false
		for i := range tasks {
			t := &tasks[i]
			if (req.Role == "" || req.Role == roleOf(t)) && re.MatchString(t.Title+"\n"+t.Spec) {
				found = true
				break
			}
		}
		if !found {
			add(req.Severity, RulePlaybookTask, nil, "the plan has no %s task (title or spec matching /%s/)", req.Name, req.Pattern)
		}
	}
}

func roleOf(t *Task) string {
	if t.Role == "" {
		return model.RoleWorker
	}
	return t.Role
}

func criteria(r *Rules, tasks []Task, add adder) {
	for _, c := range r.CriteriaRules {
		re, err := compile(c.Pattern)
		if err != nil {
			add(SeverityError, RulePlaybookCriteria, nil, "criteria rule %s has an invalid pattern", c.Name)
			continue
		}
		for i := range tasks {
			t := &tasks[i]
			if c.Role != "" && c.Role != roleOf(t) {
				continue
			}
			if !slices.ContainsFunc(t.AcceptanceCriteria, re.MatchString) {
				add(c.Severity, RulePlaybookCriteria, t, "no acceptance criterion for %s (matching /%s/)", c.Name, c.Pattern)
			}
		}
	}
}

func sizes(r *Rules, tasks []Task, add adder) {
	if len(r.AllowedSizes) == 0 {
		return
	}
	for i := range tasks {
		t := &tasks[i]
		size := SizeOf(t.Budget.MaxMinutes)
		switch {
		case size == "":
			add(SeverityWarning, RulePlaybookSize, t, "no max_minutes, so the task size is unknown (allowed: %s)", strings.Join(r.AllowedSizes, ", "))
		case !slices.Contains(r.AllowedSizes, size):
			add(SeverityError, RulePlaybookSize, t, "task size %s (%d minutes) is not allowed (allowed: %s)", size, t.Budget.MaxMinutes, strings.Join(r.AllowedSizes, ", "))
		}
	}
}
