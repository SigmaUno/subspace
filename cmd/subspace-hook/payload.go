package main

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	maxPayload  = 1 << 20
	maxChunk    = 3500
	maxCommand  = 200
	maxNote     = 500
	stdoutLines = 15
	stderrLines = 5
)

type payload struct {
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	ToolResponse  json.RawMessage `json:"tool_response"`
	Message       string          `json:"message"`
}

func readPayload(ctx context.Context, r io.Reader) payload {
	var p payload
	if r == nil {
		return p
	}
	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(io.LimitReader(r, maxPayload))
		done <- b
	}()
	select {
	case b := <-done:
		_ = json.Unmarshal(b, &p)
	case <-ctx.Done():
	}
	return p
}

func (p payload) logText() string {
	var in struct {
		Command  string `json:"command"`
		FilePath string `json:"file_path"`
	}
	_ = json.Unmarshal(p.ToolInput, &in)
	switch p.ToolName {
	case "Bash":
		head := "$ " + clip(firstLine(in.Command), maxCommand)
		var out struct {
			Stdout string `json:"stdout"`
			Stderr string `json:"stderr"`
		}
		if json.Unmarshal(p.ToolResponse, &out) != nil {
			_ = json.Unmarshal(p.ToolResponse, &out.Stdout)
		}
		var body []string
		body = append(body, lastLines(out.Stdout, stdoutLines, "")...)
		body = append(body, lastLines(out.Stderr, stderrLines, "! ")...)
		return head + "\n" + tail(strings.Join(body, "\n"), maxChunk-len(head)-1)
	case "Edit", "Write", "MultiEdit":
		if in.FilePath == "" {
			return ""
		}
		return strings.ToLower(p.ToolName) + " " + clip(in.FilePath, maxCommand)
	}
	return ""
}

func (p payload) statusNote() string {
	switch p.HookEventName {
	case "Stop":
		return "Claude Code finished responding"
	case "SubagentStop":
		return "Claude Code subagent finished"
	}
	return "Claude Code activity"
}

func (p payload) noteText() string {
	if p.Message == "" {
		return ""
	}
	return "Claude Code notification: " + clip(p.Message, maxNote)
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

func lastLines(s string, n int, prefix string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	if prefix != "" {
		for i, l := range lines {
			lines[i] = prefix + l
		}
	}
	return lines
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func tail(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	cut := len(s) - n
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return s[cut:]
}
