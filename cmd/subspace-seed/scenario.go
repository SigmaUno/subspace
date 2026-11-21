package main

import (
	"github.com/SigmaUno/subspace/internal/components"
	"github.com/SigmaUno/subspace/internal/model"
)

type agentSpec struct {
	ID, Role, Kind, Model string
}

type briefSpec struct {
	Title       string
	Body        string
	Repo        string
	MaxCost     float64
	MaxParallel int
	Providers   []string
	Components  []components.Spec
	Approve     bool
}

type questionSpec struct {
	Text    string
	Options []string
	Answer  string
}

type taskSpec struct {
	Ref      string
	Brief    int
	Title    string
	Spec     string
	Criteria []string
	Role     string
	Comp     int
	Deps     []string
	Worker   string
	Target   string
	Reviews  []string
	Priority int
	Late     bool
	Block    string
	Question *questionSpec
}

type artifactSpec struct {
	Scope string
	Ref   string
	By    string
	Name  string
	Type  string
}

const (
	changes  = "changes_requested"
	approve  = "approve"
	leaderID = "rex"
)

var agentSpecs = []agentSpec{
	{leaderID, model.RoleLeader, model.KindClaudeCode, "claude-opus-4-5"},
	{"claude-1", model.RoleWorker, model.KindClaudeCode, "claude-sonnet-5-5"},
	{"claude-2", model.RoleWorker, model.KindClaudeCode, "claude-sonnet-5-5"},
	{"codex-1", model.RoleWorker, model.KindCodex, "gpt-5-codex"},
	{"codex-2", model.RoleWorker, model.KindCodex, "gpt-5-codex"},
	{"oc-1", model.RoleWorker, model.KindOpenCode, "deepseek-chat"},
	{"oc-2", model.RoleWorker, model.KindOpenCode, "deepseek-chat"},
	{"oc-test-1", model.RoleTester, model.KindOpenCode, "deepseek-chat"},
	{"codex-test-1", model.RoleTester, model.KindCodex, "gpt-5-mini"},
	{"rev-claude", model.RoleReviewer, model.KindClaudeCode, "claude-opus-4-5"},
	{"rev-claude-2", model.RoleReviewer, model.KindClaudeCode, "claude-sonnet-5-5"},
	{"rev-codex", model.RoleReviewer, model.KindCodex, "gpt-5-codex"},
	{"rev-oc", model.RoleReviewer, model.KindOpenCode, "deepseek-reasoner"},
}

var briefSpecs = []briefSpec{
	{
		Title: "Webhooks: PR status sync",
		Body: "Keep task status in step with pull requests.\n\n" +
			"- Verify GitHub and Forgejo signatures before touching any state.\n" +
			"- A merged PR never marks a task done on its own unless the brief opts in.\n" +
			"- Deliveries are idempotent: replays have no effect.",
		Repo: "SigmaUno/subspace", MaxCost: 120, MaxParallel: 8, Approve: true,
		Components: []components.Spec{
			{Name: "store", Description: "delivery log and task lookups", Paths: []string{"internal/store"}},
			{Name: "webhook", Description: "signed PR event handler", Paths: []string{"internal/webhook"}, DependsOn: []string{"store"}},
			{Name: "mirror", Description: "issue mirroring into task threads", Paths: []string{"internal/mirror"}, DependsOn: []string{"store"}},
			{Name: "api", Description: "owner-facing endpoints and events", Paths: []string{"internal/api"}, DependsOn: []string{"webhook", "mirror"}},
		},
	},
	{
		Title: "Dashboard performance pass",
		Body: "The board gets sluggish past 300 tasks.\n\n" +
			"- First paint under 1.5s with 500 tasks.\n" +
			"- No full re-render on a single SSE update.\n" +
			"- Keep the chart bundle out of the initial load.",
		Repo: "SigmaUno/subspace-web", MaxCost: 60, MaxParallel: 6, Providers: []string{"anthropic", "openai"}, Approve: true,
		Components: []components.Spec{
			{Name: "board", Description: "kanban columns and cards", Paths: []string{"web/src/board"}},
			{Name: "charts", Description: "usage and cost charts", Paths: []string{"web/src/charts"}},
			{Name: "api-client", Description: "fetch layer and SSE handling", Paths: []string{"web/src/api"}},
		},
	},
	{
		Title: "Self-hosted inference gateway",
		Body: "A small gateway in front of our own inference pool.\n\n" +
			"- Route by model and provider health.\n" +
			"- Per-key token quotas.\n" +
			"- Prometheus metrics for queue depth and latency.",
		Repo: "SigmaUno/inference-gw", MaxCost: 200, MaxParallel: 6,
		Components: []components.Spec{
			{Name: "router", Description: "model routing and failover", Paths: []string{"internal/router"}},
			{Name: "quota", Description: "per-key token accounting", Paths: []string{"internal/quota"}, DependsOn: []string{"router"}},
			{Name: "metrics", Description: "prometheus exporters", Paths: []string{"internal/metrics"}},
		},
	},
}

var taskSpecs = []taskSpec{
	{Ref: "w1", Brief: 0, Title: "Add webhook_deliveries table and store methods", Role: model.RoleWorker, Comp: 1, Worker: "claude-1", Target: model.StatusDone, Reviews: []string{approve}, Priority: 3,
		Spec:     "Persist every webhook delivery id with its source, received time and outcome. Provide Seen and Record on the store interface.",
		Criteria: []string{"Migration is idempotent", "Seen returns true after Record", "Covered by a store integration test"}},
	{Ref: "w2", Brief: 0, Title: "Verify HMAC signatures for GitHub and Forgejo", Role: model.RoleWorker, Comp: 2, Deps: []string{"w1"}, Worker: "codex-1", Target: model.StatusDone, Reviews: []string{changes, approve}, Priority: 3,
		Spec:     "Validate X-Hub-Signature-256 and X-Forgejo-Signature with a constant-time comparison. Reject bodies over 1 MiB.",
		Criteria: []string{"Bad or missing signature returns 401", "Comparison is constant-time", "Oversized bodies return 413"}},
	{Ref: "w3", Brief: 0, Title: "Map PR opened and merged events to task status", Role: model.RoleWorker, Comp: 2, Deps: []string{"w1", "w2"}, Worker: "oc-1", Target: model.StatusApproved, Reviews: []string{approve}, Priority: 2,
		Spec:     "A PR opened for a task branch moves the task to submitted. A merged PR is recorded on the task and surfaces as a merge decision.",
		Criteria: []string{"Unknown branches are ignored", "Merge never marks done unless auto_done_on_merge is set"}},
	{Ref: "w4", Brief: 0, Title: "Replay protection for delivery ids", Role: model.RoleWorker, Comp: 2, Deps: []string{"w1"}, Worker: "claude-2", Target: model.StatusInReview, Priority: 2,
		Spec:     "A replayed delivery id answers 202 without effect.",
		Criteria: []string{"Replay changes nothing", "Concurrent duplicates are applied once"}},
	{Ref: "w5", Brief: 0, Title: "Auto-done on merge toggle per brief", Role: model.RoleWorker, Comp: 4, Deps: []string{"w3"}, Worker: "codex-2", Target: model.StatusAssigned,
		Spec:     "Expose auto_done_on_merge in the brief settings and honour it in the merge handler.",
		Criteria: []string{"Default is off", "Toggle is audited"}},
	{Ref: "w6", Brief: 0, Title: "Webhook integration tests with signed fixtures", Role: model.RoleTester, Comp: 2, Deps: []string{"w2"}, Worker: "oc-test-1", Target: model.StatusInProgress,
		Spec:     "Drive the handler over HTTP with recorded GitHub and Forgejo payloads signed at test time.",
		Criteria: []string{"Both providers covered", "Tampered payloads are rejected"}},
	{Ref: "w7", Brief: 0, Title: "Issue mirroring to task threads", Role: model.RoleWorker, Comp: 3, Deps: []string{"w1"}, Worker: "oc-2", Target: model.StatusInProgress, Priority: 1,
		Spec:     "Mirror issue comments for linked issues into the task thread as notes.",
		Criteria: []string{"Edits update the mirrored note", "Bot comments are skipped"},
		Question: &questionSpec{Text: "Should edited GitHub issues overwrite the task spec or only append a note?", Options: []string{"overwrite", "append a note"}, Answer: "Append a note. The spec stays as the owner approved it."}},
	{Ref: "w8", Brief: 0, Title: "Mirror conflict resolution on edited issues", Role: model.RoleWorker, Comp: 3, Deps: []string{"w7"}, Target: model.StatusReady,
		Spec:     "When an issue and its mirrored note diverge, keep the newest edit and record the loser in the thread.",
		Criteria: []string{"No data is dropped silently"}},
	{Ref: "w9", Brief: 0, Title: "Document webhook setup in docs/api.md", Role: model.RoleWorker, Comp: 4, Deps: []string{"w3"}, Target: model.StatusReady,
		Spec:     "Describe the secrets, endpoints, headers and delivery semantics operators need.",
		Criteria: []string{"Every env var is listed", "Examples run as written"}},
	{Ref: "w10", Brief: 0, Title: "Backfill missed deliveries after downtime", Role: model.RoleWorker, Comp: 1, Deps: []string{"w1"}, Worker: "codex-1", Target: model.StatusSubmitted,
		Spec:     "On startup, list recent PRs for linked repos and apply any event not in the delivery log.",
		Criteria: []string{"Backfill is bounded to 24h", "Already seen deliveries are skipped"}},
	{Ref: "w11", Brief: 0, Title: "Rate limit webhook endpoint per source IP", Role: model.RoleWorker, Comp: 4, Deps: []string{"w1"}, Worker: "claude-1", Target: model.StatusChangesRequested, Reviews: []string{changes},
		Spec:     "Token bucket per source address with a 429 and Retry-After when exceeded.",
		Criteria: []string{"Limits are configurable", "Trusted proxy headers are honoured"}},
	{Ref: "w12", Brief: 0, Title: "Stream webhook delivery events over SSE", Role: model.RoleWorker, Comp: 4, Deps: []string{"w1"}, Worker: "oc-1", Target: model.StatusBlocked, Priority: 1,
		Block:    "Waiting on an owner decision about how long delivery history is kept.",
		Spec:     "Publish delivery outcomes on the events stream so the UI can show a live feed.",
		Criteria: []string{"Events are coalesced under load", "Payloads carry no secrets"},
		Question: &questionSpec{Text: "How long should webhook delivery history be kept?", Options: []string{"7 days", "30 days", "configurable"}}},
	{Ref: "w14", Brief: 0, Title: "Fuzz the signature header parser", Role: model.RoleWorker, Comp: 2, Deps: []string{"w2"}, Worker: "claude-2", Target: model.StatusNeedsOwner, Reviews: []string{changes, changes, changes},
		Spec:     "Add a native Go fuzz target for header parsing and fix anything it finds.",
		Criteria: []string{"Corpus is committed", "No panics after 60s of fuzzing"}},
	{Ref: "w15", Brief: 0, Title: "Support Bitbucket webhooks", Role: model.RoleWorker, Comp: 2, Target: model.StatusCancelled,
		Spec:     "Accept Bitbucket Cloud push and PR events.",
		Criteria: []string{"Signatures verified"}},
	{Ref: "w13", Brief: 0, Title: "Hotfix: reject oversized webhook bodies early", Role: model.RoleWorker, Comp: 2, Target: model.StatusReady, Late: true, Priority: 5,
		Spec:     "Stop reading at 1 MiB and answer 413 before any parsing happens.",
		Criteria: []string{"Memory stays flat under a 1 GiB upload"}},

	{Ref: "d1", Brief: 1, Title: "Virtualize the kanban board columns", Role: model.RoleWorker, Comp: 1, Worker: "claude-1", Target: model.StatusDone, Reviews: []string{approve}, Priority: 3,
		Spec:     "Render only visible cards in each column with a fixed overscan.",
		Criteria: []string{"500 cards scroll at 60fps", "Keyboard navigation still works"}},
	{Ref: "d2", Brief: 1, Title: "Coalesce SSE updates on the client", Role: model.RoleWorker, Comp: 3, Worker: "codex-1", Target: model.StatusDone, Reviews: []string{changes, approve}, Priority: 3,
		Spec:     "Batch task.updated events per animation frame and apply them in one state update.",
		Criteria: []string{"One render per frame under a 200 events/s burst", "Order of updates is preserved"}},
	{Ref: "d3", Brief: 1, Title: "Memoize card rendering", Role: model.RoleWorker, Comp: 1, Deps: []string{"d1"}, Worker: "codex-2", Target: model.StatusApproved, Reviews: []string{approve}, Priority: 2,
		Spec:     "Cards re-render only when their own task changes.",
		Criteria: []string{"Profiler shows no re-render on unrelated updates"}},
	{Ref: "d4", Brief: 1, Title: "Lazy-load the chart bundle", Role: model.RoleWorker, Comp: 2, Worker: "claude-2", Target: model.StatusInProgress, Priority: 2,
		Spec:     "Split the charts into a dynamic import loaded when the usage tab opens.",
		Criteria: []string{"Initial JS shrinks by at least 120 KB gzip", "A skeleton shows while loading"},
		Question: &questionSpec{Text: "Is a 250 KB gzip budget for the chart bundle acceptable?", Options: []string{"yes", "no, tighter"}, Answer: "Yes, 250 KB gzip is fine."}},
	{Ref: "d5", Brief: 1, Title: "Add a Lighthouse CI budget", Role: model.RoleTester, Comp: 2, Deps: []string{"d4"}, Worker: "codex-test-1", Target: model.StatusAssigned,
		Spec:     "Fail CI when first contentful paint or total JS regress beyond the agreed budget.",
		Criteria: []string{"Budget lives in the repo", "CI output links the report"}},
	{Ref: "d6", Brief: 1, Title: "Cache the agent list with stale-while-revalidate", Role: model.RoleWorker, Comp: 3, Deps: []string{"d2"}, Worker: "claude-1", Target: model.StatusSubmitted,
		Spec:     "Serve the cached agent list immediately and refresh in the background.",
		Criteria: []string{"No flash of empty state", "Cache is dropped on logout"}},
	{Ref: "d7", Brief: 1, Title: "Profile first paint with 500 tasks", Role: model.RoleTester, Comp: 1, Deps: []string{"d1"}, Worker: "codex-test-1", Target: model.StatusInProgress,
		Spec:     "Script a seeded board of 500 tasks and record first paint over 10 runs.",
		Criteria: []string{"Results are reproducible within 10%"}},
	{Ref: "d8", Brief: 1, Title: "Skeleton states for slow endpoints", Role: model.RoleWorker, Comp: 3, Target: model.StatusReady,
		Spec:     "Show skeletons when a request takes longer than 200ms.",
		Criteria: []string{"No layout shift when data arrives"}},
	{Ref: "d9", Brief: 1, Title: "Reduce re-renders in the thread view", Role: model.RoleWorker, Comp: 3, Deps: []string{"d2"}, Worker: "claude-2", Target: model.StatusNeedsOwner, Reviews: []string{changes, changes, changes},
		Spec:     "Appending a message must not re-render earlier messages.",
		Criteria: []string{"Profiler shows one message rendered per append"},
		Question: &questionSpec{Text: "The thread fix needs a small state library. May we add zustand?", Options: []string{"yes", "no"}}},

	{Ref: "g1", Brief: 2, Title: "Route requests by model and provider health", Role: model.RoleWorker, Comp: 1, Priority: 3,
		Spec:     "Pick a backend per request from model name and a rolling health score.",
		Criteria: []string{"Unhealthy backends are skipped within 5s", "Routing decisions are logged"}},
	{Ref: "g2", Brief: 2, Title: "Per-key token quota with a sliding window", Role: model.RoleWorker, Comp: 2, Deps: []string{"g1"},
		Spec:     "Count prompt and completion tokens per API key over a sliding hour.",
		Criteria: []string{"Quota errors are 429 with Retry-After", "Counters survive restarts"}},
	{Ref: "g3", Brief: 2, Title: "Expose Prometheus metrics for queue depth", Role: model.RoleWorker, Comp: 3,
		Spec:     "Export queue depth, in-flight requests and latency histograms per backend.",
		Criteria: []string{"Names follow the project convention"}},
	{Ref: "g4", Brief: 2, Title: "Fail over to the secondary pool on 5xx", Role: model.RoleWorker, Comp: 1, Deps: []string{"g1"},
		Spec:     "Retry once on a different pool when a backend answers 5xx before the first token.",
		Criteria: []string{"Streams already started are never retried"}},
	{Ref: "g5", Brief: 2, Title: "Streaming passthrough for SSE completions", Role: model.RoleWorker, Comp: 1, Deps: []string{"g1"},
		Spec:     "Forward server-sent events without buffering.",
		Criteria: []string{"Time to first token is within 20ms of the backend"}},
}

var artifactSpecs = []artifactSpec{
	{Scope: "project", Ref: "0", By: leaderID, Name: "architecture-overview.md", Type: "text/markdown"},
	{Scope: "project", Ref: "1", By: leaderID, Name: "perf-baseline.json", Type: "application/json"},
	{Scope: "project", Ref: "2", By: leaderID, Name: "gateway-requirements.md", Type: "text/markdown"},
	{Scope: "task", Ref: "w2", By: "codex-1", Name: "signature-fixtures.json", Type: "application/json"},
	{Scope: "task", Ref: "w6", By: "oc-test-1", Name: "webhook-test-run.log", Type: "text/plain"},
	{Scope: "task", Ref: "d4", By: "claude-2", Name: "bundle-analysis.txt", Type: "text/plain"},
}

var followUps = []taskSpec{
	{Brief: 0, Title: "Expose webhook delivery metrics", Role: model.RoleWorker, Comp: 4,
		Spec:     "Count deliveries by source and outcome and publish them on the metrics endpoint.",
		Criteria: []string{"Labels have bounded cardinality"}},
	{Brief: 1, Title: "Prefetch task details on card hover", Role: model.RoleWorker, Comp: 1,
		Spec:     "Warm the task detail cache when the pointer rests on a card for 150ms.",
		Criteria: []string{"No request for quick fly-overs"}},
	{Brief: 0, Title: "Retry failed webhook processing with backoff", Role: model.RoleWorker, Comp: 2,
		Spec:     "Failed deliveries are retried with exponential backoff and a hard cap.",
		Criteria: []string{"Gives up after 6 attempts", "Retries are visible in the delivery log"}},
	{Brief: 1, Title: "Persist board filters in the URL", Role: model.RoleWorker, Comp: 1,
		Spec:     "Keep the selected agent, status and brief filters in query parameters.",
		Criteria: []string{"Back and forward restore filters"}},
	{Brief: 0, Title: "Soak test mirror sync for 30 minutes", Role: model.RoleTester, Comp: 3,
		Spec:     "Run the mirror against a noisy fixture repo and check memory and delivery lag.",
		Criteria: []string{"Memory stays under 150 MB", "Lag stays under 10s"}},
	{Brief: 1, Title: "Trim the unused icon set from the bundle", Role: model.RoleWorker, Comp: 2,
		Spec:     "Import icons individually so the bundler can drop the rest.",
		Criteria: []string{"Bundle shrinks and no icon is missing"}},
}
