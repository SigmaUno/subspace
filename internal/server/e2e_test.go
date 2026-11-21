package server_test

import (
	"bufio"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

func status(m map[string]any) string { return m["task"].(map[string]any)["status"].(string) }

func TestEndToEnd(t *testing.T) {
	e := servertest.New(t)

	if code, out := e.REST("GET", "/api/v1/state", nil); code != 200 || out["paused"] != false {
		t.Fatalf("state: %d %v", code, out)
	}
	if code, _ := e.REST("GET", "/api/v1/agents", nil); code != 200 {
		t.Fatal("agents")
	}

	lead := e.Register("rex", "leader", "other")
	wk := e.Register("claude-1", "worker", "claude-code")
	rev := e.Register("codex-r", "reviewer", "codex")

	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "Demo", "body": "do it", "repos": []string{"o/r"}, "max_cost_eur": 10, "max_parallel_agents": 5})
	bid := br["id"].(string)

	tools := func(tok string) map[string]bool {
		res := e.RPC(tok, "tools/list", nil)["result"].(map[string]any)["tools"].([]any)
		m := map[string]bool{}
		for _, x := range res {
			m[x.(map[string]any)["name"].(string)] = true
		}
		return m
	}
	if tw := tools(wk); !tw["claim_task"] || tw["assign"] || tw["review"] {
		t.Fatalf("worker tools wrong: %v", tw)
	}
	e.MustFail(wk, "assign", map[string]any{"task_id": "T-1", "agent_id": "x"}, "")

	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{
		map[string]any{"ref": "a", "title": "Add login", "spec": "build it", "repo": "o/r", "acceptance_criteria": []string{"works"}},
		map[string]any{"ref": "b", "title": "Docs", "spec": "docs", "repo": "o/r", "depends_on": []string{"a"}},
	}})["plan"].(map[string]any)
	pid := plan["id"].(string)
	tasks := plan["tasks"].([]any)
	t1 := tasks[0].(map[string]any)["id"].(string)
	t2 := tasks[1].(map[string]any)["id"].(string)
	if !strings.HasPrefix(tasks[0].(map[string]any)["branch"].(string), "sub/t-1-add-login") {
		t.Fatalf("branch: %v", tasks[0])
	}
	e.MustFail(lead, "assign", map[string]any{"task_id": t1, "agent_id": "claude-1"}, "conflict")
	if code, _ := e.REST("POST", "/api/v1/plans/"+pid+"/approve", map[string]any{}); code != 200 {
		t.Fatal("approve")
	}
	e.Must(lead, "assign", map[string]any{"task_id": t1, "agent_id": "claude-1"})
	e.MustFail(wk, "claim_task", map[string]any{"task_id": t2}, "not_assigned")

	claimed := e.Must(wk, "claim_task", map[string]any{"task_id": t1})
	if status(claimed) != "in_progress" || claimed["lease_expires_at"] == nil {
		t.Fatalf("claim: %v", claimed)
	}
	hb := e.Must(wk, "heartbeat", nil)
	if hb["ok"] != true {
		t.Fatal("heartbeat")
	}
	e.Must(wk, "update_status", map[string]any{"task_id": t1, "status": "in_progress", "progress_pct": 50, "note": "half", "cost_eur": 0.5})
	e.MustFail(wk, "submit", map[string]any{"task_id": t1, "summary": "x"}, "bad_request")
	sub := e.Must(wk, "submit", map[string]any{"task_id": t1, "pr_url": "https://git.example/o/r/pulls/1", "summary": "done"})
	if status(sub) != "submitted" {
		t.Fatal(sub)
	}

	if lr := e.Must(rev, "list_reviews", nil)["tasks"].([]any); len(lr) != 1 {
		t.Fatalf("list_reviews: %v", lr)
	}
	e.MustFail(rev, "review", map[string]any{"task_id": t1, "verdict": "changes_requested", "comments": ""}, "bad_request")
	if r := e.Must(rev, "review", map[string]any{"task_id": t1, "verdict": "approve", "comments": "lgtm"}); status(r) != "approved" {
		t.Fatal(r)
	}

	e.Must(lead, "assign", map[string]any{"task_id": t2, "agent_id": "claude-1"})
	e.MustFail(wk, "claim_task", map[string]any{"task_id": t2}, "conflict")
	if code, _ := e.REST("POST", "/api/v1/tasks/"+t1+"/done", map[string]any{"note": "merged"}); code != 200 {
		t.Fatal("done")
	}
	e.Must(wk, "claim_task", map[string]any{"task_id": t2})

	if code, out := e.REST("GET", "/api/v1/threads/task/"+t1+"/messages", nil); code != 200 || len(out["messages"].([]any)) < 5 {
		t.Fatalf("thread: %d %v", code, out)
	}
	if _, out := e.REST("GET", "/api/v1/audit?task_id="+t1, nil); len(out["events"].([]any)) < 4 {
		t.Fatalf("audit: %v", out)
	}

	q := e.Must(wk, "ask_owner", map[string]any{"task_id": t2, "question": "which db?", "options": []string{"a", "b"}})["question"].(map[string]any)
	if code, _ := e.REST("POST", "/api/v1/questions/"+q["id"].(string)+"/answer", map[string]any{"answer": "a"}); code != 200 {
		t.Fatal("answer")
	}
	ms := e.Must(wk, "read_thread", map[string]any{"thread": "task:" + t2})["messages"].([]any)
	if last := ms[len(ms)-1].(map[string]any); last["kind"] != "answer" {
		t.Fatalf("answer not in thread: %v", ms)
	}

	e.MustFail(rev, "read_thread", map[string]any{"thread": "task:" + t2}, "not_found")

	e.REST("POST", "/api/v1/pause-all", map[string]any{"reason": "test"})
	_, tk := e.REST("GET", "/api/v1/tasks/"+t2, nil)
	if tk["status"] != "assigned" {
		t.Fatalf("pause should release lease: %v", tk["status"])
	}
	e.MustFail(wk, "claim_task", map[string]any{"task_id": t2}, "paused")
	e.REST("POST", "/api/v1/resume-all", nil)
	e.Must(wk, "claim_task", map[string]any{"task_id": t2})

	if code, _ := e.REST("GET", "/api/v1/state", nil); code != 200 {
		t.Fatal()
	}
	req, _ := http.NewRequest("GET", e.TS.URL+"/api/v1/state", nil)
	req.Header.Set("Authorization", "Bearer "+wk)
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 403 {
		t.Fatalf("agent on REST: %d", r.StatusCode)
	}
	req, _ = http.NewRequest("POST", e.TS.URL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+servertest.OwnerToken)
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 403 {
		t.Fatalf("owner on /mcp: %d", r.StatusCode)
	}
}

func TestLoopProtectionPausesAgent(t *testing.T) {
	e := servertest.New(t)
	wk := e.Register("w1", "worker", "claude-code")
	for i := 0; i < 5; i++ {
		e.MustFail(wk, "add_note", map[string]any{"task_id": "T-99", "body": "x"}, "not_found")
	}
	e.MustFail(wk, "add_note", map[string]any{"task_id": "T-99", "body": "x"}, "agent_paused")
	_, a := e.REST("GET", "/api/v1/agents/w1", nil)
	if a["status"] != "paused" || a["pause_reason"] != "loop" {
		t.Fatalf("agent not paused for loop: %v", a)
	}
	e.MustFail(wk, "add_note", map[string]any{"body": "other"}, "agent_paused")
	e.REST("POST", "/api/v1/agents/w1/resume", nil)
	e.Must(wk, "add_note", map[string]any{"body": "fine"})
}

func TestLeaseExpiryAndRevoke(t *testing.T) {
	e := servertest.Start(t, server.Config{Lease: 150 * time.Millisecond, FlushEvery: 20 * time.Millisecond, SweepEvery: 20 * time.Millisecond})
	lead := e.Register("rex", "leader", "other")
	wk := e.Register("w1", "worker", "claude-code")
	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B"})
	bid := br["id"].(string)
	e.Must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{map[string]any{"title": "t", "spec": "s", "repo": "o/r"}}})
	e.REST("POST", "/api/v1/plans/P-1/approve", nil)
	e.Must(lead, "assign", map[string]any{"task_id": "T-1", "agent_id": "w1"})
	e.Must(wk, "claim_task", map[string]any{"task_id": "T-1"})
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, tk := e.REST("GET", "/api/v1/tasks/T-1", nil)
		if tk["status"] == "assigned" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lease did not expire: %v", tk["status"])
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.REST("POST", "/api/v1/agents/w1/revoke", map[string]any{})
	req, _ := http.NewRequest("POST", e.TS.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Authorization", "Bearer "+wk)
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 401 {
		t.Fatalf("revoked token should be 401, got %d", r.StatusCode)
	}
}

func TestSSEReceivesTaskEvents(t *testing.T) {
	e := servertest.New(t)
	req, _ := http.NewRequest("GET", e.TS.URL+"/api/v1/events?topics=board", nil)
	req.Header.Set("Authorization", "Bearer "+servertest.OwnerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	lines := make(chan string, 100)
	go func() {
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	wait := func(sub string) {
		t.Helper()
		timeout := time.After(3 * time.Second)
		for {
			select {
			case l := <-lines:
				if strings.Contains(l, sub) {
					return
				}
			case <-timeout:
				t.Fatalf("no %q event", sub)
			}
		}
	}
	wait("event: hello")
	lead := e.Register("rex", "leader", "other")
	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B"})
	e.Must(lead, "propose_plan", map[string]any{"brief_id": br["id"], "tasks": []any{map[string]any{"title": "t", "spec": "s", "repo": "o/r"}}})
	wait("event: task.created")
}
