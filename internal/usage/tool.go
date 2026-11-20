package usage

import (
	"context"
	"encoding/json"

	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/model"
)

type reportArgs struct {
	Provider    string  `json:"provider"`
	Model       string  `json:"model"`
	TokensIn    int64   `json:"tokens_in"`
	TokensOut   int64   `json:"tokens_out"`
	CacheRead   int64   `json:"cache_read"`
	CacheWrite  int64   `json:"cache_write"`
	CostUSD     float64 `json:"cost_usd"`
	TaskID      string  `json:"task_id"`
	BriefID     string  `json:"brief_id"`
	RateLimited bool    `json:"rate_limited"`
	RequestID   string  `json:"request_id"`
}

func prop(typ, desc string) map[string]any { return map[string]any{"type": typ, "description": desc} }

func nonNeg(desc string) map[string]any {
	return map[string]any{"type": "integer", "minimum": 0, "description": desc}
}

var ReportUsageSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"provider":     prop("string", "anthropic, openai, deepseek, ...; derived from model when omitted"),
		"model":        prop("string", "model id, e.g. claude-sonnet-5-5"),
		"tokens_in":    nonNeg("uncached input tokens"),
		"tokens_out":   nonNeg("output tokens including reasoning"),
		"cache_read":   nonNeg("cached input tokens read"),
		"cache_write":  nonNeg("input tokens written to cache"),
		"cost_usd":     map[string]any{"type": "number", "minimum": 0, "description": "cost in USD; computed from tokens when omitted"},
		"task_id":      prop("string", "task the usage belongs to"),
		"brief_id":     prop("string", "brief the usage belongs to; derived from task_id when omitted"),
		"rate_limited": prop("boolean", "true to report a provider 429 instead of a completed request"),
		"request_id":   prop("string", "unique id of the request; repeated reports with the same id replace each other"),
	},
	"required":             []string{"model"},
	"additionalProperties": false,
}

func (s *Service) ReportUsage(ctx context.Context, c *mcp.Call) (any, error) {
	var a reportArgs
	if len(c.Args) > 0 {
		if err := json.Unmarshal(c.Args, &a); err != nil {
			return nil, model.BadRequest("invalid arguments: %v", err)
		}
	}
	if a.Model == "" {
		return nil, model.BadRequest("model is required")
	}
	r := Record{
		AgentID: c.Actor.ID, TaskID: a.TaskID, BriefID: a.BriefID, Provider: a.Provider, Model: a.Model,
		TokensIn: a.TokensIn, TokensOut: a.TokensOut, CacheRead: a.CacheRead, CacheWrite: a.CacheWrite,
		CostUSD: a.CostUSD, Source: SourceMCP, RateLimited: a.RateLimited,
	}
	if r.Provider == "" {
		r.Provider = providerFor(r.Model)
	}
	if r.Provider == "" {
		return nil, model.BadRequest("provider is required for model %q", r.Model)
	}
	if a.RequestID != "" {
		r.Key = "mcp:" + c.Actor.ID + ":" + a.RequestID
	}
	recs := []Record{r}
	if err := s.Record(ctx, recs...); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "record": recs[0]}, nil
}

func (s *Service) Tools() []*mcp.Tool {
	return []*mcp.Tool{{
		Name:        "report_usage",
		Description: "Report token usage and cost of a model request, or a provider rate limit (429).",
		Roles:       mcp.AllRoles,
		Schema:      ReportUsageSchema,
		ReadOnly:    true,
		Handler:     s.ReportUsage,
	}}
}
