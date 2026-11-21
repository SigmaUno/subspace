package openapi

import (
	"net/http"
	"time"

	"github.com/SigmaUno/subspace/internal/activity"
	"github.com/SigmaUno/subspace/internal/agentstats"
	"github.com/SigmaUno/subspace/internal/alerts"
	"github.com/SigmaUno/subspace/internal/artifacts"
	"github.com/SigmaUno/subspace/internal/autopilot"
	"github.com/SigmaUno/subspace/internal/backup"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/breaker"
	"github.com/SigmaUno/subspace/internal/briefs"
	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/chat"
	"github.com/SigmaUno/subspace/internal/components"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/digest"
	"github.com/SigmaUno/subspace/internal/forecast"
	"github.com/SigmaUno/subspace/internal/gates"
	"github.com/SigmaUno/subspace/internal/helpreq"
	"github.com/SigmaUno/subspace/internal/hillclimb"
	"github.com/SigmaUno/subspace/internal/instructions"
	"github.com/SigmaUno/subspace/internal/knowledge"
	"github.com/SigmaUno/subspace/internal/lifecycle"
	"github.com/SigmaUno/subspace/internal/logpipe"
	"github.com/SigmaUno/subspace/internal/mergequeue"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/profiles"
	"github.com/SigmaUno/subspace/internal/prompts"
	"github.com/SigmaUno/subspace/internal/prsync"
	"github.com/SigmaUno/subspace/internal/quota"
	"github.com/SigmaUno/subspace/internal/replay"
	"github.com/SigmaUno/subspace/internal/report"
	"github.com/SigmaUno/subspace/internal/search"
	"github.com/SigmaUno/subspace/internal/spendguard"
	"github.com/SigmaUno/subspace/internal/timeseries"
	"github.com/SigmaUno/subspace/internal/tokens"
)

const (
	mediaText     = "text/plain"
	mediaMarkdown = "text/markdown"
	mediaHTML     = "text/html"
	mediaNDJSON   = "application/x-ndjson"
	mediaSSE      = "text/event-stream"
	mediaBinary   = "application/octet-stream"
	mediaForm     = "application/x-www-form-urlencoded"
	mediaMultipt  = "multipart/form-data"
)

const (
	created = http.StatusCreated
	gone    = http.StatusNoContent
)

var operations = []Op{
	get("/healthz", "Liveness, version and pause flag", Health{}).Open(),
	post("/mcp", "MCP streamable HTTP endpoint (JSON-RPC 2.0). The bearer is an agent token; the owner token is rejected", RPCRequest{}, RPCResponse{}).H("Mcp-Session-Id"),
	del("/mcp", "End an MCP session", nil).H("Mcp-Session-Id!").S(gone),
	get("/a/{token}", "Render the confirmation page of an action link; never performs the action", "").As(mediaHTML).Open(),
	post("/a/{token}", "Perform the action of a one-time action link", nil, "").Body(mediaForm, ActionForm{}).As(mediaHTML).Open(),
	post("/hooks/github", "GitHub webhook receiver (HMAC signed); replayed deliveries answer 202", Free{}, Free{}).Signed("webhookSignature"),
	post("/hooks/forgejo", "Forgejo webhook receiver (HMAC signed); replayed deliveries answer 202", Free{}, Free{}).Signed("webhookSignature"),

	get("/api/v1/openapi.json", "This OpenAPI 3.1 document", Free{}),
	get("/api/v1/me", "Role and scope of the presented owner token", Me{}),
	get("/api/v1/state", "Counts, pause flag and version", board.StateInfo{}),
	post("/api/v1/pause-all", "Pause everything: revoke leases, block claims and spawns", Reason{}, Paused{}),
	post("/api/v1/resume-all", "Clear the global pause", Free{}, Paused{}),
	get("/api/v1/charter", "Read the owner charter", board.Charter{}),
	put("/api/v1/charter", "Replace the owner charter", CharterText{}, board.Charter{}),
	post("/api/v1/backup", "Create a verified database backup", Free{}, BackupResult{}),
	get("/api/v1/backups", "List backups, newest first", list[backup.Info]("backups")),
	get("/api/v1/limits", "Organization spend caps and their windows", spendguard.View{}),
	put("/api/v1/limits", "Replace the daily and weekly spend caps", spendguard.Limits{}, spendguard.View{}),
	get("/api/v1/metrics", "Prometheus metrics", "").As(mediaText),
	get("/api/v1/events", "Server-sent events stream", "").Q("topics last_event_id").As(mediaSSE),
	get("/api/v1/logs", "Recent redacted server log lines, ascending by id", list[logpipe.Line]("lines")).Q("level since limit:integer"),
	get("/api/v1/log-level", "Current server log level", LogLevel{}),
	put("/api/v1/log-level", "Change the server log level; needs owner scope", LogLevel{}, LogLevel{}),
	get("/api/v1/activity", "Activity feed, newest first", activity.Page{}).Q("since brief_id severity cursor limit:integer"),
	get("/api/v1/alerts", "Health alerts, active first", list[alerts.Alert]("alerts")).Q("status limit:integer"),

	post("/api/v1/briefs", "Create a brief", board.BriefInput{}, model.Brief{}).S(created),
	get("/api/v1/briefs", "List briefs", list[model.Brief]("briefs")).Q("status"),
	get("/api/v1/briefs/{id}", "Get a brief", model.Brief{}),
	patch("/api/v1/briefs/{id}", "Update a brief's budget, providers, flags or status", board.BriefPatch{}, model.Brief{}),
	post("/api/v1/briefs/{id}/leader", "Hand a brief to a leader; a null or empty agent_id means any leader", BriefLeader{}, model.Brief{}),
	get("/api/v1/briefs/{id}/board", "Kanban board of a brief", board.BoardView{}),
	get("/api/v1/briefs/{id}/architecture", "Component graph of a brief", components.Graph{}),
	put("/api/v1/briefs/{id}/components", "Replace the component plan of a brief", obj("components", []components.Spec{}), list[components.Component]("components")),
	get("/api/v1/briefs/{id}/forecast", "Cost and schedule forecast of a brief", forecast.Forecast{}),
	get("/api/v1/briefs/{id}/report", "Report of a brief", report.Report{}),
	get("/api/v1/briefs/{id}/report.md", "Report of a brief as markdown", "").As(mediaMarkdown),
	get("/api/v1/briefs/{id}/replay", "Board of a brief as of an audit id or time", replay.Snapshot{}).Q("at"),
	get("/api/v1/briefs/{id}/replay/timeline", "Status counts and key events over the life of a brief", replay.Timeline{}),
	get("/api/v1/briefs/{id}/replay/diff", "Task changes of a brief between two audit ids or times", replay.Diff{}).Q("from to"),
	post("/api/v1/briefs/import", "Import a brief document; the body is markdown or {\"markdown\": ...}", nil, ImportResult{}).
		Body(mediaMarkdown, "").Body(mediaJSON, obj("markdown", "")).Q("dry_run:boolean").S(created),
	get("/api/v1/brief-templates", "List brief templates", list[briefs.Template]("templates")),
	get("/api/v1/brief-templates/{id}/render", "Render a brief template; template variables are query parameters", obj("id", "", "markdown", "")),

	get("/api/v1/plans", "List plans", list[model.Plan]("plans")).Q("status brief_id"),
	get("/api/v1/plans/{id}", "Get a plan", model.Plan{}),
	post("/api/v1/plans/{id}/approve", "Approve a proposed plan", Note{}, model.Plan{}),
	post("/api/v1/plans/{id}/reject", "Reject a proposed plan", Reason{}, model.Plan{}),
	post("/api/v1/plans/{id}/tasks", "Add a backlog task to a proposed plan", board.TaskInput{}, model.Plan{}).S(created),
	del("/api/v1/plans/{id}/tasks/{taskId}", "Drop a backlog task from a proposed plan", model.Plan{}),

	get("/api/v1/tasks", "List tasks", list[model.Task]("tasks")).Q("brief_id status assignee role limit:integer"),
	get("/api/v1/tasks/{id}", "Get a task", model.Task{}),
	patch("/api/v1/tasks/{id}", "Edit a task or its budget", board.TaskEdit{}, model.Task{}),
	post("/api/v1/tasks/{id}/done", "Mark an approved task done after merging", Note{}, model.Task{}),
	post("/api/v1/tasks/{id}/cancel", "Cancel a task", Reason{}, model.Task{}),
	post("/api/v1/tasks/{id}/reopen", "Reopen a blocked, needs_owner or cancelled task", Reopen{}, model.Task{}),
	post("/api/v1/tasks/bulk", "Apply one action to many tasks", BulkRequest{}, BulkResponse{}),
	get("/api/v1/tasks/{id}/pr", "Latest pull request snapshot of a task", prsync.Status{}),
	get("/api/v1/tasks/{id}/gates", "Quality gate results of a task with evidence", gates.Report{}),
	post("/api/v1/tasks/{id}/gates/override", "Override failing quality gates of a task with a reason (audited)", gates.OverrideInput{}, gates.Report{}),
	get("/api/v1/prs", "Pull request snapshots", list[prsync.Status]("prs")).Q("brief_id state limit:integer"),

	get("/api/v1/agents", "List agents with their daily quota", list[Agent]("agents")).Q("role status"),
	get("/api/v1/agents/{id}", "Get an agent with its daily quota", Agent{}),
	post("/api/v1/agents", "Register an agent, optionally from a profile; the token is shown once", AgentRegister{}, board.Registered{}).S(created),
	post("/api/v1/agents/bulk", "Register many agents, optionally from a profile; tokens are shown once", AgentRegisterBulk{}, list[board.Registered]("agents")).S(created),
	post("/api/v1/agents/bulk-action", "Apply one action to many agents", BulkRequest{}, BulkResponse{}),
	get("/api/v1/agents/{id}/connect", "Connection snippets of an agent, without the token", board.Connect{}),
	post("/api/v1/agents/{id}/rotate-token", "Rotate an agent token; the new token is shown once", Expiry{}, board.Registered{}),
	post("/api/v1/agents/{id}/revoke", "Revoke an agent", Reason{}, model.Agent{}),
	post("/api/v1/agents/{id}/resume", "Clear an agent's pause", Free{}, model.Agent{}),
	get("/api/v1/agents/{id}/stats", "Performance statistics of an agent", agentstats.Stats{}),
	put("/api/v1/agents/{id}/quota", "Replace the daily quota of an agent", quota.Quota{}, quota.Status{}),
	post("/api/v1/agents/quota-bulk", "Apply one daily quota to many agents", QuotaBulk{}, QuotaBulkResponse{}),
	get("/api/v1/agent-stats", "Performance statistics of every agent", list[agentstats.Stats]("agents")).Q("role"),

	get("/api/v1/agent-profiles", "List agent profiles", list[profiles.Profile]("profiles")),
	post("/api/v1/agent-profiles", "Create an agent profile", ProfileInput{}, profiles.Profile{}).S(created),
	get("/api/v1/agent-profiles/{name}", "Get an agent profile", profiles.Profile{}),
	put("/api/v1/agent-profiles/{name}", "Replace an agent profile", ProfileInput{}, profiles.Profile{}),
	del("/api/v1/agent-profiles/{name}", "Delete an agent profile unless autopilot options use it", nil).S(gone),

	get("/api/v1/questions", "List questions", list[model.Question]("questions")).Q("status"),
	post("/api/v1/questions/{id}/answer", "Answer a question", Answer{}, model.Question{}),
	get("/api/v1/help-requests", "List help requests, blockers and split proposals", list[helpreq.Request]("help_requests")).Q("status brief_id task_id limit:integer"),

	get("/api/v1/threads/{kind}/{id}/messages", "Read a thread", list[model.Message]("messages")).Q("since:integer limit:integer"),
	post("/api/v1/threads/{kind}/{id}/messages", "Post an owner message to a thread", ThreadPost{}, model.Message{}).S(created),

	get("/api/v1/capacity", "Capacity limits and usage", board.CapacityView{}),
	put("/api/v1/capacity", "Replace the capacity configuration", capacity.Config{}, board.CapacityView{}),
	get("/api/v1/spawns", "List spawns with their lifecycle", list[lifecycle.Spawn]("spawns")).Q("brief_id"),
	post("/api/v1/spawns/{id}/stop", "Stop a spawn and revoke its agent", Free{}, lifecycle.Spawn{}),
	get("/api/v1/breakers", "Provider circuit breakers", list[breaker.Status]("breakers")),
	post("/api/v1/breakers/{provider}/reset", "Close the breakers of a provider", Free{}, list[breaker.Status]("breakers")),

	get("/api/v1/audit", "Audit events", list[model.AuditEvent]("events")).Q("agent_id task_id tool since:integer limit:integer"),
	get("/api/v1/audit/verify", "Verify the audit hash chain", model.AuditCheck{}),
	get("/api/v1/audit/export", "Export the audit chain as newline-delimited JSON", "").Q("since:integer").As(mediaNDJSON),

	get("/api/v1/decisions", "Owner decision inbox", list[decisions.Decision]("decisions")).Q("limit:integer"),
	get("/api/v1/decisions/next", "Most urgent decision", obj("decision", &decisions.Decision{})),
	post("/api/v1/decisions/{id}", "Act on a decision", DecisionAction{}, decisions.Outcome{}),
	post("/api/v1/action-links", "Mint a one-time action link for a decision", ActionLinkRequest{}, obj("url", "", "expires_at", time.Time{})).S(created),

	post("/api/v1/messages", "Send an owner message to an agent, brief or leader", chat.Message{}, chat.Conversation{}).S(created),
	get("/api/v1/conversations", "List conversations", list[chat.Conversation]("conversations")),
	get("/api/v1/conversations/{id}", "Get a conversation with its messages", chat.Conversation{}),

	get("/api/v1/prompts", "List prompt templates", list[prompts.Template]("prompts")).Q("tag"),
	get("/api/v1/prompts/{id}", "Get a prompt template", prompts.Template{}),
	get("/api/v1/prompts/{id}/render", "Render a prompt template; template variables are query parameters", obj("id", "", "text", "")),
	get("/api/v1/task-templates", "List task templates", list[prompts.Template]("templates")),

	post("/api/v1/hillclimb/jobs", "Create a hill-climb job", HillclimbCreate{}, hillclimb.Job{}).S(created),
	get("/api/v1/hillclimb/jobs", "List hill-climb jobs", list[hillclimb.Job]("jobs")),
	get("/api/v1/hillclimb/jobs/{id}/series", "Attempts of a hill-climb job", obj("job", hillclimb.Job{}, "points", []hillclimb.Point{})),

	post("/api/v1/artifacts", "Upload an artifact (multipart)", nil, artifacts.Artifact{}).Body(mediaMultipt, ArtifactUpload{}).Q("scope name").S(created),
	get("/api/v1/artifacts", "List artifacts", list[artifacts.Artifact]("artifacts")).Q("scope limit:integer"),
	get("/api/v1/artifacts/{id}", "Download an artifact; supports Range", Binary{}).As(mediaBinary),
	del("/api/v1/artifacts/{id}", "Delete an artifact", nil).S(gone),

	get("/api/v1/bots", "List bots (agents with liveness)", list[Bot]("bots")).Q("role status"),
	post("/api/v1/bots", "Create a bot; the token is shown once", BotInput{}, BotCreated{}).S(created),
	post("/api/v1/bots/bulk", "Create many bots", BotInput{}, list[BotCreated]("bots")).S(created),
	post("/api/v1/bots/spawn", "Spawn bots for a brief", BotSpawn{}, list[model.Spawn]("spawns")).S(created),
	get("/api/v1/bots/{id}", "Get a bot", obj("bot", Bot{})),
	del("/api/v1/bots/{id}", "Revoke a bot", obj("bot", Bot{})),
	post("/api/v1/bots/{id}/revoke", "Revoke a bot", Reason{}, obj("bot", Bot{})),
	post("/api/v1/bots/{id}/rotate-token", "Rotate a bot token; the new token is shown once", Free{}, BotCreated{}),
	post("/api/v1/bots/{id}/pause", "Pause a bot", Reason{}, obj("bot", Bot{})),
	post("/api/v1/bots/{id}/resume", "Resume a bot", Free{}, obj("bot", Bot{})),
	post("/api/v1/bots/{id}/kill", "Pause a bot and stop its process", Reason{}, obj("bot", Bot{}, "stopped", true)),

	get("/api/v1/instructions/effective", "Effective owner instructions for a project", instructions.Effective{}).Q("project"),
	get("/api/v1/instructions/{scope}/{kind}", "Head version of an instruction", instructions.Version{}),
	put("/api/v1/instructions/{scope}/{kind}", "Save a new instruction version", InstructionSave{}, instructions.Version{}).S(created),
	get("/api/v1/instructions/{scope}/{kind}/versions", "Instruction version history", list[instructions.Meta]("versions")),
	get("/api/v1/instructions/{scope}/{kind}/versions/{n}", "A specific instruction version", instructions.Version{}),
	get("/api/v1/instructions/{scope}/{kind}/diff", "Line diff between two instruction versions", obj("from", 0, "to", 0, "lines", []instructions.DiffLine{})).Q("from:integer to:integer"),
	post("/api/v1/instructions/{scope}/{kind}/rollback", "Roll an instruction back to an earlier version", InstructionRollback{}, instructions.Version{}).S(created),

	get("/api/v1/search/", "Full-text search over tasks, messages, agents and briefs", list[search.Result]("results")).Q("q! kind limit:integer"),
	get("/api/v1/timeseries", "Metric time series", timeseries.Series{}).Q("metric! bucket since brief_id"),
	get("/api/v1/notify/status", "Notification sink status", NotifyStatus{}),
	post("/api/v1/notify/test", "Send one test notification through every sink", Free{}, list[notify.SinkResult]("results")),
	get("/api/v1/digest/preview", "Preview the digest of a recent window", digest.Preview{}).Q("window"),

	get("/api/v1/tokens", "List managed API tokens, revoked ones included", list[tokens.Token]("tokens")),
	post("/api/v1/tokens", "Create a managed API token; the secret is shown once", tokens.CreateInput{}, tokens.Created{}).S(created),
	post("/api/v1/tokens/{id}/revoke", "Revoke a managed API token", Free{}, tokens.Token{}),

	get("/api/v1/knowledge", "Search knowledge entries, pinned first", list[knowledge.Entry]("entries")).Q("brief_id kind status q limit:integer"),
	post("/api/v1/knowledge", "Add a knowledge entry", knowledge.Input{}, knowledge.Entry{}).S(created),
	get("/api/v1/knowledge/{id}", "Get a knowledge entry", knowledge.Entry{}),
	patch("/api/v1/knowledge/{id}", "Edit a knowledge entry", knowledge.Patch{}, knowledge.Entry{}),
	del("/api/v1/knowledge/{id}", "Delete a knowledge entry", nil).S(gone),
	post("/api/v1/knowledge/{id}/pin", "Pin or unpin a knowledge entry", KnowledgePin{}, knowledge.Entry{}),
	post("/api/v1/knowledge/{id}/approve", "Approve a pending knowledge entry", Free{}, knowledge.Entry{}),
	post("/api/v1/knowledge/{id}/supersede", "Mark a knowledge entry superseded", KnowledgeSupersede{}, knowledge.Entry{}),

	get("/api/v1/autopilot", "Autopilot status and per-brief flags", AutopilotView{}),
	post("/api/v1/autopilot/briefs/{id}", "Enable or disable autopilot for a brief", AutopilotSet{}, AutopilotBrief{}),
	put("/api/v1/autopilot/briefs/{id}/spawn", "Set the worker kind, model, provider or profile autopilot spawns for a brief", autopilot.SpawnConfig{}, AutopilotBrief{}),
	get("/api/v1/mergequeue/candidates", "Merge queue candidates", list[mergequeue.Candidate]("candidates")).Q("repo"),
	get("/api/v1/mergequeue/candidates/{id}", "Get a merge queue candidate", mergequeue.Candidate{}),
	post("/api/v1/mergequeue/candidates", "Enqueue an approved task's pull request", MergeEnqueue{}, mergequeue.Candidate{}).S(created),
	get("/api/v1/mergequeue/trains", "Merge trains, newest first", list[mergequeue.Train]("trains")).Q("repo limit:integer"),
	get("/api/v1/mergequeue/trains/{id}", "Get a merge train", mergequeue.Train{}),
}
