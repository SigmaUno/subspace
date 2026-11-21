package main

import (
	"github.com/SigmaUno/subspace/internal/artifacts"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/briefs"
	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/components"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/forecast"
	"github.com/SigmaUno/subspace/internal/hillclimb"
	"github.com/SigmaUno/subspace/internal/instructions"
	"github.com/SigmaUno/subspace/internal/lifecycle"
	"github.com/SigmaUno/subspace/internal/mergequeue"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/prompts"
	"github.com/SigmaUno/subspace/internal/report"
	"github.com/SigmaUno/subspace/internal/search"
	"github.com/SigmaUno/subspace/internal/usage"
)

var registry = []entry{
	{"Agent", model.Agent{}},
	{"Artifact", artifacts.Artifact{}},
	{"AuditCheck", model.AuditCheck{}},
	{"AuditEvent", model.AuditEvent{}},
	{"AuditHead", model.AuditHead{}},
	{"Brief", model.Brief{}},
	{"BriefBudget", model.BriefBudget{}},
	{"BriefComponentSpec", components.Spec{}},
	{"BriefPreview", briefs.Doc{}},
	{"BriefReport", report.Report{}},
	{"BriefTaskBudget", briefs.TaskBudget{}},
	{"BriefTemplate", briefs.Template{}},
	{"Candidate", mergequeue.Candidate{}},
	{"CapacityConfig", capacity.Config{}},
	{"CapacityLimit", capacity.Report{}},
	{"CapacityLimitConfig", capacity.Limit{}},
	{"CapacityView", board.CapacityView{}},
	{"CostBreakdown", usage.CostBreakdown{}},
	{"CostGroup", usage.CostGroup{}},
	{"CostPart", usage.CostPart{}},
	{"CostTotals", usage.CostTotals{}},
	{"Decision", decisions.Decision{}},
	{"DecisionLinks", decisions.Links{}},
	{"DecisionOutcome", decisions.Outcome{}},
	{"Forecast", forecast.Forecast{}},
	{"ForecastBurn", forecast.Burn{}},
	{"ForecastCost", forecast.Cost{}},
	{"ForecastCounts", forecast.Counts{}},
	{"ForecastRange", forecast.Range{}},
	{"ForecastRisk", forecast.Risk{}},
	{"ForecastSchedule", forecast.Schedule{}},
	{"ForecastTimeRange", forecast.TimeRange{}},
	{"HillclimbAttempt", hillclimb.Attempt{}},
	{"HillclimbJob", hillclimb.Job{}},
	{"HillclimbPoint", hillclimb.Point{}},
	{"InstructionDiffLine", instructions.DiffLine{}},
	{"InstructionEffective", instructions.Effective{}},
	{"InstructionMeta", instructions.Meta{}},
	{"InstructionVersion", instructions.Version{}},
	{"Message", model.Message{}},
	{"Plan", model.Plan{}},
	{"PlanFinding", model.PlanFinding{}},
	{"Prompt", prompts.Template{}},
	{"ProviderStatus", usage.ProviderStatus{}},
	{"QualityGates", model.QualityGates{}},
	{"Question", model.Question{}},
	{"ReportBucket", report.Bucket{}},
	{"ReportCost", report.Cost{}},
	{"ReportCriticalPath", report.CriticalPath{}},
	{"ReportEfficiency", report.Efficiency{}},
	{"ReportOutlier", report.Outlier{}},
	{"ReportReview", report.Review{}},
	{"ReportStep", report.Step{}},
	{"ReportThroughput", report.Throughput{}},
	{"SearchResult", search.Result{}},
	{"Spawn", model.Spawn{}},
	{"SpawnLifecycle", lifecycle.Spawn{}},
	{"Task", model.Task{}},
	{"TaskBudget", model.Budget{}},
	{"TaskReport", report.TaskReport{}},
	{"TestResult", model.TestResult{}},
	{"UsageBriefBudget", usage.BriefBudget{}},
	{"UsageCredits", usage.Credits{}},
	{"UsageGroup", usage.Group{}},
	{"UsagePoint", usage.Point{}},
	{"UsageSeries", usage.Series{}},
	{"UsageSummary", usage.Summary{}},
	{"UsageTotals", usage.Totals{}},
}
