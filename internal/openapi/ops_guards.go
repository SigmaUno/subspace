package openapi

import (
	"github.com/SigmaUno/subspace/internal/agentout"
	"github.com/SigmaUno/subspace/internal/announce"
	"github.com/SigmaUno/subspace/internal/prsync"
	"github.com/SigmaUno/subspace/internal/retention"
	"github.com/SigmaUno/subspace/internal/webhookout"
)

func init() {
	Register(
		get("/api/v1/tasks/{id}/scope", "Scope report of the latest pull request snapshot of a task", prsync.ScopeStatus{}),
		get("/api/v1/agents/{id}/output", "Buffered terminal output of an agent; without since the newest lines", agentout.Output{}).Q("since:integer limit:integer task_id"),
		get("/api/v1/agent-output", "Newest output line of every active agent with buffered output", list[agentout.Latest]("agents")),
		post("/api/v1/webhooks-out", "Create an outbound webhook subscription; the secret is shown once", webhookout.CreateInput{}, webhookout.Created{}).S(created),
		get("/api/v1/webhooks-out", "List outbound webhook subscriptions", list[webhookout.Subscription]("webhooks")),
		get("/api/v1/webhooks-out/{id}", "Get an outbound webhook subscription", webhookout.Subscription{}),
		del("/api/v1/webhooks-out/{id}", "Delete an outbound webhook subscription and its deliveries", nil).S(gone),
		get("/api/v1/webhooks-out/{id}/deliveries", "Deliveries of an outbound webhook, newest first", list[webhookout.Delivery]("deliveries")).Q("state limit:integer"),
		post("/api/v1/webhooks-out/{id}/deliveries/{delivery}/redeliver", "Queue a dead delivery again", Free{}, webhookout.Delivery{}),
		post("/api/v1/announcements", "Publish an announcement to every agent or the agents of a brief", announce.Input{}, obj("announcement", announce.Announcement{}, "delivered", 0)).S(created),
		get("/api/v1/announcements", "List announcements, newest first", list[announce.Announcement]("announcements")).Q("status"),
		del("/api/v1/announcements/{id}", "Delete an announcement", nil).S(gone),
		get("/api/v1/retention", "Retention policy, archived segments and the last run", retention.Status{}),
		post("/api/v1/retention/run", "Archive expired rows now", Free{}, retention.Report{}),
		get("/api/v1/retention/verify", "Verify archived segments against their manifest and the audit chain", retention.Verification{}),
		post("/api/v1/retention/segments/{id}/restore", "Restore the rows of an archived segment", Free{}, retention.Segment{}),
	)
}
