package eval

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/planlint"
)

type planTask struct {
	Ref                string       `json:"ref"`
	Title              string       `json:"title"`
	Spec               string       `json:"spec"`
	AcceptanceCriteria []string     `json:"acceptance_criteria"`
	Repo               string       `json:"repo"`
	DependsOn          []string     `json:"depends_on"`
	Role               string       `json:"role"`
	Budget             model.Budget `json:"budget"`
	Priority           int          `json:"priority"`
}

type planOutput struct {
	Note  string     `json:"note"`
	Tasks []planTask `json:"tasks"`
}

func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "```"); ok {
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "```"))
	}
	return s
}

func decodeOne(text string, v any) error {
	s := stripFence(text)
	if !strings.HasPrefix(s, "{") {
		return errors.New("output is not a single JSON object")
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after the JSON object")
	}
	return nil
}

func parsePlan(text string) (*planOutput, error) {
	var p planOutput
	if err := decodeOne(text, &p); err != nil {
		return nil, err
	}
	if len(p.Tasks) == 0 {
		return nil, errors.New("plan has no tasks")
	}
	return &p, nil
}

func (t planTask) text() string {
	return strings.ToLower(t.Title + "\n" + t.Spec + "\n" + strings.Join(t.AcceptanceCriteria, "\n"))
}

func matchesAll(text string, keywords []string) bool {
	for _, k := range keywords {
		if !matchesAny(text, k) {
			return false
		}
	}
	return true
}

func matchesAny(text, alternatives string) bool {
	for _, alt := range strings.Split(strings.ToLower(alternatives), "|") {
		if alt = strings.TrimSpace(alt); alt != "" && strings.Contains(text, alt) {
			return true
		}
	}
	return false
}

func coverage(f *PlanFixture, p *planOutput) map[string]int {
	out := map[string]int{}
	for _, r := range f.Requirements {
		for i, t := range p.Tasks {
			if matchesAll(t.text(), r.Keywords) {
				out[r.ID] = i
				break
			}
		}
	}
	return out
}

var planWeights = map[string]float64{
	"coverage": 0.3, "criteria": 0.15, "lint": 0.15, "deps": 0.15, "size": 0.1, "budget": 0.15,
}

func scorePlan(f *PlanFixture, p *planOutput) (float64, map[string]float64, []string) {
	var notes []string
	checks := map[string]float64{}
	brief := model.Brief{ID: f.ID, Budget: model.BriefBudget{MaxCostEUR: f.MaxCostEUR, MaxParallelAgent: f.MaxParallel}, CrossReview: f.CrossReview}
	for _, r := range f.Repos {
		brief.Repos = append(brief.Repos, r.Name)
	}
	tasks := make([]planlint.Task, len(p.Tasks))
	for i, t := range p.Tasks {
		tasks[i] = planlint.Task{Ref: t.Ref, Title: t.Title, Spec: t.Spec, AcceptanceCriteria: t.AcceptanceCriteria,
			Repo: t.Repo, Role: t.Role, DependsOn: t.DependsOn, Budget: t.Budget}
	}
	findings := planlint.Lint(brief, tasks)
	errs, warns := planlint.Split(findings)
	checks["lint"] = clamp(1 - 0.5*float64(len(errs)) - 0.1*float64(len(warns)))
	for _, fd := range findings {
		notes = append(notes, "lint "+planlint.Format([]planlint.Finding{fd}))
	}

	cov := coverage(f, p)
	checks["coverage"] = float64(len(cov)) / float64(len(f.Requirements))
	var crit float64
	for _, r := range f.Requirements {
		i, ok := cov[r.ID]
		if !ok {
			notes = append(notes, "requirement "+r.ID+" not covered")
			continue
		}
		text := strings.ToLower(strings.Join(p.Tasks[i].AcceptanceCriteria, "\n"))
		hit := 0
		for _, c := range r.Checks {
			if matchesAny(text, c) {
				hit++
			} else {
				notes = append(notes, fmt.Sprintf("requirement %s: criteria of %q do not mention %q", r.ID, p.Tasks[i].Ref, c))
			}
		}
		crit += float64(hit) / float64(len(r.Checks))
	}
	if len(cov) > 0 {
		checks["criteria"] = crit / float64(len(cov))
	}

	checks["deps"] = depScore(f, p, cov, errs, &notes)
	checks["size"] = sizeScore(f, p, &notes)
	checks["budget"] = budgetScore(f, p, &notes)

	var score float64
	for k, w := range planWeights {
		score += w * checks[k]
	}
	return score, checks, notes
}

func depScore(f *PlanFixture, p *planOutput, cov map[string]int, lintErrs []planlint.Finding, notes *[]string) float64 {
	refs := map[string]int{}
	for i, t := range p.Tasks {
		refs[t.Ref] = i
	}
	for _, t := range p.Tasks {
		for _, d := range t.DependsOn {
			if _, ok := refs[d]; !ok || d == t.Ref {
				*notes = append(*notes, fmt.Sprintf("task %q has an invalid dependency %q", t.Ref, d))
				return 0
			}
		}
	}
	for _, e := range lintErrs {
		if e.Rule == planlint.RuleCycle || e.Rule == planlint.RuleUnreachable || e.Rule == planlint.RuleDuplicateRef {
			return 0
		}
	}
	reaches := func(from, to int) bool {
		seen := map[int]bool{}
		stack := []int{from}
		for len(stack) > 0 {
			i := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if i == to {
				return true
			}
			if seen[i] {
				continue
			}
			seen[i] = true
			for _, d := range p.Tasks[i].DependsOn {
				stack = append(stack, refs[d])
			}
		}
		return false
	}
	total, ok := 0, 0
	for _, r := range f.Requirements {
		for _, a := range r.After {
			ti, c1 := cov[r.ID]
			tj, c2 := cov[a]
			if !c1 || !c2 {
				continue
			}
			total++
			if reaches(ti, tj) {
				ok++
			} else {
				*notes = append(*notes, fmt.Sprintf("requirement %s should come after %s but %q does not depend on %q", r.ID, a, p.Tasks[ti].Ref, p.Tasks[tj].Ref))
			}
		}
	}
	if total == 0 {
		return 1
	}
	return float64(ok) / float64(total)
}

func sizeScore(f *PlanFixture, p *planOutput, notes *[]string) float64 {
	count := 1.0
	if n := len(p.Tasks); n < f.MinTasks || n > f.MaxTasks {
		count = 0
		*notes = append(*notes, fmt.Sprintf("%d tasks, expected %d to %d", n, f.MinTasks, f.MaxTasks))
	}
	sizes := make([]float64, 0, len(p.Tasks))
	for _, t := range p.Tasks {
		v := float64(t.Budget.MaxMinutes)
		if v <= 0 {
			v = float64(len(t.Spec)) / 20
		}
		sizes = append(sizes, max(v, 1))
	}
	balance := 1.0
	if lo, hi := slices.Min(sizes), slices.Max(sizes); hi/lo > 6 {
		balance = 6 * lo / hi
		*notes = append(*notes, fmt.Sprintf("unbalanced task sizes: largest is %.0fx the smallest", hi/lo))
	}
	return 0.5*count + 0.5*balance
}

func budgetScore(f *PlanFixture, p *planOutput, notes *[]string) float64 {
	set := 0
	var sum float64
	for _, t := range p.Tasks {
		if t.Budget.MaxMinutes > 0 && t.Budget.MaxCostEUR > 0 {
			set++
		} else {
			*notes = append(*notes, fmt.Sprintf("task %q has no complete budget", t.Ref))
		}
		sum += max(t.Budget.MaxCostEUR, 0)
	}
	score := float64(set) / float64(len(p.Tasks))
	if f.MaxCostEUR > 0 && sum > f.MaxCostEUR+1e-9 {
		*notes = append(*notes, fmt.Sprintf("task budgets sum to %.2f EUR, over the brief budget of %.2f EUR", sum, f.MaxCostEUR))
		score *= 0.5
	}
	return score
}

func clamp(v float64) float64 {
	return min(max(v, 0), 1)
}
