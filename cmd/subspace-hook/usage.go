package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	maxTranscriptRead = 32 << 20
	maxProviderErrors = 3
)

type usageInput struct {
	Provider       string   `json:"provider"`
	Model          string   `json:"model"`
	InputTokens    int64    `json:"input_tokens"`
	OutputTokens   int64    `json:"output_tokens"`
	CacheRead      int64    `json:"cache_read_tokens"`
	CacheWrite     int64    `json:"cache_write_tokens"`
	CostUSD        *float64 `json:"cost_usd"`
	RateLimited    bool     `json:"rate_limited"`
	RequestID      string   `json:"request_id"`
	Status         int      `json:"status"`
	TranscriptPath string   `json:"transcript_path"`
	SessionID      string   `json:"session_id"`
}

func readUsageInput(ctx context.Context, r io.Reader, token string) (usageInput, error) {
	var in usageInput
	if err := json.Unmarshal(readInput(ctx, r, token), &in); err != nil {
		return in, errors.New("stdin must be a JSON object")
	}
	return in, nil
}

func (h *hook) usage(ctx context.Context, task string) error {
	in, err := readUsageInput(ctx, h.stdin, h.cfg.token)
	if err != nil {
		return err
	}
	if in.TranscriptPath != "" {
		return h.transcriptUsage(ctx, task, in)
	}
	if in.Model == "" {
		return errors.New("model is required")
	}
	if _, ok := h.lim.reserve("usage"); !ok {
		return nil
	}
	return h.client().Call(ctx, "report_usage", usageArgs(task, in), nil)
}

func (h *hook) providerError(ctx context.Context) error {
	in, err := readUsageInput(ctx, h.stdin, h.cfg.token)
	if err != nil {
		return err
	}
	if _, ok := h.lim.reserve("provider-error"); !ok {
		return nil
	}
	return h.client().Call(ctx, "report_provider_error", providerErrorArgs(in), nil)
}

func usageArgs(task string, in usageInput) map[string]any {
	args := map[string]any{"model": in.Model}
	if in.Provider != "" {
		args["provider"] = in.Provider
	}
	for k, v := range map[string]int64{"tokens_in": in.InputTokens, "tokens_out": in.OutputTokens, "cache_read": in.CacheRead, "cache_write": in.CacheWrite} {
		if v > 0 {
			args[k] = v
		}
	}
	if in.CostUSD != nil && *in.CostUSD >= 0 {
		args["cost_usd"] = *in.CostUSD
	}
	if in.RateLimited {
		args["rate_limited"] = true
	}
	if in.RequestID != "" {
		args["request_id"] = in.RequestID
	}
	if task != "" {
		args["task_id"] = task
	}
	return args
}

func providerErrorArgs(in usageInput) map[string]any {
	args := map[string]any{}
	if in.Provider != "" {
		args["provider"] = in.Provider
	}
	if in.Model != "" {
		args["model"] = in.Model
	}
	if in.Status > 0 {
		args["status"] = in.Status
	}
	return args
}

func (h *hook) transcriptUsage(ctx context.Context, task string, in usageInput) error {
	off, ok := h.lim.offset(in.TranscriptPath)
	if !ok {
		return nil
	}
	t, next, err := readTranscript(in.TranscriptPath, off)
	if err != nil {
		return err
	}
	if next == off {
		return nil
	}
	if len(t.usage) == 0 && len(t.errors) == 0 {
		return h.lim.setOffset(in.TranscriptPath, next)
	}
	if _, ok := h.lim.reserve("usage"); !ok {
		return nil
	}
	c := h.client()
	if task == "" && len(t.usage) > 0 {
		if tasks, err := c.ListMyTasks(ctx, "in_progress"); err == nil && len(tasks) > 0 {
			task = tasks[0].ID
		}
	}
	session := in.SessionID
	if session == "" {
		sum := sha256.Sum256([]byte(in.TranscriptPath))
		session = hex.EncodeToString(sum[:8])
	}
	for _, m := range slices.Sorted(maps.Keys(t.usage)) {
		u := t.usage[m]
		u.RequestID = fmt.Sprintf("claude:%s:%d:%s", session, off, m)
		if err := c.Call(ctx, "report_usage", usageArgs(task, u), nil); err != nil {
			return err
		}
	}
	if err := h.lim.setOffset(in.TranscriptPath, next); err != nil {
		return err
	}
	for _, e := range t.errors[:min(len(t.errors), maxProviderErrors)] {
		if err := c.Call(ctx, "report_provider_error", providerErrorArgs(e), nil); err != nil {
			return err
		}
	}
	return nil
}

type transcript struct {
	usage  map[string]usageInput
	errors []usageInput
}

type transcriptLine struct {
	Type       string `json:"type"`
	IsAPIError bool   `json:"isApiErrorMessage"`
	Message    struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			InputTokens   int64 `json:"input_tokens"`
			OutputTokens  int64 `json:"output_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

var apiErrorStatus = regexp.MustCompile(`API Error: (\d{3})\b`)

func readTranscript(path string, off int64) (transcript, int64, error) {
	t := transcript{usage: map[string]usageInput{}}
	f, err := os.Open(path) //nolint:gosec // path comes from the local agent harness
	if err != nil {
		return t, off, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return t, off, err
	}
	if st.Size() < off {
		off = 0
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return t, off, err
	}
	type msg struct {
		model string
		u     usageInput
	}
	byID := map[string]msg{}
	var order []string
	lastModel := ""
	next := off
	br := bufio.NewReaderSize(io.LimitReader(f, maxTranscriptRead), 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			break
		}
		next += int64(len(line))
		var l transcriptLine
		if json.Unmarshal(line, &l) != nil || l.Type != "assistant" {
			continue
		}
		if l.IsAPIError {
			t.errors = append(t.errors, usageInput{Provider: "anthropic", Model: lastModel, Status: errorStatus(l.Message.Content)})
			continue
		}
		u := l.Message.Usage
		if u == nil || l.Message.ID == "" || l.Message.Model == "" || strings.HasPrefix(l.Message.Model, "<") {
			continue
		}
		lastModel = l.Message.Model
		if _, seen := byID[l.Message.ID]; !seen {
			order = append(order, l.Message.ID)
		}
		byID[l.Message.ID] = msg{l.Message.Model, usageInput{
			InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CacheRead: u.CacheRead, CacheWrite: u.CacheCreation,
		}}
	}
	for _, id := range order {
		m := byID[id]
		s := t.usage[m.model]
		s.Provider, s.Model = "anthropic", m.model
		s.InputTokens += m.u.InputTokens
		s.OutputTokens += m.u.OutputTokens
		s.CacheRead += m.u.CacheRead
		s.CacheWrite += m.u.CacheWrite
		t.usage[m.model] = s
	}
	return t, next, nil
}

func errorStatus(content json.RawMessage) int {
	var text string
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &parts) == nil {
		for _, p := range parts {
			text += p.Text
		}
	} else {
		_ = json.Unmarshal(content, &text)
	}
	if m := apiErrorStatus.FindStringSubmatch(text); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}
