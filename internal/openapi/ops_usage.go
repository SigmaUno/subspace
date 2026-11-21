package openapi

import (
	"reflect"
	"time"

	"github.com/SigmaUno/subspace/internal/breaker"
	"github.com/SigmaUno/subspace/internal/usage"
)

func init() {
	componentNames[reflect.TypeFor[breaker.ProbeResult]()] = "ProviderProbe"
	componentNames[reflect.TypeFor[usage.Summary]()] = "UsageSummary"
	componentNames[reflect.TypeFor[usage.Group]()] = "UsageGroup"
	componentNames[reflect.TypeFor[usage.Series]()] = "UsageSeries"
	componentNames[reflect.TypeFor[usage.Point]()] = "UsagePoint"
	componentNames[reflect.TypeFor[usage.Credits]()] = "UsageCredits"
	componentNames[reflect.TypeFor[usage.BriefBudget]()] = "UsageBriefBudget"
	componentNames[reflect.TypeFor[usage.ProviderStatus]()] = "UsageProviderStatus"
	componentNames[reflect.TypeFor[usage.CapacityReport]()] = "UsageCapacity"
	componentNames[reflect.TypeFor[usage.ModelCapacity]()] = "UsageModelCapacity"

	const filters = "from to agent_id brief_id task_id provider model source"
	Register(
		get("/api/v1/providers", "Provider probe results per capacity limit", list[breaker.ProbeResult]("providers")),
		get("/api/v1/usage/summary", "Usage totals grouped by agent, task, brief, model, provider or source", usage.Summary{}).Q("group_by "+filters),
		get("/api/v1/usage/series", "Usage time series", obj("from", time.Time{}, "to", time.Time{}, "bucket", "", "group_by", "", "series", []usage.Series{})).Q("bucket group_by "+filters),
		get("/api/v1/usage/providers", "Provider request and rate-limit status", list[usage.ProviderStatus]("providers")),
		get("/api/v1/usage/capacity", "Live capacity with headroom per provider and model", usage.CapacityReport{}),
	)
}
