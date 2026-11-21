package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
	"github.com/SigmaUno/subspace/internal/testdb"
)

type idemResp struct {
	Replayed bool
	Result   map[string]any
	IsError  bool
}

func idemCall(t *testing.T, e *servertest.Env, tok, tool string, args map[string]any, metaKey, headerKey string) idemResp {
	t.Helper()
	params := map[string]any{"name": tool, "arguments": args}
	if metaKey != "" {
		params["_meta"] = map[string]any{"idempotencyKey": metaKey}
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": "tools/call", "params": params})
	req, _ := http.NewRequest("POST", e.TS.URL+"/mcp", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+tok)
	if headerKey != "" {
		req.Header.Set("Idempotency-Key", headerKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Error(err)
		return idemResp{}
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		ID     json.RawMessage `json:"id"`
		Result struct {
			StructuredContent map[string]any `json:"structuredContent"`
			IsError           bool           `json:"isError"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || string(out.ID) != "7" {
		t.Errorf("%s: bad response %s %v", tool, out.ID, err)
	}
	return idemResp{Replayed: resp.Header.Get("Idempotent-Replayed") == "true", Result: out.Result.StructuredContent, IsError: out.Result.IsError}
}

func idemCode(r idemResp) string {
	e, _ := r.Result["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func messageID(t *testing.T, r idemResp) float64 {
	t.Helper()
	m, ok := r.Result["message"].(map[string]any)
	if !ok {
		t.Fatalf("no message in %v", r.Result)
	}
	return m["id"].(float64)
}

func threadLen(t *testing.T, e *servertest.Env, path string) int {
	t.Helper()
	code, out := e.REST("GET", path, nil)
	if code != 200 {
		t.Fatalf("GET %s: %d %v", path, code, out)
	}
	return len(out["messages"].([]any))
}

func idemTask(t *testing.T, e *servertest.Env) (lead, wk, tid string) {
	t.Helper()
	lead = e.Register("rex", "leader", "other")
	wk = e.Register("w1", "worker", "claude-code")
	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "idem", "body": "b", "repos": []string{"o/r"}, "max_cost_eur": 10, "max_parallel_agents": 5})
	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": br["id"], "tasks": []any{map[string]any{"title": "t", "spec": "s", "repo": "o/r"}}})["plan"].(map[string]any)
	if code, out := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	tid = plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
	e.Must(lead, "assign", map[string]any{"task_id": tid, "agent_id": "w1"})
	return lead, wk, tid
}

func TestIdempotencyReplayConflictAndHeader(t *testing.T) {
	e := servertest.New(t)
	wk := e.Register("w1", "worker", "claude-code")
	thread := "/api/v1/threads/agent/w1/messages"
	args := map[string]any{"body": "hello"}

	first := idemCall(t, e, wk, "add_note", args, "k1", "")
	if first.IsError || first.Replayed {
		t.Fatalf("first call: %+v", first)
	}
	again := idemCall(t, e, wk, "add_note", map[string]any{"body": "hello"}, "k1", "")
	if !again.Replayed || messageID(t, again) != messageID(t, first) {
		t.Fatalf("retry was not replayed: %+v vs %+v", again, first)
	}
	if n := threadLen(t, e, thread); n != 1 {
		t.Fatalf("thread has %d messages after a retried note, want 1", n)
	}

	conflict := idemCall(t, e, wk, "add_note", map[string]any{"body": "other"}, "k1", "")
	if !conflict.IsError || idemCode(conflict) != "idempotency_conflict" {
		t.Fatalf("different args with the same key: %+v", conflict)
	}
	other := idemCall(t, e, wk, "post_message", map[string]any{"thread": "agent:me", "body": "hello"}, "k1", "")
	if idemCode(other) != "idempotency_conflict" {
		t.Fatalf("different tool with the same key: %+v", other)
	}

	h1 := idemCall(t, e, wk, "add_note", args, "", "h1")
	h2 := idemCall(t, e, wk, "add_note", args, "", "h1")
	if !h2.Replayed || messageID(t, h1) != messageID(t, h2) {
		t.Fatalf("header key not honored: %+v %+v", h1, h2)
	}

	idemCall(t, e, wk, "add_note", args, "", "")
	idemCall(t, e, wk, "add_note", args, "", "")
	if n := threadLen(t, e, thread); n != 4 {
		t.Fatalf("thread has %d messages, want 4 (keyless calls are not deduplicated)", n)
	}

	other2 := e.Register("w2", "worker", "claude-code")
	if r := idemCall(t, e, other2, "add_note", args, "k1", ""); r.Replayed || r.IsError {
		t.Fatalf("keys must be scoped per agent: %+v", r)
	}
}

func TestIdempotencyFailedCallIsReplayedNotReapplied(t *testing.T) {
	e := servertest.New(t)
	_, wk, tid := idemTask(t, e)
	first := idemCall(t, e, wk, "submit", map[string]any{"task_id": tid, "pr_url": "https://git.example/pr/1", "summary": "s"}, "s1", "")
	if !first.IsError || idemCode(first) != "conflict" {
		t.Fatalf("submit before claim: %+v", first)
	}
	e.Must(wk, "claim_task", map[string]any{"task_id": tid})
	again := idemCall(t, e, wk, "submit", map[string]any{"task_id": tid, "pr_url": "https://git.example/pr/1", "summary": "s"}, "s1", "")
	if !again.Replayed || idemCode(again) != "conflict" {
		t.Fatalf("retry of a failed call must return the stored failure: %+v", again)
	}
	if _, out := e.REST("GET", "/api/v1/tasks/"+tid, nil); out["status"] != "in_progress" {
		t.Fatalf("task status %v, want in_progress", out["status"])
	}
}

func TestIdempotencyConcurrentDuplicates(t *testing.T) {
	e := servertest.New(t)
	_, wk, tid := idemTask(t, e)
	const n = 50
	run := func(tool string, args map[string]any, key string) []idemResp {
		out := make([]idemResp, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				out[i] = idemCall(t, e, wk, tool, args, key, "")
			}()
		}
		wg.Wait()
		return out
	}

	claims := run("claim_task", map[string]any{"task_id": tid}, "claim-1")
	replayed := 0
	for _, r := range claims {
		if r.IsError {
			t.Fatalf("duplicate claim failed: %v", r.Result)
		}
		if r.Replayed {
			replayed++
		}
	}
	if replayed != n-1 {
		t.Fatalf("%d of %d duplicate claims replayed, want %d", replayed, n, n-1)
	}
	if _, out := e.REST("GET", "/api/v1/audit?tool=claim_task&task_id="+tid, nil); len(out["events"].([]any)) != 1 {
		t.Fatalf("claim applied %d times: %v", len(out["events"].([]any)), out)
	}

	notes := run("add_note", map[string]any{"task_id": tid, "body": "once"}, "note-1")
	for _, r := range notes {
		if messageID(t, r) != messageID(t, notes[0]) {
			t.Fatalf("duplicates returned different messages: %v vs %v", r.Result, notes[0].Result)
		}
	}
	code, out := e.REST("GET", "/api/v1/threads/task/"+tid+"/messages", nil)
	if code != 200 {
		t.Fatal(code)
	}
	count := 0
	for _, m := range out["messages"].([]any) {
		if m.(map[string]any)["body"] == "once" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("note applied %d times, want 1", count)
	}
}

func TestIdempotencyKeyRowCommitsAfterEffect_AtLeastOnceIfCrashInBetween_ReplaysAfterRestart(t *testing.T) {
	db := testdb.Path(t, "idem.db")
	cfg := server.Config{DBPath: db, PublicURL: "http://sub.test", FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute}
	e := servertest.Start(t, cfg)
	wk := e.Register("w1", "worker", "claude-code")
	first := idemCall(t, e, wk, "add_note", map[string]any{"body": "durable"}, "restart-1", "")
	e.TS.Close()
	_ = e.Srv.Close()

	e2 := servertest.Start(t, cfg)
	again := idemCall(t, e2, wk, "add_note", map[string]any{"body": "durable"}, "restart-1", "")
	if !again.Replayed || messageID(t, again) != messageID(t, first) {
		t.Fatalf("key not durable across restart: %+v vs %+v", again, first)
	}
	if n := threadLen(t, e2, "/api/v1/threads/agent/w1/messages"); n != 1 {
		t.Fatalf("thread has %d messages, want 1", n)
	}
}
