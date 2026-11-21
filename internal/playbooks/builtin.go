package playbooks

import (
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/planlint"
)

func Builtin() []Playbook {
	return []Playbook{
		{
			Name: "feature", Description: "New functionality with tests and review",
			Rules: planlint.Rules{
				Coverage:          planlint.CoverageTesterOrReviewer,
				CriteriaRules:     []planlint.CriteriaRule{{Name: "tests", Pattern: "test|verif|assert", Role: model.RoleWorker, Severity: planlint.SeverityWarning}},
				AllowedSizes:      []string{"s", "m", "l"},
				DefaultTaskBudget: model.Budget{MaxMinutes: 120, MaxCostEUR: 5},
				CrossReview:       true,
				Instructions:      "Feature work: every implementation task needs a tester task or cross review, an acceptance criterion that says how it is tested, and a size of at most l (480 minutes). Split anything larger.",
			},
		},
		{
			Name: "hotfix", Description: "Urgent bug fix with a rollback path and a regression test",
			Rules: planlint.Rules{
				Coverage:            planlint.CoverageTester,
				RequiredTasks:       []planlint.RequiredTask{{Name: "rollback", Pattern: "roll ?back|revert", Severity: planlint.SeverityError}},
				CriteriaRules:       []planlint.CriteriaRule{{Name: "regression_test", Pattern: "regression", Role: model.RoleWorker, Severity: planlint.SeverityError}},
				AllowedSizes:        []string{"s", "m"},
				DefaultTaskBudget:   model.Budget{MaxMinutes: 60, MaxCostEUR: 3},
				BriefCostMultiplier: 0.5,
				CrossReview:         true,
				Instructions:        "Hotfix: keep the change minimal. The plan must contain a rollback task, every implementation task needs a regression test criterion and a tester task that depends on it, and tasks stay at size s or m (180 minutes).",
			},
		},
		{
			Name: "refactor", Description: "Behaviour-preserving restructuring",
			Rules: planlint.Rules{
				Coverage:          planlint.CoverageTesterOrReviewer,
				CriteriaRules:     []planlint.CriteriaRule{{Name: "behaviour_unchanged", Pattern: "unchanged|no behaviou?r change|existing tests|same behaviou?r", Role: model.RoleWorker, Severity: planlint.SeverityError}},
				AllowedSizes:      []string{"s", "m", "l"},
				DefaultTaskBudget: model.Budget{MaxMinutes: 120, MaxCostEUR: 5},
				CrossReview:       true,
				Instructions:      "Refactor: behaviour must not change. Every implementation task needs an acceptance criterion stating that behaviour is unchanged or that the existing tests still pass. Prefer many small steps over one large one.",
			},
		},
		{
			Name: "security-audit", Description: "Security review with findings and fixes",
			Rules: planlint.Rules{
				Coverage:            planlint.CoverageTesterOrReviewer,
				RequiredTasks:       []planlint.RequiredTask{{Name: "findings_report", Pattern: "report|findings|summary", Severity: planlint.SeverityWarning}},
				CriteriaRules:       []planlint.CriteriaRule{{Name: "threat", Pattern: "threat|vulnerab|exploit|injection|authz|secret|permission", Role: model.RoleWorker, Severity: planlint.SeverityWarning}},
				AllowedSizes:        []string{"s", "m"},
				DefaultTaskBudget:   model.Budget{MaxMinutes: 90, MaxCostEUR: 4},
				BriefCostMultiplier: 1.5,
				CrossReview:         true,
				Instructions:        "Security audit: plan a findings report task, name the threat or vulnerability class each task covers in its acceptance criteria, never put secrets or exploit details that expose live systems in tasks or notes, and keep tasks at size s or m.",
			},
		},
		{
			Name: "performance", Description: "Measured performance improvement",
			Rules: planlint.Rules{
				Coverage:            planlint.CoverageTesterOrReviewer,
				RequiredTasks:       []planlint.RequiredTask{{Name: "baseline", Pattern: "baseline|benchmark", Severity: planlint.SeverityWarning}},
				CriteriaRules:       []planlint.CriteriaRule{{Name: "measurement", Pattern: "benchmark|p95|p99|latency|throughput|ops/s|profil", Role: model.RoleWorker, Severity: planlint.SeverityError}},
				AllowedSizes:        []string{"s", "m", "l"},
				DefaultTaskBudget:   model.Budget{MaxMinutes: 120, MaxCostEUR: 6},
				BriefCostMultiplier: 1.25,
				CrossReview:         true,
				Instructions:        "Performance: plan a baseline measurement first. Every implementation task needs a measurable acceptance criterion (benchmark, latency percentile or throughput target) and must report before and after numbers.",
			},
		},
	}
}
