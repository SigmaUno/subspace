package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/servertest"
)

type bulkOutcome struct {
	ID      string
	OK      bool
	Error   struct{ Code, Message string }
	Connect struct{ Token string }
}

type bulkReply struct {
	Action    string
	Results   []bulkOutcome
	Succeeded int
	Failed    int
}

func bulkCall(t *testing.T, e *servertest.Env, path string, body map[string]any) (int, bulkReply) {
	t.Helper()
	code, out := e.REST("POST", path, body)
	raw, _ := json.Marshal(out)
	var r bulkReply
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return code, r
}

func mcpStatus(t *testing.T, e *servertest.Env, tok string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", e.TS.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func bulkTasks(t *testing.T, e *servertest.Env, n int) (lead string, ids []string) {
	t.Helper()
	lead = e.Register("rex", "leader", "other")
	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B", "repos": []string{"o/r"}})
	var specs []any
	for i := range n {
		specs = append(specs, map[string]any{"title": fmt.Sprintf("task %d", i), "spec": "s", "repo": "o/r"})
	}
	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": br["id"], "tasks": specs})["plan"].(map[string]any)
	if code, _ := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	for _, tk := range plan["tasks"].([]any) {
		ids = append(ids, tk.(map[string]any)["id"].(string))
	}
	return lead, ids
}

func taskField(t *testing.T, e *servertest.Env, id, field string) any {
	t.Helper()
	_, out := e.REST("GET", "/api/v1/tasks/"+id, nil)
	return out[field]
}

func wantResults(t *testing.T, r bulkReply, ok map[string]string) {
	t.Helper()
	if len(r.Results) != len(ok) {
		t.Fatalf("results %+v want %d entries", r.Results, len(ok))
	}
	failed := 0
	for _, res := range r.Results {
		code, found := ok[res.ID]
		if !found {
			t.Fatalf("unexpected result for %s", res.ID)
		}
		if code == "" && !res.OK || code != "" && (res.OK || res.Error.Code != code) {
			t.Fatalf("%s: %+v want code %q", res.ID, res, code)
		}
		if code != "" {
			failed++
		}
	}
	if r.Failed != failed || r.Succeeded != len(ok)-failed {
		t.Fatalf("counts %d/%d want %d/%d", r.Succeeded, r.Failed, len(ok)-failed, failed)
	}
}

func TestBulkTasksPartialFailureAndAudit(t *testing.T) {
	e := servertest.New(t)
	lead, ids := bulkTasks(t, e, 4)
	e.Register("w1", "worker", "claude-code")
	wk := e.Register("w2", "worker", "claude-code")
	e.Register("w3", "worker", "claude-code")

	code, r := bulkCall(t, e, "/api/v1/tasks/bulk", map[string]any{"ids": []string{ids[0], ids[1], "T-9999"}, "action": "set_budget",
		"params": map[string]any{"max_cost_eur": 7.5}})
	if code != 200 {
		t.Fatalf("set_budget: %d", code)
	}
	wantResults(t, r, map[string]string{ids[0]: "", ids[1]: "", "T-9999": "not_found"})
	if bud := taskField(t, e, ids[1], "budget").(map[string]any); bud["max_cost_eur"] != 7.5 || bud["max_minutes"] == nil {
		t.Fatalf("budget %v", bud)
	}

	e.Must(lead, "assign", map[string]any{"task_id": ids[3], "agent_id": "w3"})
	_, r = bulkCall(t, e, "/api/v1/tasks/bulk", map[string]any{"ids": []string{ids[0], ids[1], ids[3], ids[0]}, "action": "assign",
		"params": map[string]any{"agent_id": "w1"}})
	wantResults(t, r, map[string]string{ids[0]: "", ids[1]: "", ids[3]: "conflict"})
	if taskField(t, e, ids[0], "assignee") != "w1" || taskField(t, e, ids[3], "assignee") != "w3" {
		t.Fatal("assign applied to the wrong tasks")
	}

	_, r = bulkCall(t, e, "/api/v1/tasks/bulk", map[string]any{"ids": []string{ids[0], ids[3], ids[2]}, "action": "reassign",
		"params": map[string]any{"agent_id": "w2", "reason": "rebalance"}})
	wantResults(t, r, map[string]string{ids[0]: "", ids[3]: "", ids[2]: ""})
	if taskField(t, e, ids[3], "assignee") != "w2" || taskField(t, e, ids[2], "assignee") != "w2" {
		t.Fatal("reassign did not apply")
	}

	_, r = bulkCall(t, e, "/api/v1/tasks/bulk", map[string]any{"ids": []string{ids[0], ids[1]}, "action": "assign",
		"params": map[string]any{"agent_id": "nobody"}})
	wantResults(t, r, map[string]string{ids[0]: "not_found", ids[1]: "not_found"})

	e.Must(wk, "claim_task", map[string]any{"task_id": ids[3]})
	e.Must(wk, "update_status", map[string]any{"task_id": ids[3], "status": "blocked", "note": "stuck"})

	_, r = bulkCall(t, e, "/api/v1/tasks/bulk", map[string]any{"ids": []string{ids[3], ids[1]}, "action": "unblock",
		"params": map[string]any{"status": "ready"}})
	wantResults(t, r, map[string]string{ids[3]: "", ids[1]: "conflict"})
	if taskField(t, e, ids[3], "status") != "ready" {
		t.Fatalf("unblock: %v", taskField(t, e, ids[3], "status"))
	}

	_, r = bulkCall(t, e, "/api/v1/tasks/bulk", map[string]any{"ids": []string{ids[0], ids[1], ids[0]}, "action": "cancel",
		"params": map[string]any{"reason": "obsolete"}})
	wantResults(t, r, map[string]string{ids[0]: "", ids[1]: ""})
	_, r = bulkCall(t, e, "/api/v1/tasks/bulk", map[string]any{"ids": []string{ids[0], ids[1]}, "action": "cancel"})
	wantResults(t, r, map[string]string{ids[0]: "conflict", ids[1]: "conflict"})

	_, r = bulkCall(t, e, "/api/v1/tasks/bulk", map[string]any{"ids": []string{ids[0], ids[2], ids[1]}, "action": "reopen",
		"params": map[string]any{"status": "ready"}})
	wantResults(t, r, map[string]string{ids[0]: "", ids[1]: "", ids[2]: "conflict"})
	if taskField(t, e, ids[0], "status") != "ready" {
		t.Fatalf("reopen: %v", taskField(t, e, ids[0], "status"))
	}

	for _, tool := range []string{"set_budget", "assign", "reassign", "cancel", "reopen"} {
		_, out := e.REST("GET", "/api/v1/audit?tool="+tool+"&task_id="+ids[0], nil)
		if len(out["events"].([]any)) == 0 {
			t.Errorf("no %s audit event for %s", tool, ids[0])
		}
	}
	_, out := e.REST("GET", "/api/v1/audit?tool=cancel", nil)
	if n := len(out["events"].([]any)); n != 2 {
		t.Errorf("cancel audit events %d want 2", n)
	}
}

func TestBulkTasksEmitIndividualEvents(t *testing.T) {
	e := servertest.New(t)
	_, ids := bulkTasks(t, e, 3)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", e.TS.URL+"/api/v1/events?topics=board", nil)
	req.Header.Set("Authorization", "Bearer "+servertest.OwnerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	var mu sync.Mutex
	got := map[string]bool{}
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var typ string
		for sc.Scan() {
			line := sc.Text()
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				typ = v
			}
			if v, ok := strings.CutPrefix(line, "data: "); ok && typ == "task.updated" {
				var ev struct{ Data struct{ ID, Status string } }
				if json.Unmarshal([]byte(v), &ev) == nil && ev.Data.Status == "cancelled" {
					mu.Lock()
					got[ev.Data.ID] = true
					mu.Unlock()
				}
			}
		}
	}()

	time.Sleep(100 * time.Millisecond)
	_, r := bulkCall(t, e, "/api/v1/tasks/bulk", map[string]any{"ids": ids, "action": "cancel"})
	if r.Succeeded != 3 {
		t.Fatalf("cancel: %+v", r)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == len(ids) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task.updated seen for %v, want all of %v", got, ids)
}

func TestBulkTasksRejectsBadRequests(t *testing.T) {
	e := servertest.New(t)
	_, ids := bulkTasks(t, e, 1)
	many := make([]string, 501)
	for i := range many {
		many[i] = fmt.Sprintf("T-%d", i+1)
	}
	cases := map[string]map[string]any{
		"no ids":          {"ids": []string{}, "action": "cancel"},
		"too many ids":    {"ids": many, "action": "cancel"},
		"empty id":        {"ids": []string{ids[0], " "}, "action": "cancel"},
		"unknown action":  {"ids": ids, "action": "explode"},
		"no action":       {"ids": ids},
		"assign no agent": {"ids": ids, "action": "assign", "params": map[string]any{}},
		"budget empty":    {"ids": ids, "action": "set_budget", "params": map[string]any{}},
		"budget negative": {"ids": ids, "action": "set_budget", "params": map[string]any{"max_minutes": -1}},
		"bad params":      {"ids": ids, "action": "cancel", "params": "x"},
	}
	for name, body := range cases {
		if code, out := e.REST("POST", "/api/v1/tasks/bulk", body); code != 400 {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	if taskField(t, e, ids[0], "status") != "ready" {
		t.Fatal("a rejected request changed a task")
	}
	if code, _ := e.REST("GET", "/api/v1/tasks/bulk", nil); code == 200 {
		t.Fatal("GET /tasks/bulk must not succeed")
	}
}

func TestBulkAgentActions(t *testing.T) {
	e := servertest.New(t)
	tokens := map[string]string{}
	for _, id := range []string{"a1", "a2", "a3"} {
		tokens[id] = e.Register(id, "worker", "claude-code")
	}
	agentStatus := func(id string) any {
		_, out := e.REST("GET", "/api/v1/agents/"+id, nil)
		return out["status"]
	}

	_, r := bulkCall(t, e, "/api/v1/agents/bulk-action", map[string]any{"ids": []string{"a1", "a2", "ghost"}, "action": "pause",
		"params": map[string]any{"reason": "maintenance"}})
	wantResults(t, r, map[string]string{"a1": "", "a2": "", "ghost": "not_found"})
	if agentStatus("a1") != "paused" || agentStatus("a3") != "active" {
		t.Fatal("pause hit the wrong agents")
	}

	_, r = bulkCall(t, e, "/api/v1/agents/bulk-action", map[string]any{"ids": []string{"a1", "a2"}, "action": "resume"})
	wantResults(t, r, map[string]string{"a1": "", "a2": ""})
	if agentStatus("a2") != "active" {
		t.Fatal("resume failed")
	}

	_, r = bulkCall(t, e, "/api/v1/agents/bulk-action", map[string]any{"ids": []string{"a1", "a3", "ghost"}, "action": "rotate-token"})
	wantResults(t, r, map[string]string{"a1": "", "a3": "", "ghost": "not_found"})
	fresh := map[string]string{}
	for _, res := range r.Results {
		if res.OK {
			if !strings.HasPrefix(res.Connect.Token, "ssp_") || res.Connect.Token == tokens[res.ID] {
				t.Fatalf("%s: rotated token %q", res.ID, res.Connect.Token)
			}
			fresh[res.ID] = res.Connect.Token
		}
	}
	if code := mcpStatus(t, e, tokens["a1"]); code != http.StatusUnauthorized {
		t.Fatalf("old token: %d", code)
	}
	e.Must(fresh["a1"], "whoami", map[string]any{})

	_, r = bulkCall(t, e, "/api/v1/agents/bulk-action", map[string]any{"ids": []string{"a1", "a2"}, "action": "revoke",
		"params": map[string]any{"reason": "retired"}})
	wantResults(t, r, map[string]string{"a1": "", "a2": ""})
	for _, action := range []string{"pause", "resume", "rotate-token", "revoke"} {
		_, r = bulkCall(t, e, "/api/v1/agents/bulk-action", map[string]any{"ids": []string{"a1", "a3"}, "action": action})
		if r.Failed != 1 || r.Results[0].Error.Code != "conflict" || !r.Results[1].OK {
			t.Errorf("%s on revoked+active: %+v", action, r)
		}
	}

	if code, _ := e.REST("POST", "/api/v1/agents/bulk-action", map[string]any{"ids": []string{"a3"}, "action": "nuke"}); code != 400 {
		t.Fatalf("unknown action: %d", code)
	}
	if code, _ := e.REST("POST", "/api/v1/agents/bulk-action", map[string]any{"ids": []string{}, "action": "pause"}); code != 400 {
		t.Fatalf("empty ids: %d", code)
	}
	_, out := e.REST("GET", "/api/v1/audit?tool=revoke_agent", nil)
	if n := len(out["events"].([]any)); n != 3 {
		t.Errorf("revoke audit events %d want 3", n)
	}
}
