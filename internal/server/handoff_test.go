package server

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
)

func handoffCtx(t *testing.T, res map[string]any) map[string]any {
	t.Helper()
	c, _ := res["context"].(map[string]any)
	return c
}

func strList(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func ctxSize(t *testing.T, c map[string]any) int {
	t.Helper()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return len(raw)
}

func TestHandoffContextAfterLeaseExpiry(t *testing.T) {
	e := newFlowEnv(t, func(c *Config) { c.Lease = 150 * time.Millisecond })
	lead := e.register("rex", "leader", "other")
	tess := e.register("tess", "tester", "other")
	bid := e.brief(map[string]any{})
	id := e.approvedPlan(lead, bid, planTask("Verify", map[string]any{"role": "tester"}))[0]
	e.must(lead, "assign", map[string]any{"task_id": id, "agent_id": "tess"})

	if first := e.must(tess, "get_task", map[string]any{"task_id": id}); handoffCtx(t, first) != nil {
		t.Fatalf("fresh task must have no context: %v", first)
	}
	if claim := e.must(tess, "claim_task", map[string]any{"task_id": id}); handoffCtx(t, claim) != nil {
		t.Fatalf("first claim must have no context: %v", claim)
	}
	e.must(tess, "update_status", map[string]any{"task_id": id, "status": "in_progress", "cost_eur": 0.25, "note": "halfway"})
	e.must(tess, "report_tests", map[string]any{"task_id": id, "passed": 7, "failed": 2})
	e.waitStatus(id, "assigned")

	got := e.must(tess, "get_task", map[string]any{"task_id": id})
	c := handoffCtx(t, got)
	if c == nil {
		t.Fatalf("expected context after lease expiry: %v", got)
	}
	tests := c["tests"].(map[string]any)
	if tests["passed"] != float64(7) || tests["failed"] != float64(2) {
		t.Fatalf("tests: %v", tests)
	}
	if c["cost_used"] != 0.25 || c["branch"] != got["task"].(map[string]any)["branch"] {
		t.Fatalf("cost/branch: %v", c)
	}
	var texts []string
	for _, m := range c["messages"].([]any) {
		texts = append(texts, m.(map[string]any)["text"].(string))
	}
	all := strings.Join(texts, "\n")
	for _, want := range []string{"lease expired", "Tests: 7 passed, 2 failed", "halfway"} {
		if !strings.Contains(all, want) {
			t.Fatalf("thread digest lacks %q: %v", want, texts)
		}
	}

	again := e.must(tess, "claim_task", map[string]any{"task_id": id})
	if handoffCtx(t, again) == nil {
		t.Fatalf("re-claim after expiry must carry context: %v", again)
	}
}

func TestHandoffContextOnReassign(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	w1 := e.register("claude-1", "worker", "claude-code")
	w2 := e.register("claude-2", "worker", "claude-code")
	bid := e.brief(map[string]any{})
	id := e.approvedPlan(lead, bid, planTask("Build", nil))[0]
	e.must(lead, "assign", map[string]any{"task_id": id, "agent_id": "claude-1"})
	e.must(w1, "claim_task", map[string]any{"task_id": id})
	secret := "ssp_" + strings.Repeat("a1B2c3D4", 6)
	e.must(w1, "add_note", map[string]any{"task_id": id, "body": "tried approach A, token " + secret})
	e.must(w1, "update_status", map[string]any{"task_id": id, "status": "in_progress", "cost_eur": 0.5})
	e.must(lead, "reassign", map[string]any{"task_id": id, "agent_id": "claude-2", "reason": "w1 stuck"})

	e.mustFail(w1, "get_task", map[string]any{"task_id": id}, "not_found")

	got := e.must(w2, "get_task", map[string]any{"task_id": id})
	c := handoffCtx(t, got)
	if c == nil {
		t.Fatalf("new assignee must get context: %v", got)
	}
	if prev := strList(c["previous_assignees"]); !slices.Equal(prev, []string{"claude-1"}) {
		t.Fatalf("previous assignees: %v", prev)
	}
	if c["cost_used"] != 0.5 {
		t.Fatalf("cost: %v", c)
	}
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), secret) || !strings.Contains(string(raw), "tried approach A") {
		t.Fatalf("note must be kept and token redacted: %s", raw)
	}

	claim := e.must(w2, "claim_task", map[string]any{"task_id": id})
	if cc := handoffCtx(t, claim); cc == nil || !slices.Equal(strList(cc["previous_assignees"]), []string{"claude-1"}) {
		t.Fatalf("claim by new assignee: %v", claim)
	}
	if lg := e.must(lead, "get_task", map[string]any{"task_id": id}); handoffCtx(t, lg) != nil {
		t.Fatalf("leader must not get context: %v", lg)
	}
}

func TestHandoffContextAfterChangesRequested(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	wk := e.register("claude-1", "worker", "claude-code")
	rev := e.register("codex-r", "reviewer", "codex")
	bid := e.brief(map[string]any{})
	id := e.approvedPlan(lead, bid, planTask("Build", nil))[0]
	e.must(lead, "assign", map[string]any{"task_id": id, "agent_id": "claude-1"})
	e.must(wk, "claim_task", map[string]any{"task_id": id})
	e.submit(wk, id)
	e.must(rev, "review", map[string]any{"task_id": id, "verdict": "changes_requested", "comments": "first round: add validation"})
	e.must(wk, "claim_task", map[string]any{"task_id": id})
	e.submit(wk, id)
	e.must(rev, "review", map[string]any{"task_id": id, "verdict": "changes_requested", "comments": "second round: handle empty input"})

	got := e.must(wk, "get_task", map[string]any{"task_id": id})
	c := handoffCtx(t, got)
	if c == nil {
		t.Fatalf("worker must get context on changes_requested: %v", got)
	}
	if c["review_comments"] != "second round: handle empty input" || c["review_rounds"] != float64(2) {
		t.Fatalf("latest round only: %v", c)
	}
	if c["pr_url"] != "https://git.example/o/r/pulls/1" {
		t.Fatalf("pr_url: %v", c["pr_url"])
	}
	if len(strList(c["previous_assignees"])) != 0 {
		t.Fatalf("same assignee is not previous: %v", c["previous_assignees"])
	}

	if rg := e.must(rev, "get_task", map[string]any{"task_id": id}); handoffCtx(t, rg) != nil {
		t.Fatalf("reviewer must not get context: %v", rg)
	}
	claim := e.must(wk, "claim_task", map[string]any{"task_id": id})
	if handoffCtx(t, claim) == nil {
		t.Fatalf("claim must carry context: %v", claim)
	}
}

func TestHandoffContextSizeLimits(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	w1 := e.register("claude-1", "worker", "claude-code")
	w2 := e.register("claude-2", "worker", "claude-code")
	rev := e.register("codex-r", "reviewer", "codex")
	bid := e.brief(map[string]any{})
	id := e.approvedPlan(lead, bid, planTask("Build", nil))[0]
	e.must(lead, "assign", map[string]any{"task_id": id, "agent_id": "claude-1"})
	e.must(w1, "claim_task", map[string]any{"task_id": id})
	for i := 0; i < 40; i++ {
		e.must(w1, "add_note", map[string]any{"task_id": id, "body": fmt.Sprintf("%02d ", i) + strings.Repeat("n", 12000)})
	}
	e.submit(w1, id)
	e.must(rev, "review", map[string]any{"task_id": id, "verdict": "changes_requested", "comments": strings.Repeat("r", 15000)})
	e.must(lead, "reassign", map[string]any{"task_id": id, "agent_id": "claude-2"})

	c := handoffCtx(t, e.must(w2, "get_task", map[string]any{"task_id": id}))
	if c == nil {
		t.Fatal("expected context")
	}
	if n := ctxSize(t, c); n > board.MaxContextBytes {
		t.Fatalf("context is %d bytes, limit %d", n, board.MaxContextBytes)
	}
	msgs := c["messages"].([]any)
	if len(msgs) == 0 || len(msgs) > 20 {
		t.Fatalf("messages: %d", len(msgs))
	}
	if c["truncated"] != true {
		t.Fatalf("expected truncated: %v", c["truncated"])
	}
	if rc := c["review_comments"].(string); len(rc) == 0 || len(rc) > 6000 {
		t.Fatalf("review comments length %d", len(rc))
	}
	last := msgs[len(msgs)-1].(map[string]any)
	if n := len([]rune(last["text"].(string))); n > 300 {
		t.Fatalf("message text not condensed: %d", n)
	}
}
