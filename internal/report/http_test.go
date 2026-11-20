package report_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/report"
	"github.com/SigmaUno/subspace/internal/server"
)

const ownerTok = "owner-token-for-tests-0123456789"

type env struct {
	t  *testing.T
	ts *httptest.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s, err := server.New(ctx, server.Config{
		DBPath: filepath.Join(t.TempDir(), "t.db"), OwnerToken: ownerTok, PublicURL: "http://sub.test",
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	go s.Board.Run(ctx)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); cancel(); _ = s.Close() })
	return &env{t: t, ts: ts}
}

func (e *env) do(method, path, token string, body any) (int, http.Header, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, out
}

func (e *env) rest(method, path string, body any) (int, map[string]any) {
	e.t.Helper()
	code, _, out := e.do(method, path, ownerTok, body)
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	return code, m
}

func (e *env) register(id, role, kind string) string {
	e.t.Helper()
	code, out := e.rest("POST", "/api/v1/agents", map[string]any{"id": id, "role": role, "kind": kind, "model": "m"})
	if code != 201 {
		e.t.Fatalf("register %s: %d %v", id, code, out)
	}
	return out["connect"].(map[string]any)["token"].(string)
}

func (e *env) must(tok, tool string, args any) map[string]any {
	e.t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
	req, _ := http.NewRequest("POST", e.ts.URL+"/mcp", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	res, ok := out["result"].(map[string]any)
	if !ok || res["isError"] == true {
		e.t.Fatalf("%s failed: %v", tool, out)
	}
	sc, _ := res["structuredContent"].(map[string]any)
	return sc
}

func TestReportOverHTTP(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	wk := e.register("claude-1", "worker", "claude-code")
	rev := e.register("codex-r", "reviewer", "codex")

	_, br := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "Demo", "body": "do it", "repos": []string{"o/r"}, "max_cost_eur": 10, "max_parallel_agents": 5})
	bid := br["id"].(string)

	plan := e.must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{
		map[string]any{"ref": "a", "title": "Add login", "spec": "build it", "repo": "o/r", "acceptance_criteria": []string{"works"}},
		map[string]any{"ref": "b", "title": "Docs", "spec": "docs", "repo": "o/r", "depends_on": []string{"a"}},
	}})["plan"].(map[string]any)
	tasks := plan["tasks"].([]any)
	t1 := tasks[0].(map[string]any)["id"].(string)
	t2 := tasks[1].(map[string]any)["id"].(string)
	if code, _ := e.rest("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatal("approve")
	}

	e.must(lead, "assign", map[string]any{"task_id": t1, "agent_id": "claude-1"})
	e.must(wk, "claim_task", map[string]any{"task_id": t1})
	e.must(wk, "update_status", map[string]any{"task_id": t1, "status": "in_progress", "cost_eur": 0.5})
	pr := map[string]any{"task_id": t1, "pr_url": "https://git.example/o/r/pulls/1", "summary": "done"}
	e.must(wk, "submit", pr)
	e.must(rev, "review", map[string]any{"task_id": t1, "verdict": "changes_requested", "comments": "fix it"})
	e.must(wk, "claim_task", map[string]any{"task_id": t1})
	e.must(wk, "submit", pr)
	e.must(rev, "review", map[string]any{"task_id": t1, "verdict": "approve", "comments": "lgtm"})
	if code, _ := e.rest("POST", "/api/v1/tasks/"+t1+"/done", map[string]any{"note": "merged"}); code != 200 {
		t.Fatal("done")
	}
	e.must(lead, "assign", map[string]any{"task_id": t2, "agent_id": "claude-1"})
	e.must(wk, "claim_task", map[string]any{"task_id": t2})

	code, hdr, body := e.do("GET", "/api/v1/briefs/"+bid+"/report", ownerTok, nil)
	if code != 200 || !strings.HasPrefix(hdr.Get("Content-Type"), "application/json") {
		t.Fatalf("report: %d %s %s", code, hdr.Get("Content-Type"), body)
	}
	var r report.Report
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	if r.BriefID != bid || r.Total != 2 || !reflect.DeepEqual(r.Counts, map[string]int{"done": 1, "in_progress": 1}) {
		t.Fatalf("summary: %+v", r)
	}
	var tools []string
	for _, s := range r.Tasks[0].Timeline {
		tools = append(tools, s.Tool)
	}
	wantTools := []string{"update_task", "claim_task", "submit", "review", "claim_task", "submit", "review", "mark_done"}
	if r.Tasks[0].ID != t1 || !reflect.DeepEqual(tools, wantTools) {
		t.Fatalf("timeline tools: %v", tools)
	}
	if r.Tasks[0].ReviewRounds != 1 || r.Tasks[0].Claims != 2 || r.Tasks[0].DoneAt == nil || r.Tasks[1].ClaimedAt == nil || r.Tasks[1].DoneAt != nil {
		t.Fatalf("tasks: %+v", r.Tasks)
	}
	if r.Cost.TotalEUR != 0.5 || r.Cost.ByAgent["claude-1"] != 0.5 || r.Cost.ByProvider["anthropic"] != 0.5 || r.Cost.ByTask[t1] != 0.5 {
		t.Fatalf("cost: %+v", r.Cost)
	}
	if !reflect.DeepEqual(r.CriticalPath.TaskIDs, []string{t1, t2}) || r.Throughput.Done != 1 {
		t.Fatalf("flow: %+v %+v", r.CriticalPath, r.Throughput)
	}

	code, hdr, body = e.do("GET", "/api/v1/briefs/"+bid+"/report.md", ownerTok, nil)
	if code != 200 || !strings.HasPrefix(hdr.Get("Content-Type"), "text/markdown") {
		t.Fatalf("markdown: %d %s", code, hdr.Get("Content-Type"))
	}
	for _, want := range []string{"# " + bid + " — Demo", "### " + t1 + " Add login", "`in_progress → submitted`", "Critical path: " + t1 + " → " + t2} {
		if !strings.Contains(string(body), want) {
			t.Errorf("markdown missing %q:\n%s", want, body)
		}
	}

	if code, _, _ := e.do("GET", "/api/v1/briefs/B-99/report", ownerTok, nil); code != 404 {
		t.Fatalf("unknown brief: %d", code)
	}
	if code, _, _ := e.do("GET", "/api/v1/briefs/"+bid+"/report", "", nil); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code, _, _ := e.do("GET", "/api/v1/briefs/"+bid+"/report", wk, nil); code != 403 {
		t.Fatalf("agent token: %d", code)
	}
}
