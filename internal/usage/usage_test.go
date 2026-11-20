package usage_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/usage"
)

var now = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

type briefs []*model.Brief

func (b briefs) ListBriefs(context.Context, string) ([]*model.Brief, error) { return b, nil }

func writeFile(t *testing.T, p, s string, appendTo bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendTo {
		flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(filepath.Clean(p), flag, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func opencodeDB(t *testing.T, p string, stmts ...string) {
	t.Helper()
	if err := os.Chmod(p, 0o600); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+p)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o400); err != nil {
		t.Fatal(err)
	}
}

func claudeLine(id, req, mdl string, ts time.Time, in, out, cr, cw int) string {
	return fmt.Sprintf(`{"type":"assistant","sessionId":"s1","timestamp":%q,"requestId":%q,"message":{"id":%q,"model":%q,"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}}}`,
		ts.Format(time.RFC3339Nano), req, id, mdl, in, out, cr, cw)
}

func get(t *testing.T, srv *httptest.Server, path string, want int, v any) {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != want {
		t.Fatalf("GET %s: status %d, want %d", path, resp.StatusCode, want)
	}
	if v != nil {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}
}

func near(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

type summary struct {
	Totals  usage.Totals  `json:"totals"`
	Groups  []usage.Group `json:"groups"`
	Credits struct {
		TotalUSD     *float64 `json:"total_usd"`
		SpentUSD     float64  `json:"spent_usd"`
		RemainingUSD *float64 `json:"remaining_usd"`
	} `json:"credits"`
	Briefs []usage.BriefBudget `json:"briefs"`
}

func (s summary) group(t *testing.T, key string) usage.Group {
	t.Helper()
	for _, g := range s.Groups {
		if g.Key == key {
			return g
		}
	}
	t.Fatalf("group %q missing in %+v", key, s.Groups)
	return usage.Group{}
}

func TestUsageEndToEnd(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st := usage.NewMemStore()
	svc := usage.New(st, usage.Config{TotalCreditsUSD: 1000, EURToUSD: 1.1})
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	svc.Now = func() time.Time { return time.Unix(0, clock.Load()) }
	svc.Briefs = briefs{{ID: "B-1", Title: "login", Status: "open", Budget: model.BriefBudget{MaxCostEUR: 10}}}
	svc.BriefOf = func(_ context.Context, task string) (string, error) {
		if task == "T-1" || task == "T-2" {
			return "B-1", nil
		}
		return "", model.NotFound("task %s not found", task)
	}
	svc.Capacity = usage.CapacityFunc(func(context.Context) (capacity.Config, capacity.Usage, error) {
		return capacity.Config{
			GlobalMaxAgents: 10,
			Limits: []capacity.Limit{
				{Provider: "anthropic", Model: "*", MaxConcurrency: 4, RequestsPerMin: 3},
				{Provider: "deepseek", Model: "*", MaxConcurrency: 2},
			},
		}, capacity.Usage{
			{"anthropic", "claude-sonnet-5-5"}: 2,
			{"deepseek", "deepseek-v4-pro"}:    2,
		}, nil
	})
	srv := httptest.NewServer(usage.NewHandler(svc))
	defer srv.Close()

	tools := svc.Tools()
	if len(tools) != 1 || tools[0].Name != "report_usage" || tools[0].Schema["type"] != "object" {
		t.Fatalf("tools = %+v", tools)
	}
	report := func(agent, args string) (any, error) {
		return tools[0].Handler(ctx, &mcp.Call{Actor: model.Actor{ID: agent, Role: model.RoleWorker}, Args: json.RawMessage(args)})
	}
	for _, a := range []string{
		`{"model":"claude-sonnet-5-5","tokens_in":1000,"tokens_out":500,"task_id":"T-1","request_id":"r1"}`,
		`{"model":"claude-sonnet-5-5","tokens_in":1000,"tokens_out":500,"task_id":"T-1","request_id":"r1"}`,
		`{"model":"deepseek-v4-pro","cost_usd":2.5,"task_id":"T-2"}`,
		`{"model":"claude-sonnet-5-5","rate_limited":true}`,
	} {
		if _, err := report("w1", a); err != nil {
			t.Fatalf("report %s: %v", a, err)
		}
	}
	for _, a := range []string{`{"model":"claude-sonnet-5-5","tokens_in":-1}`, `{"model":"mystery"}`, `{"tokens_in":1}`} {
		if _, err := report("w1", a); model.AsError(err).Code != "bad_request" {
			t.Fatalf("report %s: err = %v, want bad_request", a, err)
		}
	}

	claudeFile := filepath.Join(dir, "claude", "proj", "s1.jsonl")
	writeFile(t, claudeFile,
		claudeLine("msg_1", "req_1", "claude-opus-4-5", now.Add(-30*time.Second), 100, 20, 1000, 200)+"\n"+
			`{"type":"user","message":{"role":"user","content":"hi"}}`+"\n"+
			claudeLine("msg_1", "req_1", "claude-opus-4-5", now.Add(-30*time.Second), 100, 20, 1000, 200)+"\n"+
			claudeLine("msg_2", "req_2", "claude-opus-4-5", now.Add(-20*time.Second), 10, 10, 0, 0), false)

	codexFile := filepath.Join(dir, "codex", "2026", "11", "20", "rollout-x.jsonl")
	tc := `{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":400,"output_tokens":500,"total_tokens":1500},"last_token_usage":{"input_tokens":1000,"cached_input_tokens":400,"output_tokens":500,"total_tokens":1500}}}}`
	ts := now.Add(-2 * time.Minute).Format(time.RFC3339)
	writeFile(t, codexFile,
		`{"timestamp":"`+ts+`","type":"session_meta","payload":{"id":"cx1"}}`+"\n"+
			`{"timestamp":"`+ts+`","type":"turn_context","payload":{"model":"gpt-5-codex"}}`+"\n"+
			fmt.Sprintf(tc, ts)+"\n"+fmt.Sprintf(tc, ts)+"\n"+
			`{"timestamp":"`+ts+`","type":"event_msg","payload":{"type":"token_count","info":null}}`+"\n", false)

	ocPath := filepath.Join(dir, "opencode.db")
	opencodeDB(t, ocPath,
		`CREATE TABLE session_v2 (id text PRIMARY KEY, model text, cost real DEFAULT 0 NOT NULL,
			tokens_input integer DEFAULT 0 NOT NULL, tokens_output integer DEFAULT 0 NOT NULL,
			tokens_reasoning integer DEFAULT 0 NOT NULL, tokens_cache_read integer DEFAULT 0 NOT NULL,
			tokens_cache_write integer DEFAULT 0 NOT NULL, time_updated integer NOT NULL)`,
		fmt.Sprintf(`INSERT INTO session_v2 VALUES ('ses_a','{"id":"deepseek-v4-pro","providerID":"deepseek"}',1.0,10000,2000,500,0,0,%d)`,
			now.Add(-60*time.Second).UnixMilli()),
		`INSERT INTO session_v2 VALUES ('ses_idle',NULL,0,0,0,0,0,0,1)`)

	collectors := []usage.Collector{
		&usage.ClaudeCode{Root: filepath.Join(dir, "claude"), AgentID: "cc-1"},
		&usage.Codex{Root: filepath.Join(dir, "codex"), AgentID: "cx-1"},
		&usage.OpenCode{Path: ocPath, AgentID: "oc-1"},
		&usage.Codex{Root: filepath.Join(dir, "missing")},
	}
	collect := func() int {
		n := 0
		for _, c := range collectors {
			k, err := svc.Collect(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			n += k
		}
		return n
	}
	if n := collect(); n != 3 {
		t.Fatalf("first collect = %d records, want 3", n)
	}
	if n := collect(); n != 0 {
		t.Fatalf("second collect = %d records, want 0", n)
	}

	writeFile(t, claudeFile, "\n", true)
	opencodeDB(t, ocPath, fmt.Sprintf(`UPDATE session_v2 SET cost=1.5, tokens_input=15000, time_updated=%d WHERE id='ses_a'`,
		now.Add(-10*time.Second).UnixMilli()))
	if n := collect(); n != 2 {
		t.Fatalf("incremental collect = %d records, want 2", n)
	}
	if n := collect(); n != 0 {
		t.Fatalf("repeat collect = %d records, want 0", n)
	}

	var bySource summary
	get(t, srv, "/usage/summary?group_by=source", http.StatusOK, &bySource)
	mcpG := bySource.group(t, "mcp")
	if mcpG.Requests != 2 || mcpG.RateLimited != 1 || mcpG.TokensIn != 1000 {
		t.Fatalf("mcp group = %+v", mcpG)
	}
	near(t, "mcp cost", mcpG.CostUSD, 0.0105+2.5)
	claudeG := bySource.group(t, "claude")
	if claudeG.Requests != 2 || claudeG.TokensIn != 110 || claudeG.CacheRead != 1000 || claudeG.CacheWrite != 200 {
		t.Fatalf("claude group = %+v", claudeG)
	}
	near(t, "claude cost", claudeG.CostUSD, 0.00275+0.0003)
	codexG := bySource.group(t, "codex")
	if codexG.Requests != 1 || codexG.TokensIn != 600 || codexG.CacheRead != 400 || codexG.TokensOut != 500 {
		t.Fatalf("codex group = %+v", codexG)
	}
	near(t, "codex cost", codexG.CostUSD, 0.0058)
	ocG := bySource.group(t, "opencode")
	if ocG.Requests != 2 || ocG.TokensIn != 15000 || ocG.TokensOut != 2500 {
		t.Fatalf("opencode group = %+v", ocG)
	}
	near(t, "opencode cost", ocG.CostUSD, 1.5)

	spent := 0.0105 + 2.5 + 0.00275 + 0.0003 + 0.0058 + 1.5
	near(t, "total cost", bySource.Totals.CostUSD, spent)
	near(t, "credits spent", bySource.Credits.SpentUSD, spent)
	near(t, "credits remaining", *bySource.Credits.RemainingUSD, 1000-spent)
	if len(bySource.Briefs) != 1 {
		t.Fatalf("briefs = %+v", bySource.Briefs)
	}
	b := bySource.Briefs[0]
	near(t, "brief budget", b.BudgetUSD, 11)
	near(t, "brief spent", b.SpentUSD, 2.5105)
	near(t, "brief remaining", *b.RemainingUSD, 11-2.5105)

	var byTask summary
	get(t, srv, "/usage/summary?group_by=task&agent_id=w1", http.StatusOK, &byTask)
	near(t, "T-2 cost", byTask.group(t, "T-2").CostUSD, 2.5)

	var series struct {
		Series []usage.Series `json:"series"`
	}
	get(t, srv, "/usage/series?bucket=minute&group_by=provider&from=2026-11-20T11:55:00Z", http.StatusOK, &series)
	var anth *usage.Series
	for i := range series.Series {
		if series.Series[i].Key == "anthropic" {
			anth = &series.Series[i]
		}
	}
	if anth == nil || len(anth.Points) != 6 {
		t.Fatalf("anthropic series = %+v", series.Series)
	}
	if p := anth.Points[4]; !p.TS.Equal(now.Add(-time.Minute)) || p.Requests != 2 {
		t.Fatalf("11:59 bucket = %+v", p)
	}
	if p := anth.Points[5]; p.Requests != 1 || p.RateLimited != 1 {
		t.Fatalf("12:00 bucket = %+v", p)
	}

	var provs struct {
		Providers []usage.ProviderStatus `json:"providers"`
	}
	get(t, srv, "/usage/providers", http.StatusOK, &provs)
	pm := map[string]usage.ProviderStatus{}
	for _, p := range provs.Providers {
		pm[p.Provider] = p
	}
	if p := pm["anthropic"]; p.Status != "throttled" || p.RateLimited5m != 1 || p.Requests1m != 3 || p.RPMHeadroom != 0 || p.ConcurrencyHeadroom != 2 || p.InUse != 2 {
		t.Fatalf("anthropic = %+v", p)
	}
	if p := pm["deepseek"]; p.Status != "saturated" || p.ConcurrencyHeadroom != 0 || p.Requests1m != 2 {
		t.Fatalf("deepseek = %+v", p)
	}
	if p := pm["openai"]; p.Status != "ok" || p.ConcurrencyHeadroom != -1 || p.RPMHeadroom != -1 {
		t.Fatalf("openai = %+v", p)
	}

	var capRep usage.CapacityReport
	get(t, srv, "/usage/capacity", http.StatusOK, &capRep)
	cm := map[string]usage.ModelCapacity{}
	for _, m := range capRep.Models {
		cm[m.Provider+"/"+m.Model] = m
	}
	want := map[string]struct {
		n   int
		why string
	}{
		"anthropic/claude-sonnet-5-5": {0, "rate_limited"},
		"anthropic/claude-opus-4-5":   {0, "rpm anthropic/*"},
		"deepseek/deepseek-v4-pro":    {0, "concurrency deepseek/*"},
		"openai/gpt-5-codex":          {6, "global"},
	}
	if len(cm) != len(want) {
		t.Fatalf("capacity models = %+v", capRep.Models)
	}
	for k, w := range want {
		if m := cm[k]; m.Spawnable != w.n || m.LimitedBy != w.why {
			t.Fatalf("capacity %s = %+v, want %d by %q", k, m, w.n, w.why)
		}
	}
	if capRep.AgentsInUse != 4 || len(capRep.Limits) != 2 || capRep.CreditsRemainingUSD == nil {
		t.Fatalf("capacity = %+v", capRep)
	}

	clock.Add(int64(10 * time.Minute))
	get(t, srv, "/usage/capacity", http.StatusOK, &capRep)
	for _, m := range capRep.Models {
		if m.Provider == "anthropic" && (m.Spawnable != 2 || m.LimitedBy != "concurrency anthropic/*") {
			t.Fatalf("after throttle window %s = %+v", m.Model, m)
		}
	}

	get(t, srv, "/usage/series?bucket=week", http.StatusBadRequest, nil)
	get(t, srv, "/usage/summary?group_by=color", http.StatusBadRequest, nil)
	get(t, srv, "/usage/summary?from=2026-11-21T00:00:00Z&to=2026-11-20T00:00:00Z", http.StatusBadRequest, nil)
	get(t, srv, "/usage/series?bucket=minute&from=2026-01-01T00:00:00Z", http.StatusBadRequest, nil)
}
