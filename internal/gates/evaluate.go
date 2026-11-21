package gates

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

const (
	Tests     = "tests"
	Checks    = "checks"
	Scope     = "scope"
	Questions = "questions"
	Review    = "review"
	Budget    = "budget"
	PRAge     = "pr_age"
)

var All = []string{Tests, Checks, Scope, Questions, Review, Budget, PRAge}

var criterionRe = regexp.MustCompile(`(?im)^\s*(?:[-*]\s*)?(?:criterion|ac)\s*#?(\d+)\s*[:=-]\s*(pass(?:ed)?|met|ok|fail(?:ed)?|unmet)\b`)

type TestReport struct {
	TaskID string           `json:"task_id"`
	Result model.TestResult `json:"result"`
}

type PR struct {
	Checks      string    `json:"checks"`
	HeadSHA     string    `json:"head_sha"`
	HeadSince   time.Time `json:"head_since"`
	ScopeSynced bool      `json:"scope_synced"`
	ScopeErrors []string  `json:"scope_errors"`
}

type ReviewInfo struct {
	Reviewer         string `json:"reviewer"`
	ReviewerProvider string `json:"reviewer_provider"`
	WorkerProvider   string `json:"worker_provider"`
	Comments         string `json:"-"`
}

type Override struct {
	Gates  []string  `json:"gates"`
	Reason string    `json:"reason"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`
}

func (o *Override) Covers(gate string) bool {
	return o != nil && (len(o.Gates) == 0 || slices.Contains(o.Gates, gate))
}

type Input struct {
	Config        model.QualityGates
	Now           time.Time
	Task          *model.Task
	Brief         *model.Brief
	BriefCost     float64
	Tests         []TestReport
	PR            *PR
	PRError       string
	OpenQuestions []string
	Review        *ReviewInfo
	Override      *Override
}

type Result struct {
	Gate       string `json:"gate"`
	Enabled    bool   `json:"enabled"`
	Passed     bool   `json:"passed"`
	Overridden bool   `json:"overridden"`
	Evidence   string `json:"evidence"`
}

type Report struct {
	TaskID      string    `json:"task_id"`
	BriefID     string    `json:"brief_id"`
	Passed      bool      `json:"passed"`
	Gates       []Result  `json:"gates"`
	Override    *Override `json:"override"`
	EvaluatedAt time.Time `json:"evaluated_at"`
}

func (r Report) Failures() []Result {
	var out []Result
	for _, g := range r.Gates {
		if g.Enabled && !g.Passed && !g.Overridden {
			out = append(out, g)
		}
	}
	return out
}

func (r Report) Error() *model.Error {
	f := r.Failures()
	if len(f) == 0 {
		return nil
	}
	parts := make([]string, len(f))
	for i, g := range f {
		parts[i] = g.Gate + " (" + g.Evidence + ")"
	}
	return &model.Error{Code: "gates_failed", Message: "quality gates failed: " + strings.Join(parts, "; "),
		Details: map[string]any{"task_id": r.TaskID, "gates": f}}
}

func Evaluate(in Input) Report {
	r := Report{TaskID: in.Task.ID, BriefID: in.Task.BriefID, Passed: true, Override: in.Override, EvaluatedAt: in.Now}
	c := in.Config
	add := func(gate string, enabled bool, eval func() (bool, string)) {
		res := Result{Gate: gate, Enabled: enabled, Passed: true}
		if enabled {
			res.Passed, res.Evidence = eval()
			if !res.Passed && in.Override.Covers(gate) {
				res.Overridden = true
				res.Evidence += "; overridden: " + in.Override.Reason
			}
			if !res.Passed && !res.Overridden {
				r.Passed = false
			}
		} else {
			res.Evidence = "disabled"
		}
		r.Gates = append(r.Gates, res)
	}
	add(Tests, c.Tests, func() (bool, string) { return tests(in) })
	add(Checks, c.Checks, func() (bool, string) { return checks(in) })
	add(Scope, c.Scope, func() (bool, string) { return scope(in) })
	add(Questions, c.Questions, func() (bool, string) { return questions(in) })
	add(Review, c.Review, func() (bool, string) { return review(in) })
	add(Budget, c.Budget, func() (bool, string) { return budget(in) })
	add(PRAge, c.MaxPRAgeHours > 0, func() (bool, string) { return prAge(in) })
	return r
}

func tests(in Input) (bool, string) {
	var latest *TestReport
	for i := range in.Tests {
		t := &in.Tests[i]
		if latest == nil || t.Result.ReportedAt.After(latest.Result.ReportedAt) {
			latest = t
		}
	}
	if latest == nil {
		return false, "no test report"
	}
	res := latest.Result
	ev := fmt.Sprintf("%d passed, %d failed (report of %s)", res.Passed, res.Failed, latest.TaskID)
	if res.Failed > 0 {
		return false, ev
	}
	if res.Passed < in.Config.MinTestsPassed {
		return false, fmt.Sprintf("%s; at least %d must pass", ev, in.Config.MinTestsPassed)
	}
	return true, ev
}

func noPR(in Input) string {
	if in.PRError != "" {
		return in.PRError
	}
	return "no pull request status"
}

func checks(in Input) (bool, string) {
	if in.PR == nil {
		return false, noPR(in)
	}
	return in.PR.Checks == "success", "checks " + in.PR.Checks
}

func scope(in Input) (bool, string) {
	if in.PR == nil {
		return false, noPR(in)
	}
	if !in.PR.ScopeSynced {
		return false, "no scope report"
	}
	if len(in.PR.ScopeErrors) > 0 {
		return false, "error findings: " + strings.Join(in.PR.ScopeErrors, ", ")
	}
	return true, "no error findings"
}

func questions(in Input) (bool, string) {
	if len(in.OpenQuestions) > 0 {
		return false, "open questions: " + strings.Join(in.OpenQuestions, ", ")
	}
	return true, "no open questions"
}

func review(in Input) (bool, string) {
	rv := in.Review
	if rv == nil || rv.Reviewer == "" {
		return false, "no reviewer"
	}
	if rv.ReviewerProvider == "" || rv.ReviewerProvider == rv.WorkerProvider {
		return false, fmt.Sprintf("reviewer %s uses the worker's provider %q", rv.Reviewer, rv.WorkerProvider)
	}
	n := len(in.Task.AcceptanceCriteria)
	var unmet []string
	for _, m := range criterionRe.FindAllStringSubmatch(rv.Comments, -1) {
		i, err := strconv.Atoi(m[1])
		if err != nil || i < 1 || (n > 0 && i > n) {
			continue
		}
		v := strings.ToLower(m[2])
		if strings.HasPrefix(v, "fail") || v == "unmet" {
			unmet = append(unmet, strconv.Itoa(i))
		}
	}
	if len(unmet) > 0 {
		slices.Sort(unmet)
		return false, "criteria not met: " + strings.Join(slices.Compact(unmet), ", ")
	}
	return true, fmt.Sprintf("reviewed by %s (%s), worker on %s", rv.Reviewer, rv.ReviewerProvider, rv.WorkerProvider)
}

func budget(in Input) (bool, string) {
	t := in.Task
	if t.Budget.MaxCostEUR > 0 && t.CostUsed > t.Budget.MaxCostEUR {
		return false, fmt.Sprintf("task cost %.2f EUR above %.2f", t.CostUsed, t.Budget.MaxCostEUR)
	}
	if t.Budget.MaxMinutes > 0 && t.MinutesUsed > t.Budget.MaxMinutes {
		return false, fmt.Sprintf("task time %d min above %d", t.MinutesUsed, t.Budget.MaxMinutes)
	}
	if in.Brief != nil && in.Brief.Budget.MaxCostEUR > 0 && in.BriefCost > in.Brief.Budget.MaxCostEUR {
		return false, fmt.Sprintf("brief cost %.2f EUR above %.2f", in.BriefCost, in.Brief.Budget.MaxCostEUR)
	}
	return true, fmt.Sprintf("task %.2f EUR, %d min; brief %.2f EUR", t.CostUsed, t.MinutesUsed, in.BriefCost)
}

func prAge(in Input) (bool, string) {
	if in.PR == nil {
		return false, noPR(in)
	}
	if in.PR.HeadSince.IsZero() {
		return false, "head commit age unknown"
	}
	age := in.Now.Sub(in.PR.HeadSince)
	limit := time.Duration(in.Config.MaxPRAgeHours * float64(time.Hour))
	ev := fmt.Sprintf("head %s unchanged for %s (limit %s)", short(in.PR.HeadSHA), age.Round(time.Minute), limit)
	return age <= limit, ev
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
