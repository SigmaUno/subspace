package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/spawner"
	"github.com/SigmaUno/subspace/internal/store"
)

func newFlowEnv(t *testing.T, mod func(*Config)) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cfg := Config{DBPath: filepath.Join(t.TempDir(), "t.db"), OwnerToken: ownerTok, PublicURL: "http://sub.test",
		FlushEvery: 20 * time.Millisecond, SweepEvery: 20 * time.Millisecond, Lease: time.Minute}
	if mod != nil {
		mod(&cfg)
	}
	s, err := New(ctx, cfg)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	go s.Board.Run(ctx)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); cancel(); _ = s.Close() })
	return &env{t: t, ts: ts, srv: s}
}

func (e *env) restWant(want int, method, path string, body any) map[string]any {
	e.t.Helper()
	code, out := e.rest(method, path, body)
	if code != want {
		e.t.Fatalf("%s %s: %d want %d: %v", method, path, code, want, out)
	}
	return out
}

func (e *env) mcpStatus(tok string) int {
	e.t.Helper()
	req, _ := http.NewRequest("POST", e.ts.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func (e *env) brief(body map[string]any) string {
	e.t.Helper()
	if body["title"] == nil {
		body["title"] = "B"
	}
	return e.restWant(201, "POST", "/api/v1/briefs", body)["id"].(string)
}

func planTask(title string, extra map[string]any) map[string]any {
	t := map[string]any{"title": title, "spec": "s", "repo": "o/r", "acceptance_criteria": []string{"works"}}
	for k, v := range extra {
		t[k] = v
	}
	return t
}

func (e *env) approvedPlan(lead, bid string, tasks ...map[string]any) []string {
	e.t.Helper()
	in := make([]any, len(tasks))
	for i, t := range tasks {
		in[i] = t
	}
	plan := e.must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": in})["plan"].(map[string]any)
	e.restWant(200, "POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{})
	var ids []string
	for _, t := range plan["tasks"].([]any) {
		ids = append(ids, t.(map[string]any)["id"].(string))
	}
	return ids
}

func (e *env) task(id string) map[string]any {
	e.t.Helper()
	return e.restWant(200, "GET", "/api/v1/tasks/"+id, nil)
}

func (e *env) waitStatus(id, want string) map[string]any {
	e.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		tk := e.task(id)
		if tk["status"] == want {
			return tk
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("task %s: status %v, want %s", id, tk["status"], want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *env) submit(wk, id string) {
	e.t.Helper()
	e.must(wk, "submit", map[string]any{"task_id": id, "pr_url": "https://git.example/o/r/pulls/1", "summary": "done"})
}

func (e *env) backdateClaim(id string, by time.Duration) {
	e.t.Helper()
	err := e.srv.Store.Atomic(context.Background(), func(tx store.Tx) error {
		tk, err := tx.GetTask(id)
		if err != nil {
			return err
		}
		c := tk.ClaimedAt.Add(-by)
		tk.ClaimedAt = &c
		return tx.UpdateTask(tk)
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func TestBulkRegistrationTokensAuthenticate(t *testing.T) {
	e := newEnv(t)
	out := e.restWant(201, "POST", "/api/v1/agents/bulk", map[string]any{"prefix": "claude", "count": 300, "role": "worker", "kind": "claude-code", "model": "m"})
	regs := out["agents"].([]any)
	if len(regs) != 300 {
		t.Fatalf("registered %d agents", len(regs))
	}
	seen := map[string]bool{}
	for i, r := range regs {
		reg := r.(map[string]any)
		id := reg["agent"].(map[string]any)["id"].(string)
		tok := reg["connect"].(map[string]any)["token"].(string)
		if want := fmt.Sprintf("claude-%03d", i+1); id != want {
			t.Fatalf("agent %d id %q want %q", i, id, want)
		}
		if seen[tok] {
			t.Fatalf("duplicate token for %s", id)
		}
		seen[tok] = true
		me := e.must(tok, "whoami", nil)["agent"].(map[string]any)
		if me["id"] != id || me["role"] != "worker" || me["provider"] != "anthropic" {
			t.Fatalf("token for %s authenticated as %v", id, me)
		}
	}
	if agents := e.restWant(200, "GET", "/api/v1/agents?limit=1000", nil)["agents"].([]any); len(agents) != 300 {
		t.Fatalf("listed %d agents", len(agents))
	}
	e.restWant(409, "POST", "/api/v1/agents/bulk", map[string]any{"prefix": "claude", "count": 1, "role": "worker", "kind": "claude-code", "model": "m"})
}

func TestTokenRotationKillsOldToken(t *testing.T) {
	e := newEnv(t)
	old := e.register("w1", "worker", "claude-code")
	e.must(old, "whoami", nil)
	if e.mcpStatus(old) != 200 {
		t.Fatal("token should work before rotation")
	}
	out := e.restWant(200, "POST", "/api/v1/agents/w1/rotate-token", map[string]any{})
	fresh := out["connect"].(map[string]any)["token"].(string)
	if fresh == old {
		t.Fatal("rotation returned the same token")
	}
	if got := e.mcpStatus(old); got != 401 {
		t.Fatalf("old token after rotation: %d", got)
	}
	if got := e.mcpStatus(fresh); got != 200 {
		t.Fatalf("new token: %d", got)
	}
	if me := e.must(fresh, "whoami", nil)["agent"].(map[string]any); me["id"] != "w1" {
		t.Fatalf("new token identity: %v", me)
	}
}

func TestPlanRejectionCancelsTasks(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	e.register("w1", "worker", "claude-code")
	bid := e.brief(map[string]any{})
	plan := e.must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{planTask("a", nil), planTask("b", nil)}})["plan"].(map[string]any)
	pid := plan["id"].(string)
	var ids []string
	for _, tk := range plan["tasks"].([]any) {
		ids = append(ids, tk.(map[string]any)["id"].(string))
	}

	e.restWant(200, "POST", "/api/v1/plans/"+pid+"/reject", map[string]any{"reason": "too vague"})

	for _, id := range ids {
		tk := e.task(id)
		if tk["status"] != "cancelled" || tk["blocked_reason"] != "plan rejected" {
			t.Fatalf("task %s after rejection: %v %v", id, tk["status"], tk["blocked_reason"])
		}
		e.mustFail(lead, "assign", map[string]any{"task_id": id, "agent_id": "w1"}, "conflict")
	}
	if p := e.restWant(200, "GET", "/api/v1/plans/"+pid, nil); p["status"] != "rejected" {
		t.Fatalf("plan: %v", p["status"])
	}
	if b := e.restWant(200, "GET", "/api/v1/briefs/"+bid, nil); b["plan_status"] != "rejected" {
		t.Fatalf("brief plan_status: %v", b["plan_status"])
	}
	ms := e.restWant(200, "GET", "/api/v1/threads/brief/"+bid+"/messages", nil)["messages"].([]any)
	if last := ms[len(ms)-1].(map[string]any)["body"].(string); !strings.Contains(last, "too vague") {
		t.Fatalf("reason missing from brief thread: %q", last)
	}
	e.restWant(409, "POST", "/api/v1/plans/"+pid+"/approve", map[string]any{})
}

func TestReviewRoundsEndInNeedsOwnerAndReopenResets(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	wk := e.register("w1", "worker", "claude-code")
	rev := e.register("r1", "reviewer", "codex")
	bid := e.brief(map[string]any{})
	id := e.approvedPlan(lead, bid, planTask("a", nil))[0]
	e.must(lead, "assign", map[string]any{"task_id": id, "agent_id": "w1"})

	for round := 1; round <= 3; round++ {
		e.must(wk, "claim_task", map[string]any{"task_id": id})
		e.submit(wk, id)
		r := e.must(rev, "review", map[string]any{"task_id": id, "verdict": "changes_requested", "comments": fmt.Sprintf("round %d", round)})
		want := "changes_requested"
		if round == 3 {
			want = "needs_owner"
		}
		if status(r) != want {
			t.Fatalf("round %d: status %s want %s", round, status(r), want)
		}
		if tk := e.task(id); tk["review_rounds"] != float64(round) {
			t.Fatalf("round %d: review_rounds %v", round, tk["review_rounds"])
		}
	}
	if tk := e.task(id); tk["blocked_reason"] == nil {
		t.Fatal("needs_owner without a reason")
	}
	e.mustFail(wk, "claim_task", map[string]any{"task_id": id}, "conflict")

	e.restWant(200, "POST", "/api/v1/tasks/"+id+"/reopen", map[string]any{"status": "assigned", "note": "owner takes a look"})
	tk := e.task(id)
	if tk["status"] != "assigned" || tk["review_rounds"] != float64(0) || tk["blocked_reason"] != nil {
		t.Fatalf("after reopen: %v rounds=%v reason=%v", tk["status"], tk["review_rounds"], tk["blocked_reason"])
	}

	e.must(wk, "claim_task", map[string]any{"task_id": id})
	e.submit(wk, id)
	if r := e.must(rev, "review", map[string]any{"task_id": id, "verdict": "changes_requested", "comments": "again"}); status(r) != "changes_requested" {
		t.Fatalf("first round after reset: %s", status(r))
	}
}

func TestTaskBudgetCostBlocksAndOwnerRaiseUnblocks(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	wk := e.register("w1", "worker", "claude-code")
	bid := e.brief(map[string]any{})
	id := e.approvedPlan(lead, bid, planTask("a", map[string]any{"budget": map[string]any{"max_cost_eur": 1.0}}))[0]
	e.must(lead, "assign", map[string]any{"task_id": id, "agent_id": "w1"})
	e.must(wk, "claim_task", map[string]any{"task_id": id})

	e.must(wk, "update_status", map[string]any{"task_id": id, "status": "in_progress", "cost_eur": 0.5})
	e.mustFail(wk, "update_status", map[string]any{"task_id": id, "status": "in_progress", "cost_eur": 1.5}, "budget_exceeded")
	tk := e.task(id)
	if tk["status"] != "blocked" || !strings.HasPrefix(tk["blocked_reason"].(string), "budget_exceeded") || tk["lease_expires_at"] != nil {
		t.Fatalf("after overspend: %v %v lease=%v", tk["status"], tk["blocked_reason"], tk["lease_expires_at"])
	}
	e.mustFail(wk, "claim_task", map[string]any{"task_id": id}, "conflict")

	e.restWant(200, "PATCH", "/api/v1/tasks/"+id, map[string]any{"budget": map[string]any{"max_cost_eur": 1.2}})
	if tk := e.task(id); tk["status"] != "blocked" {
		t.Fatalf("a raise that still falls short must not unblock: %v", tk["status"])
	}
	e.restWant(200, "PATCH", "/api/v1/tasks/"+id, map[string]any{"budget": map[string]any{"max_cost_eur": 5.0}})
	tk = e.task(id)
	if tk["status"] != "assigned" || tk["blocked_reason"] != nil {
		t.Fatalf("after raise: %v %v", tk["status"], tk["blocked_reason"])
	}
	e.must(wk, "claim_task", map[string]any{"task_id": id})
}

func TestTaskBudgetMinutesBlocksAndOwnerRaiseUnblocks(t *testing.T) {
	e := newFlowEnv(t, func(c *Config) { c.Lease = time.Hour })
	lead := e.register("rex", "leader", "other")
	wk := e.register("w1", "worker", "claude-code")
	bid := e.brief(map[string]any{})
	id := e.approvedPlan(lead, bid, planTask("a", map[string]any{"budget": map[string]any{"max_minutes": 5}}))[0]
	e.must(lead, "assign", map[string]any{"task_id": id, "agent_id": "w1"})
	e.must(wk, "claim_task", map[string]any{"task_id": id})
	time.Sleep(100 * time.Millisecond)
	if tk := e.task(id); tk["status"] != "in_progress" {
		t.Fatalf("should still be within budget: %v", tk["status"])
	}

	e.backdateClaim(id, 10*time.Minute)
	tk := e.waitStatus(id, "blocked")
	if !strings.HasPrefix(tk["blocked_reason"].(string), "budget_exceeded") || tk["minutes_used"].(float64) < 10 {
		t.Fatalf("after time overrun: reason=%v minutes=%v", tk["blocked_reason"], tk["minutes_used"])
	}
	e.restWant(200, "PATCH", "/api/v1/tasks/"+id, map[string]any{"budget": map[string]any{"max_minutes": 60}})
	if tk := e.task(id); tk["status"] != "assigned" {
		t.Fatalf("after raise: %v", tk["status"])
	}
	e.must(wk, "claim_task", map[string]any{"task_id": id})
}

func TestBriefMaxParallelAgentsBlocksExtraClaim(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	w1 := e.register("w1", "worker", "claude-code")
	w2 := e.register("w2", "worker", "claude-code")
	bid := e.brief(map[string]any{"max_parallel_agents": 1})
	ids := e.approvedPlan(lead, bid, planTask("a", nil), planTask("b", nil), planTask("c", nil))
	e.must(lead, "assign", map[string]any{"task_id": ids[0], "agent_id": "w1"})
	e.must(lead, "assign", map[string]any{"task_id": ids[1], "agent_id": "w2"})
	e.must(lead, "assign", map[string]any{"task_id": ids[2], "agent_id": "w1"})

	e.must(w1, "claim_task", map[string]any{"task_id": ids[0]})
	e.mustFail(w2, "claim_task", map[string]any{"task_id": ids[1]}, "capacity")
	if tk := e.task(ids[1]); tk["status"] != "assigned" {
		t.Fatalf("blocked claim changed the task: %v", tk["status"])
	}
	e.must(w1, "claim_task", map[string]any{"task_id": ids[2]})

	e.submit(w1, ids[0])
	e.submit(w1, ids[2])
	e.must(w2, "claim_task", map[string]any{"task_id": ids[1]})
}

func TestRequestSpawnLimits(t *testing.T) {
	e := newFlowEnv(t, func(c *Config) { c.Spawner = spawner.Noop{} })
	lead := e.register("rex", "leader", "other")
	bid := e.brief(map[string]any{"allowed_providers": []string{"anthropic"}})
	e.restWant(200, "PUT", "/api/v1/capacity", map[string]any{"global_max_agents": 300,
		"limits": []any{map[string]any{"provider": "anthropic", "model": "*", "max_concurrency": 2, "requests_per_min": 0}}})
	spawn := func(kind string, n int) map[string]any {
		return map[string]any{"brief_id": bid, "worker_kind": kind, "model": "m", "n": n}
	}

	e.mustFail(lead, "request_spawn", spawn("codex", 1), "forbidden")
	e.mustFail(lead, "request_spawn", spawn("claude-code", 3), "capacity")
	if sp := e.restWant(200, "GET", "/api/v1/spawns?brief_id="+bid, nil)["spawns"].([]any); len(sp) != 0 {
		t.Fatalf("denied requests created spawns: %v", sp)
	}

	spawns := e.must(lead, "request_spawn", spawn("claude-code", 2))["spawns"].([]any)
	if len(spawns) != 2 {
		t.Fatalf("spawns: %v", spawns)
	}
	for _, s := range spawns {
		sp := s.(map[string]any)
		if sp["status"] != "spawned" || sp["spawner"] != "noop" || sp["provider"] != "anthropic" {
			t.Fatalf("spawn: %v", sp)
		}
		ag := e.restWant(200, "GET", "/api/v1/agents/"+sp["agent_id"].(string), nil)
		if ag["spawned"] != true || ag["status"] != "active" || ag["role"] != "worker" {
			t.Fatalf("spawned agent: %v", ag)
		}
	}
	if sp := e.restWant(200, "GET", "/api/v1/spawns?brief_id="+bid, nil)["spawns"].([]any); len(sp) != 2 {
		t.Fatalf("listed spawns: %v", sp)
	}
	limits := e.restWant(200, "GET", "/api/v1/capacity", nil)["limits"].([]any)
	if in := limits[0].(map[string]any)["in_use"]; in != float64(2) {
		t.Fatalf("in_use: %v", in)
	}
	e.mustFail(lead, "request_spawn", spawn("claude-code", 1), "capacity")
}

func TestPauseAllReleasesLeasesAndResumeAllowsReclaim(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	w1 := e.register("w1", "worker", "claude-code")
	w2 := e.register("w2", "worker", "codex")
	bid := e.brief(map[string]any{})
	ids := e.approvedPlan(lead, bid, planTask("a", nil), planTask("b", nil))
	e.must(lead, "assign", map[string]any{"task_id": ids[0], "agent_id": "w1"})
	e.must(lead, "assign", map[string]any{"task_id": ids[1], "agent_id": "w2"})
	e.must(w1, "claim_task", map[string]any{"task_id": ids[0]})
	e.must(w2, "claim_task", map[string]any{"task_id": ids[1]})

	e.restWant(200, "POST", "/api/v1/pause-all", map[string]any{"reason": "stop"})
	for _, id := range ids {
		tk := e.task(id)
		if tk["status"] != "assigned" || tk["lease_expires_at"] != nil {
			t.Fatalf("task %s after pause: %v lease=%v", id, tk["status"], tk["lease_expires_at"])
		}
	}
	if hb := e.must(w1, "heartbeat", nil); hb["paused"] != true {
		t.Fatalf("heartbeat while paused: %v", hb)
	}
	e.mustFail(w1, "claim_task", map[string]any{"task_id": ids[0]}, "paused")
	e.mustFail(w2, "claim_task", map[string]any{"task_id": ids[1]}, "paused")
	if st := e.restWant(200, "GET", "/api/v1/state", nil); st["paused"] != true {
		t.Fatalf("state: %v", st)
	}

	e.restWant(200, "POST", "/api/v1/resume-all", map[string]any{})
	for i, wk := range []string{w1, w2} {
		if c := e.must(wk, "claim_task", map[string]any{"task_id": ids[i]}); status(c) != "in_progress" || c["lease_expires_at"] == nil {
			t.Fatalf("re-claim: %v", c)
		}
	}
	if hb := e.must(w1, "heartbeat", nil); hb["paused"] != false {
		t.Fatalf("heartbeat after resume: %v", hb)
	}
}

func TestReviewerWithWorkerProviderIsRefused(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	wk := e.register("w1", "worker", "claude-code")
	same := e.register("r-same", "reviewer", "claude-code")
	other := e.register("r-other", "reviewer", "codex")
	bid := e.brief(map[string]any{})
	id := e.approvedPlan(lead, bid, planTask("a", nil))[0]
	e.must(lead, "assign", map[string]any{"task_id": id, "agent_id": "w1"})
	e.must(wk, "claim_task", map[string]any{"task_id": id})
	e.submit(wk, id)

	e.mustFail(lead, "assign", map[string]any{"task_id": id, "agent_id": "r-same", "as": "reviewer"}, "forbidden")
	e.mustFail(same, "review", map[string]any{"task_id": id, "verdict": "approve", "comments": "lgtm"}, "forbidden")
	if lr := e.must(same, "list_reviews", nil)["tasks"].([]any); len(lr) != 0 {
		t.Fatalf("same-provider reviewer sees %v", lr)
	}
	if tk := e.task(id); tk["status"] != "submitted" || tk["reviewer"] != nil {
		t.Fatalf("refusals changed the task: %v reviewer=%v", tk["status"], tk["reviewer"])
	}

	if lr := e.must(other, "list_reviews", nil)["tasks"].([]any); len(lr) != 1 {
		t.Fatalf("other-provider reviewer sees %v", lr)
	}
	e.must(lead, "assign", map[string]any{"task_id": id, "agent_id": "r-other", "as": "reviewer"})
	if r := e.must(other, "review", map[string]any{"task_id": id, "verdict": "approve", "comments": "lgtm"}); status(r) != "approved" {
		t.Fatalf("review: %v", r)
	}
}

func TestThreadScoping(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	w1 := e.register("w1", "worker", "claude-code")
	w2 := e.register("w2", "worker", "claude-code")
	rev := e.register("r1", "reviewer", "codex")
	bid := e.brief(map[string]any{})
	ids := e.approvedPlan(lead, bid, planTask("a", nil), planTask("b", nil))
	e.must(lead, "assign", map[string]any{"task_id": ids[0], "agent_id": "w1"})
	e.must(lead, "assign", map[string]any{"task_id": ids[1], "agent_id": "w2"})
	e.must(w1, "add_note", map[string]any{"task_id": ids[0], "body": "secret of w1"})
	e.must(w2, "add_note", map[string]any{"body": "private to w2"})
	e.restWant(201, "POST", "/api/v1/threads/agent/w2/messages", map[string]any{"body": "owner to w2"})

	bodies := func(out map[string]any) string {
		var sb strings.Builder
		for _, m := range out["messages"].([]any) {
			sb.WriteString(m.(map[string]any)["body"].(string) + "\n")
		}
		return sb.String()
	}
	for _, thread := range []string{"task:" + ids[1], "agent:w2", "brief:" + bid} {
		e.mustFail(w1, "read_thread", map[string]any{"thread": thread}, "not_found")
		e.mustFail(w1, "post_message", map[string]any{"thread": thread, "body": "intrusion"}, "not_found")
	}
	e.mustFail(w1, "add_note", map[string]any{"task_id": ids[1], "body": "intrusion"}, "not_found")
	e.mustFail(w2, "read_thread", map[string]any{"thread": "task:" + ids[0]}, "not_found")
	e.mustFail(w2, "read_thread", map[string]any{"thread": "agent:w1"}, "not_found")
	e.mustFail(rev, "read_thread", map[string]any{"thread": "task:" + ids[0]}, "not_found")

	own := bodies(e.must(w1, "read_thread", map[string]any{"thread": "task:" + ids[0]}))
	if !strings.Contains(own, "secret of w1") {
		t.Fatalf("w1 cannot read its own task thread: %q", own)
	}
	mine := bodies(e.must(w2, "read_thread", map[string]any{"thread": "agent:me"}))
	if !strings.Contains(mine, "owner to w2") || !strings.Contains(mine, "private to w2") {
		t.Fatalf("w2 agent thread: %q", mine)
	}
	if other := bodies(e.must(w1, "read_thread", map[string]any{"thread": "agent:me"})); strings.Contains(other, "w2") {
		t.Fatalf("w1 agent thread leaks w2: %q", other)
	}
	if all := bodies(e.must(lead, "read_thread", map[string]any{"thread": "task:" + ids[0]})); !strings.Contains(all, "secret of w1") {
		t.Fatalf("leader cannot read task thread: %q", all)
	}
	if all := bodies(e.must(lead, "read_thread", map[string]any{"thread": "agent:w2"})); !strings.Contains(all, "private to w2") {
		t.Fatalf("leader cannot read agent thread: %q", all)
	}
	for _, m := range e.restWant(200, "GET", "/api/v1/threads/task/"+ids[0]+"/messages", nil)["messages"].([]any) {
		if strings.Contains(m.(map[string]any)["body"].(string), "intrusion") {
			t.Fatalf("rejected post landed in thread: %v", m)
		}
	}
}

var transition = regexp.MustCompile(`^(\w+) → (\w+)`)

func TestAuditCoversFullFlow(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	wk := e.register("w1", "worker", "claude-code")
	rev := e.register("r1", "reviewer", "codex")
	bid := e.brief(map[string]any{})
	id := e.approvedPlan(lead, bid, planTask("a", nil))[0]
	e.must(lead, "assign", map[string]any{"task_id": id, "agent_id": "w1"})
	e.must(wk, "claim_task", map[string]any{"task_id": id})
	e.must(wk, "update_status", map[string]any{"task_id": id, "status": "in_progress", "progress_pct": 60, "cost_eur": 0.2})
	e.submit(wk, id)
	e.must(lead, "assign", map[string]any{"task_id": id, "agent_id": "r1", "as": "reviewer"})
	e.must(rev, "review", map[string]any{"task_id": id, "verdict": "approve", "comments": "lgtm"})
	e.restWant(200, "POST", "/api/v1/tasks/"+id+"/done", map[string]any{"note": "merged"})

	events := e.restWant(200, "GET", "/api/v1/audit?limit=1000", nil)["events"].([]any)
	var last float64
	tools := map[string]bool{}
	var audited []string
	for _, ev := range events {
		m := ev.(map[string]any)
		if m["id"].(float64) <= last {
			t.Fatalf("audit ids not increasing: %v after %v", m["id"], last)
		}
		last = m["id"].(float64)
		tools[m["tool"].(string)] = true
		before, _ := m["before"].(map[string]any)
		after, _ := m["after"].(map[string]any)
		if m["task_id"] == id && before["status"] != nil && after["status"] != nil {
			audited = append(audited, fmt.Sprintf("%v → %v", before["status"], after["status"]))
		}
	}
	for _, tool := range []string{"register_agents", "create_brief", "propose_plan", "approve_plan", "assign", "claim_task", "update_status", "submit", "review", "mark_done"} {
		if !tools[tool] {
			t.Errorf("no audit entry for %s (have %v)", tool, tools)
		}
	}

	want := []string{"ready → assigned", "assigned → in_progress", "in_progress → submitted", "submitted → in_review", "in_review → approved", "approved → done"}
	if !slices.Equal(audited, want) {
		t.Fatalf("audited transitions %v want %v", audited, want)
	}
	var threaded []string
	for _, m := range e.restWant(200, "GET", "/api/v1/threads/task/"+id+"/messages", nil)["messages"].([]any) {
		msg := m.(map[string]any)
		if msg["kind"] != "status" {
			continue
		}
		if g := transition.FindStringSubmatch(msg["body"].(string)); g != nil {
			threaded = append(threaded, g[1]+" → "+g[2])
		}
	}
	if !slices.Equal(threaded, audited) {
		t.Fatalf("thread transitions %v differ from audit %v", threaded, audited)
	}

	mid := int(events[len(events)/2].(map[string]any)["id"].(float64))
	tail := e.restWant(200, "GET", fmt.Sprintf("/api/v1/audit?since=%d&limit=1000", mid), nil)["events"].([]any)
	if len(tail) != len(events)-len(events)/2-1 {
		t.Fatalf("since=%d returned %d of %d events", mid, len(tail), len(events))
	}
	if code, _ := e.rest("DELETE", "/api/v1/audit", nil); code == 200 || code == 204 {
		t.Fatalf("audit has a delete path: %d", code)
	}
}
