package mcp

import (
	"context"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/usage"
)

func RegisterUsage(r *Registry, b *board.Board) {
	r.Register(&Tool{
		Name:        "report_usage",
		Description: "Report token usage of your model calls. The cost is computed by Subspace and added to the task's cost_used; exceeding a task or brief budget blocks the task.",
		Roles:       AllRoles,
		Schema: obj(map[string]any{
			"task_id":       str("task the tokens were spent on; omit for work outside a task"),
			"input_tokens":  intg("input tokens used since your last report"),
			"output_tokens": intg("output tokens used since your last report"),
			"model":         str("model used; defaults to your registered model"),
		}, "input_tokens", "output_tokens"),
		Handler: func(ctx context.Context, c *Call) (any, error) {
			var in struct {
				TaskID       string `json:"task_id"`
				InputTokens  int64  `json:"input_tokens"`
				OutputTokens int64  `json:"output_tokens"`
				Model        string `json:"model"`
			}
			if err := decode(c, &in); err != nil {
				return nil, err
			}
			ag, err := b.GetAgent(ctx, c.Actor.ID)
			if err != nil {
				return nil, err
			}
			mdl := in.Model
			if mdl == "" {
				mdl = ag.Model
			}
			res, err := b.ReportUsage(ctx, c.Actor, board.UsageInput{
				TaskID: in.TaskID, Provider: ag.Provider, Model: mdl,
				InputTokens: in.InputTokens, OutputTokens: in.OutputTokens,
				CostEUR: usage.Estimate(ag.Provider, mdl, in.InputTokens, in.OutputTokens),
			})
			if err != nil {
				return nil, err
			}
			return map[string]any{"usage": res.Usage, "task": res.Task}, nil
		},
	})
	r.Register(&Tool{
		Name:        "report_provider_error",
		Description: "Report an error from a model provider (for example rate_limited, overloaded, auth_failed) so the owner can see provider availability.",
		Roles:       AllRoles,
		Schema: obj(map[string]any{
			"provider": str("provider, e.g. anthropic, openai, deepseek"),
			"model":    str("model that failed"),
			"code":     str("short error code, e.g. rate_limited or overloaded"),
		}, "provider", "model", "code"),
		Handler: func(ctx context.Context, c *Call) (any, error) {
			var in board.ProviderErrorInput
			if err := decode(c, &in); err != nil {
				return nil, err
			}
			e, err := b.ReportProviderError(ctx, c.Actor, in)
			if err != nil {
				return nil, err
			}
			return map[string]any{"provider_error": e}, nil
		},
	})
}
