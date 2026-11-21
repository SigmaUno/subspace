package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (r *rig) agentUsage() map[string]any {
	r.t.Helper()
	code, out := r.env.REST("GET", "/api/v1/usage/summary?group_by=agent", nil)
	if code != 200 {
		r.t.Fatalf("usage summary: %d %v", code, out)
	}
	for _, g := range out["groups"].([]any) {
		if g := g.(map[string]any); g["key"] == "wally" {
			return g
		}
	}
	r.t.Fatalf("no usage for wally: %v", out)
	return nil
}

func wantArgs(t *testing.T, got call, tool string, want map[string]any) {
	t.Helper()
	if got.tool != tool {
		t.Fatalf("tool = %s, want %s (%v)", got.tool, tool, got.args)
	}
	for k, v := range want {
		if got.args[k] != v {
			t.Fatalf("%s %s = %v, want %v (%v)", tool, k, got.args[k], v, got.args)
		}
	}
}

func TestUsagePipesJSONToReportUsage(t *testing.T) {
	r := newRig(t)

	r.ok(`{"provider":"deepseek","model":"deepseek-chat","input_tokens":1200,"output_tokens":300,"cost_usd":0.25,"request_id":"step-1"}`, "usage", "-task", r.taskID)
	wantArgs(t, r.tap.last(), "report_usage", map[string]any{
		"provider": "deepseek", "model": "deepseek-chat", "tokens_in": 1200.0, "tokens_out": 300.0,
		"cost_usd": 0.25, "request_id": "step-1", "task_id": r.taskID,
	})

	r.ok(`{"provider":"deepseek","model":"deepseek-chat","rate_limited":true}`, "usage")
	wantArgs(t, r.tap.last(), "report_usage", map[string]any{"provider": "deepseek", "rate_limited": true, "task_id": nil})

	r.ok(`{"provider":"deepseek","model":"deepseek-chat","status":503}`, "provider-error")
	wantArgs(t, r.tap.last(), "report_provider_error", map[string]any{"provider": "deepseek", "model": "deepseek-chat", "status": 503.0})

	before := r.tap.count()
	r.ok(`{"input_tokens":5}`, "usage")
	r.ok(`not json`, "usage")
	if r.tap.count() != before {
		t.Fatalf("invalid input reached the server: %v", r.tap.tools()[before:])
	}

	if g := r.agentUsage(); g["tokens_in"] != 1200.0 || g["tokens_out"] != 300.0 || g["cost_usd"] != 0.25 || g["rate_limited"] != 1.0 {
		t.Fatalf("usage for wally: %v", g)
	}
	_, brk := r.env.REST("GET", "/api/v1/breakers", nil)
	b := brk["breakers"].([]any)[0].(map[string]any)
	if b["provider"] != "deepseek" || b["samples"] != 3.0 || b["errors"] != 2.0 {
		t.Fatalf("breaker: %v", b)
	}
}

func TestClaudeStopReportsTranscriptUsageOnce(t *testing.T) {
	r := newRig(t)
	src, err := os.ReadFile("testdata/claude-transcript.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "s-1.jsonl")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	stop := fmt.Sprintf(`{"hook_event_name":"Stop","session_id":"s-1","transcript_path":%q}`, path)

	r.ok(stop, "usage")
	if got := strings.Join(r.tap.tools(), ","); got != "list_my_tasks,report_usage,report_provider_error" {
		t.Fatalf("tools = %s", got)
	}
	wantArgs(t, r.tap.calls[1], "report_usage", map[string]any{
		"provider": "anthropic", "model": "claude-sonnet-5-5", "task_id": r.taskID,
		"tokens_in": 1050.0, "tokens_out": 200.0, "cache_read": 3000.0, "cache_write": 2000.0,
	})
	wantArgs(t, r.tap.last(), "report_provider_error", map[string]any{"provider": "anthropic", "model": "claude-sonnet-5-5", "status": 529.0})

	before := r.tap.count()
	r.ok(stop, "usage")
	if r.tap.count() != before {
		t.Fatalf("second Stop without new turns reported again: %v", r.tap.tools()[before:])
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"type":"assistant","sessionId":"s-1","message":{"id":"msg_3","model":"claude-sonnet-5-5","usage":{"input_tokens":10,"output_tokens":5}}}` + "\n")
	_ = f.Close()
	r.ok(stop, "usage")
	if got := strings.Join(r.tap.tools()[before:], ","); got != "list_my_tasks,report_usage" {
		t.Fatalf("tools after a new turn = %s", got)
	}
	wantArgs(t, r.tap.last(), "report_usage", map[string]any{"tokens_in": 10.0, "tokens_out": 5.0, "cache_read": nil})

	if g := r.agentUsage(); g["tokens_in"] != 1060.0 || g["tokens_out"] != 205.0 || g["requests"] != 2.0 {
		t.Fatalf("usage for wally: %v", g)
	}
}
