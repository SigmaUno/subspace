package llm

import (
	"context"
	"errors"
)

type openAI struct {
	t    transport
	base string
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	MaxTokens      int             `json:"max_tokens"`
	Temperature    float64         `json:"temperature"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type chatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int64 `json:"prompt_tokens"`
		CompletionTokens    int64 `json:"completion_tokens"`
		PromptCacheHit      int64 `json:"prompt_cache_hit_tokens"`
		PromptTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

func (o *openAI) Name() string  { return o.t.cfg.Provider }
func (o *openAI) Model() string { return o.t.cfg.Model }

func (o *openAI) Complete(ctx context.Context, req Request) (*Response, error) {
	in := chatRequest{Model: o.t.cfg.Model, MaxTokens: maxTokens(req), Temperature: req.Temperature}
	if req.System != "" {
		in.Messages = append(in.Messages, chatMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		in.Messages = append(in.Messages, chatMessage(m))
	}
	if req.JSON {
		in.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	var out chatResponse
	if err := o.t.post(ctx, o.base+"/chat/completions", map[string]string{"Authorization": "Bearer " + o.t.cfg.APIKey}, in, &out); err != nil {
		return nil, err
	}
	if len(out.Choices) == 0 {
		return nil, errors.New("llm: response has no choices")
	}
	cached := max(out.Usage.PromptTokensDetails.CachedTokens, out.Usage.PromptCacheHit)
	return &Response{
		ID: out.ID, Model: out.Model, Text: out.Choices[0].Message.Content, StopReason: out.Choices[0].FinishReason,
		Usage: Usage{
			InputTokens: max(out.Usage.PromptTokens-cached, 0), OutputTokens: out.Usage.CompletionTokens, CacheRead: cached,
		},
	}, nil
}
