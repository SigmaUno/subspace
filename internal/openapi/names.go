package openapi

import (
	"reflect"

	"github.com/SigmaUno/subspace/internal/agentstats"
	"github.com/SigmaUno/subspace/internal/artifacts"
	"github.com/SigmaUno/subspace/internal/backup"
	"github.com/SigmaUno/subspace/internal/breaker"
	"github.com/SigmaUno/subspace/internal/briefs"
	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/components"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/forecast"
	"github.com/SigmaUno/subspace/internal/helpreq"
	"github.com/SigmaUno/subspace/internal/hillclimb"
	"github.com/SigmaUno/subspace/internal/instructions"
	"github.com/SigmaUno/subspace/internal/lifecycle"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/prompts"
	"github.com/SigmaUno/subspace/internal/prsync"
	"github.com/SigmaUno/subspace/internal/report"
	"github.com/SigmaUno/subspace/internal/spendguard"
	"github.com/SigmaUno/subspace/internal/timeseries"
)

var componentNames = map[reflect.Type]string{
	reflect.TypeFor[agentstats.Stats]():       "AgentStats",
	reflect.TypeFor[agentstats.TrendPoint]():  "AgentStatsTrendPoint",
	reflect.TypeFor[backup.Info]():            "Backup",
	reflect.TypeFor[breaker.Status]():         "Breaker",
	reflect.TypeFor[briefs.Doc]():             "BriefPreview",
	reflect.TypeFor[briefs.Template]():        "BriefTemplate",
	reflect.TypeFor[briefs.TaskBudget]():      "BriefTaskBudget",
	reflect.TypeFor[capacity.Config]():        "CapacityConfig",
	reflect.TypeFor[capacity.Limit]():         "CapacityLimitConfig",
	reflect.TypeFor[capacity.Report]():        "CapacityLimit",
	reflect.TypeFor[components.Component]():   "Component",
	reflect.TypeFor[components.Edge]():        "ArchitectureEdge",
	reflect.TypeFor[components.Graph]():       "ArchitectureGraph",
	reflect.TypeFor[components.Node]():        "ArchitectureNode",
	reflect.TypeFor[components.Owner]():       "ArchitectureOwner",
	reflect.TypeFor[components.Rollup]():      "ArchitectureRollup",
	reflect.TypeFor[components.Spec]():        "BriefComponentSpec",
	reflect.TypeFor[decisions.Links]():        "DecisionLinks",
	reflect.TypeFor[decisions.Outcome]():      "DecisionOutcome",
	reflect.TypeFor[forecast.Burn]():          "ForecastBurn",
	reflect.TypeFor[forecast.Cost]():          "ForecastCost",
	reflect.TypeFor[forecast.Counts]():        "ForecastCounts",
	reflect.TypeFor[forecast.Range]():         "ForecastRange",
	reflect.TypeFor[forecast.Risk]():          "ForecastRisk",
	reflect.TypeFor[forecast.Schedule]():      "ForecastSchedule",
	reflect.TypeFor[forecast.TimeRange]():     "ForecastTimeRange",
	reflect.TypeFor[helpreq.Proposed]():       "ProposedTask",
	reflect.TypeFor[helpreq.Request]():        "HelpRequest",
	reflect.TypeFor[hillclimb.Job]():          "HillclimbJob",
	reflect.TypeFor[hillclimb.Point]():        "HillclimbPoint",
	reflect.TypeFor[instructions.DiffLine]():  "InstructionDiffLine",
	reflect.TypeFor[instructions.Effective](): "InstructionEffective",
	reflect.TypeFor[instructions.Meta]():      "InstructionMeta",
	reflect.TypeFor[instructions.Version]():   "InstructionVersion",
	reflect.TypeFor[lifecycle.Spawn]():        "SpawnLifecycle",
	reflect.TypeFor[model.Budget]():           "TaskBudget",
	reflect.TypeFor[prompts.Template]():       "Prompt",
	reflect.TypeFor[prsync.Review]():          "PRReview",
	reflect.TypeFor[prsync.Status]():          "PRStatus",
	reflect.TypeFor[report.Bucket]():          "ReportBucket",
	reflect.TypeFor[report.Cost]():            "ReportCost",
	reflect.TypeFor[report.CriticalPath]():    "ReportCriticalPath",
	reflect.TypeFor[report.Outlier]():         "ReportOutlier",
	reflect.TypeFor[report.Report]():          "BriefReport",
	reflect.TypeFor[report.Review]():          "ReportReview",
	reflect.TypeFor[report.Step]():            "ReportStep",
	reflect.TypeFor[report.Throughput]():      "ReportThroughput",
	reflect.TypeFor[spendguard.Limits]():      "SpendCaps",
	reflect.TypeFor[spendguard.Status]():      "SpendWindow",
	reflect.TypeFor[spendguard.View]():        "SpendLimits",
	reflect.TypeFor[timeseries.Point]():       "TimeseriesPoint",
	reflect.TypeFor[timeseries.Series]():      "TimeseriesSeries",
	reflect.TypeFor[artifacts.Artifact]():     "Artifact",
}
