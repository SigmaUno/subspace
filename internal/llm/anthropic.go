package llm

import (
	"context"
	"strings"
)

const anthropicVersion = "2023-06-01"

type anthropic struct {
	t    transport
	base string
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Temperature float64            `json:"temperature"`
}

type anthropicResponse struct {
	ID         string `json:"id"`
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

func (a *anthropic) Name() string  { return "anthropic" }
func (a *anthropic) Model() string { return a.t.cfg.Model }

func (a *anthropic) Complete(ctx context.Context, req Request) (*Response, error) {
	in := anthropicRequest{Model: a.t.cfg.Model, MaxTokens: maxTokens(req), System: req.System, Temperature: req.Temperature}
	for _, m := range req.Messages {
		in.Messages = append(in.Messages, anthropicMessage(m))
	}
	headers := map[string]string{"x-api-key": a.t.cfg.APIKey, "anthropic-version": anthropicVersion}
	var out anthropicResponse
	if err := a.t.post(ctx, a.base+"/v1/messages", headers, in, &out); err != nil {
		return nil, err
	}
	var text strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	return &Response{
		ID: out.ID, Model: out.Model, Text: text.String(), StopReason: out.StopReason,
		Usage: Usage{
			InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens,
			CacheRead: out.Usage.CacheReadInputTokens, CacheWrite: out.Usage.CacheCreationInputTokens,
		},
	}, nil
}

func maxTokens(req Request) int {
	if req.MaxTokens > 0 {
		return req.MaxTokens
	}
	return 4096
}
