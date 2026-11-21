package briefs

import "github.com/SigmaUno/subspace/internal/model"

func qualityGates(n *node) (*model.QualityGates, error) {
	m, err := mapping(n, "quality_gates", "tests", "min_tests_passed", "checks", "scope", "questions", "review", "budget", "max_pr_age_hours")
	if err != nil {
		return nil, err
	}
	var q model.QualityGates
	flags := []struct {
		key string
		dst *bool
	}{{"tests", &q.Tests}, {"checks", &q.Checks}, {"scope", &q.Scope}, {"questions", &q.Questions}, {"review", &q.Review}, {"budget", &q.Budget}}
	for _, f := range flags {
		if e, ok := m[f.key]; ok {
			if *f.dst, err = boolean(e.val, "quality_gates."+f.key); err != nil {
				return nil, err
			}
		}
	}
	if e, ok := m["min_tests_passed"]; ok {
		if q.MinTestsPassed, err = integer(e.val, "quality_gates.min_tests_passed"); err != nil {
			return nil, err
		}
		if q.MinTestsPassed > 0 && !q.Tests {
			return nil, errAt(e.pos, "quality_gates.min_tests_passed needs tests: true")
		}
	}
	if e, ok := m["max_pr_age_hours"]; ok {
		if q.MaxPRAgeHours, err = number(e.val, "quality_gates.max_pr_age_hours"); err != nil {
			return nil, err
		}
		if q.MaxPRAgeHours > 8760 {
			return nil, errAt(e.val.pos, "quality_gates.max_pr_age_hours must be at most 8760")
		}
	}
	return &q, nil
}
