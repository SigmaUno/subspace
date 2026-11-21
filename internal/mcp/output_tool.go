package mcp

import (
	"context"

	"github.com/SigmaUno/subspace/internal/agentout"
)

func RegisterOutput(r *Registry, s *agentout.Service) {
	r.Register(&Tool{
		Name: "log_output",
		Description: "Stream your terminal output to the owner's live view. Send up to 4 KB of text per call; it is redacted, " +
			"sanitized and kept in a short in-memory tail. It is not stored in the audit log or sent as a notification.",
		Roles:    AllRoles,
		ReadOnly: true,
		Schema: obj(map[string]any{
			"task_id": str("task the output belongs to, if any"),
			"stream":  enum("which stream the text came from", "stdout", "stderr", "note"),
			"text":    str("output text, at most 4096 bytes; longer text is truncated"),
			"seq":     intg("optional increasing chunk number; a chunk whose seq is not above the last accepted one is ignored as a retry"),
		}, "stream", "text"),
		Handler: handle(func(ctx context.Context, c *Call, in agentout.Input) (any, error) {
			return s.Append(ctx, c.Actor, in)
		}),
	})
}
