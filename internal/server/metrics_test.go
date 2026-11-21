package server_test

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
	"github.com/SigmaUno/subspace/internal/testdb"
)

func scrapeMetrics(t *testing.T, e *servertest.Env) (*http.Response, map[string]float64) {
	t.Helper()
	req, _ := http.NewRequest("GET", e.TS.URL+"/api/v1/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+servertest.OwnerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	out := map[string]float64{}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.LastIndexByte(line, ' ')
		v, err := strconv.ParseFloat(line[i+1:], 64)
		if err != nil {
			t.Fatalf("bad sample %q", line)
		}
		out[line[:i]] = v
	}
	return resp, out
}

func TestMetricsAfterFlow(t *testing.T) {
	e := servertest.Start(t, server.Config{Lease: 150 * time.Millisecond, FlushEvery: 20 * time.Millisecond, SweepEvery: 20 * time.Millisecond})
	lead := e.Register("rex", "leader", "other")
	wk := e.Register("w1", "worker", "claude-code")
	e.Register("w2", "worker", "codex")
	e.REST("POST", "/api/v1/agents/w2/revoke", map[string]any{})

	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B"})
	e.Must(lead, "propose_plan", map[string]any{"brief_id": br["id"], "tasks": []any{
		map[string]any{"title": "a", "spec": "s", "repo": "o/r"},
		map[string]any{"title": "b", "spec": "s", "repo": "o/r"},
	}})
	e.REST("POST", "/api/v1/plans/P-1/approve", nil)
	e.Must(lead, "assign", map[string]any{"task_id": "T-1", "agent_id": "w1"})
	e.Must(wk, "claim_task", map[string]any{"task_id": "T-1"})
	e.MustFail(wk, "claim_task", map[string]any{"task_id": "T-2"}, "not_assigned")
	e.MustFail(wk, "assign", map[string]any{"task_id": "T-2", "agent_id": "w1"}, "")

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
	if code, _ := e.REST("POST", "/api/v1/backup", map[string]any{}); code != 200 {
		t.Fatalf("backup: %d", code)
	}

	resp, m := scrapeMetrics(t, e)
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Fatalf("content type %q", ct)
	}
	if resp.Header.Get("X-Request-Id") == "" {
		t.Fatal("missing X-Request-Id")
	}

	want := map[string]float64{
		`subspace_mcp_tool_calls_total{tool="claim_task",role="worker",result="ok"}`:           1,
		`subspace_mcp_tool_calls_total{tool="claim_task",role="worker",result="not_assigned"}`: 1,
		`subspace_mcp_tool_calls_total{tool="propose_plan",role="leader",result="ok"}`:         1,
		`subspace_mcp_tool_calls_total{tool="unknown",role="worker",result="rpc_error"}`:       1,
		`subspace_mcp_tool_call_duration_seconds_count{tool="claim_task",role="worker"}`:       2,
		`subspace_http_requests_total{route="POST /api/v1/plans/{id}/approve",code="200"}`:     1,
		`subspace_http_requests_total{route="GET /api/v1/tasks/{id}",code="200"}`:              -1,
		`subspace_http_requests_total{route="/mcp",code="200"}`:                                -1,
		`subspace_tasks{status="ready"}`:                                                       1,
		`subspace_tasks{status="assigned"}`:                                                    1,
		`subspace_agents{status="active"}`:                                                     2,
		`subspace_agents{status="revoked"}`:                                                    1,
		`subspace_leases_expired_total`:                                                        1,
		`subspace_backups`:                                                                     1,
		`subspace_sse_subscribers`:                                                             0,
	}
	if testdb.Postgres() {
		delete(want, `subspace_sse_subscribers`)
	}
	for k, v := range want {
		got, ok := m[k]
		switch {
		case !ok:
			t.Errorf("missing %s", k)
		case v >= 0 && got != v:
			t.Errorf("%s = %v, want %v", k, got, v)
		case v < 0 && got < 1:
			t.Errorf("%s = %v, want >= 1", k, got)
		}
	}
	if m[`subspace_backup_last_bytes`] <= 0 {
		t.Errorf("backup size = %v", m[`subspace_backup_last_bytes`])
	}
}

func TestMetricsEndpointIsOwnerOnly(t *testing.T) {
	e := servertest.New(t)
	wk := e.Register("w1", "worker", "claude-code")
	for tok, want := range map[string]int{"": 401, "nope": 401, wk: 403, servertest.OwnerToken: 200} {
		req, _ := http.NewRequest("GET", e.TS.URL+"/api/v1/metrics", nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("token %q: %d, want %d", tok, resp.StatusCode, want)
		}
	}
}

func TestRequestIDPropagation(t *testing.T) {
	e := servertest.New(t)
	do := func(id string) string {
		req, _ := http.NewRequest("GET", e.TS.URL+"/healthz", nil)
		if id != "" {
			req.Header.Set("X-Request-Id", id)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.Header.Get("X-Request-Id")
	}
	if got := do("trace-123.a_b"); got != "trace-123.a_b" {
		t.Fatalf("client id not echoed: %q", got)
	}
	a, b := do(""), do("")
	if a == "" || a == b {
		t.Fatalf("generated ids must be unique and non-empty: %q %q", a, b)
	}
	if got := do("bad id\twith spaces"); got == "bad id\twith spaces" || got == "" {
		t.Fatalf("invalid client id accepted: %q", got)
	}
}
