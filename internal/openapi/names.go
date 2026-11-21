package openapi

import (
	"reflect"

	"github.com/SigmaUno/subspace/internal/activity"
	"github.com/SigmaUno/subspace/internal/agentstats"
	"github.com/SigmaUno/subspace/internal/alerts"
	"github.com/SigmaUno/subspace/internal/artifacts"
	"github.com/SigmaUno/subspace/internal/backup"
	"github.com/SigmaUno/subspace/internal/breaker"
	"github.com/SigmaUno/subspace/internal/briefs"
	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/components"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/digest"
	"github.com/SigmaUno/subspace/internal/estimate"
	"github.com/SigmaUno/subspace/internal/forecast"
	"github.com/SigmaUno/subspace/internal/helpreq"
	"github.com/SigmaUno/subspace/internal/hillclimb"
	"github.com/SigmaUno/subspace/internal/insights"
	"github.com/SigmaUno/subspace/internal/instructions"
	"github.com/SigmaUno/subspace/internal/knowledge"
	"github.com/SigmaUno/subspace/internal/lifecycle"
	"github.com/SigmaUno/subspace/internal/logpipe"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/prompts"
	"github.com/SigmaUno/subspace/internal/prsync"
	"github.com/SigmaUno/subspace/internal/quota"
	"github.com/SigmaUno/subspace/internal/replay"
	"github.com/SigmaUno/subspace/internal/report"
	"github.com/SigmaUno/subspace/internal/spendguard"
	"github.com/SigmaUno/subspace/internal/timeseries"
	"github.com/SigmaUno/subspace/internal/tokens"
	"github.com/SigmaUno/subspace/internal/usage"
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
	reflect.TypeFor[estimate.Report]():        "EstimateReport",
	reflect.TypeFor[estimate.TaskEstimate]():  "TaskEstimate",
	reflect.TypeFor[estimate.Estimate]():      "Estimate",
	reflect.TypeFor[estimate.Stat]():          "EstimateStat",
	reflect.TypeFor[estimate.Level]():         "EstimateLevel",
	reflect.TypeFor[estimate.Range]():         "EstimateRange",
	reflect.TypeFor[estimate.Totals]():        "EstimateTotals",
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
	reflect.TypeFor[model.Agent]():            "AgentRecord",
	reflect.TypeFor[model.Budget]():           "TaskBudget",
	reflect.TypeFor[prompts.Template]():       "Prompt",
	reflect.TypeFor[prsync.Review]():          "PRReview",
	reflect.TypeFor[prsync.Status]():          "PRStatus",
	reflect.TypeFor[report.Bucket]():          "ReportBucket",
	reflect.TypeFor[report.Cost]():            "ReportCost",
	reflect.TypeFor[report.Efficiency]():      "ReportEfficiency",
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
	reflect.TypeFor[usage.CostBreakdown]():    "CostBreakdown",
	reflect.TypeFor[usage.CostGroup]():        "CostGroup",
	reflect.TypeFor[usage.CostPart]():         "CostPart",
	reflect.TypeFor[usage.CostTotals]():       "CostTotals",
	reflect.TypeFor[timeseries.Series]():      "TimeseriesSeries",
	reflect.TypeFor[artifacts.Artifact]():     "Artifact",
	reflect.TypeFor[Agent]():                  "Agent",
	reflect.TypeFor[activity.Item]():          "ActivityItem",
	reflect.TypeFor[activity.Page]():          "ActivityPage",
	reflect.TypeFor[activity.Ref]():           "ActivityRef",
	reflect.TypeFor[alerts.Alert]():           "Alert",
	reflect.TypeFor[digest.Decisions]():       "DigestDecisions",
	reflect.TypeFor[digest.Digest]():          "Digest",
	reflect.TypeFor[digest.Preview]():         "DigestPreview",
	reflect.TypeFor[digest.Risk]():            "DigestRisk",
	reflect.TypeFor[digest.Tasks]():           "DigestTasks",
	reflect.TypeFor[insights.Report]():        "InsightsReport",
	reflect.TypeFor[insights.Finding]():       "InsightFinding",
	reflect.TypeFor[insights.Evidence]():      "InsightEvidence",
	reflect.TypeFor[knowledge.Entry]():        "KnowledgeEntry",
	reflect.TypeFor[knowledge.Input]():        "KnowledgeInput",
	reflect.TypeFor[knowledge.Patch]():        "KnowledgePatch",
	reflect.TypeFor[logpipe.Line]():           "LogLine",
	reflect.TypeFor[notify.SinkResult]():      "NotifySinkResult",
	reflect.TypeFor[quota.Quota]():            "AgentQuota",
	reflect.TypeFor[quota.Status]():           "AgentQuotaStatus",
	reflect.TypeFor[quota.Today]():            "AgentQuotaUsage",
	reflect.TypeFor[replay.Agent]():           "ReplayAgent",
	reflect.TypeFor[replay.Change]():          "ReplayChange",
	reflect.TypeFor[replay.Diff]():            "ReplayDiff",
	reflect.TypeFor[replay.KeyEvent]():        "ReplayKeyEvent",
	reflect.TypeFor[replay.Point]():           "ReplayPoint",
	reflect.TypeFor[replay.Snapshot]():        "ReplaySnapshot",
	reflect.TypeFor[replay.Task]():            "ReplayTask",
	reflect.TypeFor[replay.Timeline]():        "ReplayTimeline",
	reflect.TypeFor[replay.Transition]():      "ReplayTransition",
	reflect.TypeFor[tokens.CreateInput]():     "APITokenInput",
	reflect.TypeFor[tokens.Created]():         "APITokenCreated",
	reflect.TypeFor[tokens.Token]():           "APIToken",
}
