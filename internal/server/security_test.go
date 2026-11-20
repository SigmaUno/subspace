package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func (e *env) brief(body map[string]any) string {
	e.t.Helper()
	code, br := e.rest("POST", "/api/v1/briefs", body)
	if code != 201 {
		e.t.Fatalf("brief: %d %v", code, br)
	}
	return br["id"].(string)
}

func (e *env) readyTask(lead, bid string) string {
	e.t.Helper()
	p := e.must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{map[string]any{"title": "t", "spec": "s", "repo": "o/r"}}})["plan"].(map[string]any)
	if code, out := e.rest("POST", "/api/v1/plans/"+p["id"].(string)+"/approve", map[string]any{}); code != 200 {
		e.t.Fatalf("approve: %d %v", code, out)
	}
	return p["tasks"].([]any)[0].(map[string]any)["id"].(string)
}

func (e *env) taskStatus(id string) string {
	e.t.Helper()
	_, tk := e.rest("GET", "/api/v1/tasks/"+id, nil)
	return tk["status"].(string)
}

func TestLeaderCannotBypassPlanApprovalViaBlocked(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	e.register("w1", "worker", "claude-code")
	bid := e.brief(map[string]any{"title": "B"})
	p := e.must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{map[string]any{"title": "t", "spec": "s", "repo": "o/r"}}})["plan"].(map[string]any)
	tid := p["tasks"].([]any)[0].(map[string]any)["id"].(string)

	e.mustFail(lead, "update_task", map[string]any{"task_id": tid, "status": "ready"}, "forbidden")
	e.mustFail(lead, "update_task", map[string]any{"task_id": tid, "status": "blocked"}, "conflict")
	e.mustFail(lead, "update_task", map[string]any{"task_id": tid, "status": "ready"}, "forbidden")
	e.mustFail(lead, "assign", map[string]any{"task_id": tid, "agent_id": "w1"}, "conflict")
	if s := e.taskStatus(tid); s != "backlog" {
		t.Fatalf("unapproved task escaped backlog: %s", s)
	}
}

func TestLeaderCannotClearNeedsOwnerViaBlocked(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	wk := e.register("w1", "worker", "claude-code")
	rev := e.register("r1", "reviewer", "codex")
	tid := e.readyTask(lead, e.brief(map[string]any{"title": "B"}))
	e.must(lead, "assign", map[string]any{"task_id": tid, "agent_id": "w1"})
	for i := 0; i < 3; i++ {
		e.must(wk, "claim_task", map[string]any{"task_id": tid})
		e.must(wk, "submit", map[string]any{"task_id": tid, "pr_url": "https://git.example/o/r/pulls/1", "summary": "s"})
		e.must(rev, "review", map[string]any{"task_id": tid, "verdict": "changes_requested", "comments": "no"})
	}
	if s := e.taskStatus(tid); s != "needs_owner" {
		t.Fatalf("want needs_owner, got %s", s)
	}
	e.mustFail(lead, "update_task", map[string]any{"task_id": tid, "status": "blocked"}, "conflict")
	e.mustFail(lead, "update_task", map[string]any{"task_id": tid, "status": "ready"}, "conflict")
	if s := e.taskStatus(tid); s != "needs_owner" {
		t.Fatalf("leader cleared needs_owner: %s", s)
	}
}

func TestSelfReviewRespectsAllowedProviders(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	wk := e.register("w1", "worker", "claude-code")
	code, out := e.rest("POST", "/api/v1/agents", map[string]any{"id": "r1", "role": "reviewer", "kind": "other", "model": "m", "provider": "deepseek"})
	if code != 201 {
		t.Fatalf("register: %d %v", code, out)
	}
	rev := out["connect"].(map[string]any)["token"].(string)
	tid := e.readyTask(lead, e.brief(map[string]any{"title": "B", "allowed_providers": []string{"anthropic", "openai"}}))
	e.must(lead, "assign", map[string]any{"task_id": tid, "agent_id": "w1"})
	e.must(wk, "claim_task", map[string]any{"task_id": tid})
	e.must(wk, "submit", map[string]any{"task_id": tid, "pr_url": "https://git.example/o/r/pulls/1", "summary": "s"})

	if lr := e.must(rev, "list_reviews", nil)["tasks"].([]any); len(lr) != 0 {
		t.Fatalf("disallowed provider sees review queue: %v", lr)
	}
	e.mustFail(rev, "review", map[string]any{"task_id": tid, "verdict": "approve", "comments": "ok"}, "forbidden")
	if s := e.taskStatus(tid); s != "submitted" {
		t.Fatalf("disallowed provider reviewed the task: %s", s)
	}
}

func TestLoopGuardIgnoresUnknownArgs(t *testing.T) {
	e := newEnv(t)
	wk := e.register("w1", "worker", "claude-code")
	for i := 0; i < 5; i++ {
		e.must(wk, "add_note", map[string]any{"body": "again", "nonce": i})
	}
	e.mustFail(wk, "add_note", map[string]any{"Body": "again", "nonce": 99}, "agent_paused")
}

func TestThreadWritesEnforceBodyLimit(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	wk := e.register("w1", "worker", "claude-code")
	tid := e.readyTask(lead, e.brief(map[string]any{"title": "B"}))
	e.must(lead, "assign", map[string]any{"task_id": tid, "agent_id": "w1"})
	e.must(wk, "claim_task", map[string]any{"task_id": tid})

	big := strings.Repeat("a", 20<<10)
	e.mustFail(wk, "update_status", map[string]any{"task_id": tid, "status": "in_progress", "note": big}, "bad_request")
	opts := make([]string, 10)
	for i := range opts {
		opts[i] = strings.Repeat("o", 4<<10)
	}
	e.mustFail(wk, "ask_owner", map[string]any{"task_id": tid, "question": "which?", "options": opts}, "bad_request")
	e.mustFail(wk, "submit", map[string]any{"task_id": tid, "pr_url": "https://git.example/o/r/pulls/1", "summary": big}, "bad_request")
	_, out := e.rest("GET", "/api/v1/threads/task/"+tid+"/messages?limit=1000", nil)
	for _, m := range out["messages"].([]any) {
		if n := len(m.(map[string]any)["body"].(string)); n > 16<<10 {
			t.Fatalf("oversized message stored: %d bytes", n)
		}
	}
}

func TestThreadReadLimitIsCapped(t *testing.T) {
	e := newEnv(t)
	wk := e.register("w1", "worker", "claude-code")
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for i := 0; i < 1100; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer func() { <-sem; wg.Done() }()
			if _, err := e.srv.Board.OwnerReply(context.Background(), "agent", "w1", fmt.Sprint("m", i)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if n := len(e.must(wk, "read_thread", map[string]any{"thread": "agent:me", "limit": 100000})["messages"].([]any)); n != 1000 {
		t.Fatalf("read_thread returned %d messages", n)
	}
	if _, out := e.rest("GET", "/api/v1/threads/agent/w1/messages?limit=100000", nil); len(out["messages"].([]any)) != 1000 {
		t.Fatalf("REST thread returned %d messages", len(out["messages"].([]any)))
	}
}
