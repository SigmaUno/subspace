package usage_test

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
	"github.com/SigmaUno/subspace/internal/usage"
)

type env struct {
	t      *testing.T
	b      *board.Board
	ts     *httptest.Server
	now    atomic.Int64
	tokens map[string]string
	leader model.Actor
}

func newEnv(t *testing.T, limits ...capacity.Limit) *env {
	t.Helper()
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, tokens: map[string]string{}, leader: model.Actor{ID: "rex", Role: model.RoleLeader}}
	e.now.Store(time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC).UnixMilli())
	b, err := board.New(context.Background(), st, board.Options{
		Capacity: capacity.Config{GlobalMaxAgents: 10, Limits: limits},
		Now:      func() time.Time { return time.UnixMilli(e.now.Load()).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	e.b = b
	cache := auth.NewCache(b.LoadByTokenHash, time.Second)
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewServer(cache, mcp.NewRegistry(b)))
	mux.Handle("/usage/", usage.Handler(st, b, nil))
	e.ts = httptest.NewServer(mux)
	t.Cleanup(func() { e.ts.Close(); _ = st.Close() })
	e.agent("rex", model.RoleLeader, "claude-code", "claude-opus-5")
	return e
}

func (e *env) advance(d time.Duration) { e.now.Add(d.Milliseconds()) }

func (e *env) agent(id, role, kind, mdl string) {
	e.t.Helper()
	r, err := e.b.RegisterAgent(context.Background(), board.AgentInput{ID: id, Role: role, Kind: kind, Model: mdl})
	if err != nil {
		e.t.Fatal(err)
	}
	e.tokens[id] = r.Connect.Token
}

func (e *env) brief(maxCost float64, tasks ...board.TaskInput) (string, []string) {
	e.t.Helper()
	ctx := context.Background()
	br, err := e.b.CreateBrief(ctx, board.BriefInput{Title: "b", Body: "b", MaxCostEUR: maxCost})
	if err != nil {
		e.t.Fatal(err)
	}
	for i := range tasks {
		tasks[i].Title, tasks[i].Spec, tasks[i].Repo = "t", "s", "o/r"
	}
	p, err := e.b.ProposePlan(ctx, e.leader, board.PlanInput{BriefID: br.ID, Tasks: tasks})
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.b.ApprovePlan(ctx, p.ID, ""); err != nil {
		e.t.Fatal(err)
	}
	ids := make([]string, len(p.Tasks))
	for i, tk := range p.Tasks {
		ids[i] = tk.ID
	}
	return br.ID, ids
}

func (e *env) assignAndClaim(agent, task string) {
	e.t.Helper()
	if _, err := e.b.Assign(context.Background(), e.leader, task, agent, ""); err != nil {
		e.t.Fatal(err)
	}
	if out, isErr := e.call(agent, "claim_task", map[string]any{"task_id": task}); isErr {
		e.t.Fatalf("claim: %v", out)
	}
}

func (e *env) call(agent, tool string, args any) (map[string]any, bool) {
	e.t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args}})
	req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/mcp", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+e.tokens[agent])
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Result struct {
			Structured map[string]any `json:"structuredContent"`
			IsError    bool           `json:"isError"`
		} `json:"result"`
		Error map[string]any `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		e.t.Fatal(err)
	}
	if out.Error != nil {
		e.t.Fatalf("rpc error calling %s: %v", tool, out.Error)
	}
	return out.Result.Structured, out.Result.IsError
}

func (e *env) get(path string, v any) {
	e.t.Helper()
	resp, err := http.Get(e.ts.URL + path)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("GET %s = %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) task(id string) *model.Task {
	e.t.Helper()
	tk, err := e.b.GetTask(context.Background(), model.Owner, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return tk
}

func near(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func errCode(out map[string]any) string {
	m, _ := out["error"].(map[string]any)
	c, _ := m["code"].(string)
	return c
}

func report(task string, in, out int) map[string]any {
	args := map[string]any{"input_tokens": in, "output_tokens": out}
	if task != "" {
		args["task_id"] = task
	}
	return args
}

func TestReportUsageAccruesCostAndBlocksOnTaskBudget(t *testing.T) {
	e := newEnv(t)
	e.agent("w1", model.RoleWorker, "claude-code", "claude-sonnet-5-5")
	_, ids := e.brief(0, board.TaskInput{Budget: model.Budget{MaxCostEUR: 5}})
	e.assignAndClaim("w1", ids[0])

	out, isErr := e.call("w1", "report_usage", report(ids[0], 1_000_000, 100_000))
	if isErr {
		t.Fatalf("report_usage: %v", out)
	}
	near(t, "first usage cost", out["usage"].(map[string]any)["cost_eur"].(float64), 4.14)
	near(t, "task cost_used", e.task(ids[0]).CostUsed, 4.14)

	if out, isErr = e.call("w1", "report_usage", report(ids[0], 100_000, 0)); isErr {
		t.Fatalf("second report: %v", out)
	}
	near(t, "task cost_used", e.task(ids[0]).CostUsed, 4.416)
	if st := e.task(ids[0]).Status; st != model.StatusInProgress {
		t.Fatalf("status = %s, want in_progress", st)
	}

	out, isErr = e.call("w1", "report_usage", report(ids[0], 500_000, 0))
	if !isErr || errCode(out) != "budget_exceeded" {
		t.Fatalf("expected budget_exceeded, got %v (isErr=%v)", out, isErr)
	}
	tk := e.task(ids[0])
	if tk.Status != model.StatusBlocked {
		t.Fatalf("status = %s, want blocked", tk.Status)
	}
	near(t, "blocked task cost_used", tk.CostUsed, 5.796)
	if tk.BlockedReason == nil || *tk.BlockedReason == "" {
		t.Error("blocked task has no reason")
	}

	if out, isErr = e.call("w1", "update_status", map[string]any{"task_id": ids[0], "status": "in_progress"}); !isErr {
		t.Fatalf("blocked task accepted update_status: %v", out)
	}
}

func TestReportUsageBriefBudgetBlocksTask(t *testing.T) {
	e := newEnv(t)
	e.agent("w1", model.RoleWorker, "claude-code", "claude-sonnet-5-5")
	e.agent("w2", model.RoleWorker, "claude-code", "claude-sonnet-5-5")
	_, ids := e.brief(5, board.TaskInput{}, board.TaskInput{})
	e.assignAndClaim("w1", ids[0])
	e.assignAndClaim("w2", ids[1])

	if out, isErr := e.call("w1", "report_usage", report(ids[0], 1_000_000, 100_000)); isErr {
		t.Fatalf("w1 report: %v", out)
	}
	out, isErr := e.call("w2", "report_usage", report(ids[1], 1_000_000, 100_000))
	if !isErr || errCode(out) != "budget_exceeded" {
		t.Fatalf("w2 should exceed the brief budget, got %v", out)
	}
	if st := e.task(ids[1]).Status; st != model.StatusBlocked {
		t.Errorf("w2 task = %s, want blocked", st)
	}
	if st := e.task(ids[0]).Status; st != model.StatusInProgress {
		t.Errorf("w1 task = %s, want in_progress", st)
	}
}

func TestReportUsageRejectsForeignTaskAndBadInput(t *testing.T) {
	e := newEnv(t)
	e.agent("w1", model.RoleWorker, "claude-code", "claude-sonnet-5-5")
	e.agent("w2", model.RoleWorker, "claude-code", "claude-sonnet-5-5")
	_, ids := e.brief(0, board.TaskInput{})
	e.assignAndClaim("w1", ids[0])

	out, isErr := e.call("w2", "report_usage", report(ids[0], 10, 10))
	if !isErr || errCode(out) != "not_assigned" {
		t.Errorf("foreign task: %v", out)
	}
	out, isErr = e.call("w1", "report_usage", report(ids[0], -1, 10))
	if !isErr || errCode(out) != "bad_request" {
		t.Errorf("negative tokens: %v", out)
	}
	near(t, "cost_used", e.task(ids[0]).CostUsed, 0)
}

func TestSummaryAddsUp(t *testing.T) {
	e := newEnv(t)
	e.agent("w1", model.RoleWorker, "claude-code", "claude-sonnet-5-5")
	e.agent("w2", model.RoleWorker, "opencode", "deepseek-chat")
	b1, t1 := e.brief(0, board.TaskInput{})
	b2, t2 := e.brief(0, board.TaskInput{})
	e.assignAndClaim("w1", t1[0])
	e.assignAndClaim("w2", t2[0])

	steps := []struct {
		agent string
		args  map[string]any
	}{
		{"w1", report(t1[0], 200_000, 20_000)},
		{"w2", report(t2[0], 1_000_000, 500_000)},
		{"w1", report("", 50_000, 5_000)},
	}
	for i, s := range steps {
		if out, isErr := e.call(s.agent, "report_usage", s.args); isErr {
			t.Fatalf("step %d: %v", i, out)
		}
		e.advance(24 * time.Hour)
	}
	if out, isErr := e.call("w1", "report_usage", map[string]any{"input_tokens": 1_000_000, "output_tokens": 0, "model": "claude-haiku-5"}); isErr {
		t.Fatalf("model override: %v", out)
	}

	var s usage.Summary
	e.get("/usage/summary", &s)
	wantTotal := usage.Estimate("anthropic", "claude-sonnet-5-5", 200_000, 20_000) +
		usage.Estimate("deepseek", "deepseek-chat", 1_000_000, 500_000) +
		usage.Estimate("anthropic", "claude-sonnet-5-5", 50_000, 5_000) +
		usage.Estimate("anthropic", "claude-haiku-5", 1_000_000, 0)
	near(t, "total cost", s.Totals.CostEUR, wantTotal)
	if s.Totals.Calls != 4 || s.Totals.InputTokens != 2_250_000 || s.Totals.OutputTokens != 525_000 {
		t.Errorf("totals = %+v", s.Totals)
	}
	for name, group := range map[string][]store.UsageTotal{
		"provider": s.ByProvider, "model": s.ByModel, "agent": s.ByAgent, "brief": s.ByBrief, "day": s.ByDay,
	} {
		var calls, in, out int64
		var cost float64
		for _, g := range group {
			calls, in, out, cost = calls+g.Calls, in+g.InputTokens, out+g.OutputTokens, cost+g.CostEUR
		}
		if calls != s.Totals.Calls || in != s.Totals.InputTokens || out != s.Totals.OutputTokens {
			t.Errorf("by_%s tokens/calls %d/%d/%d != totals %+v", name, calls, in, out, s.Totals)
		}
		near(t, "by_"+name+" cost", cost, s.Totals.CostEUR)
	}

	byKey := func(rows []store.UsageTotal, key string) store.UsageTotal {
		for _, r := range rows {
			if r.Key == key {
				return r
			}
		}
		t.Fatalf("no row %q", key)
		return store.UsageTotal{}
	}
	near(t, "deepseek cost", byKey(s.ByProvider, "deepseek").CostEUR, usage.Estimate("deepseek", "deepseek-chat", 1_000_000, 500_000))
	near(t, "brief 1 cost", byKey(s.ByBrief, b1).CostEUR, usage.Estimate("anthropic", "claude-sonnet-5-5", 200_000, 20_000))
	near(t, "brief 2 cost", byKey(s.ByBrief, b2).CostEUR, usage.Estimate("deepseek", "deepseek-chat", 1_000_000, 500_000))
	near(t, "no-task cost", byKey(s.ByBrief, "").CostEUR,
		usage.Estimate("anthropic", "claude-sonnet-5-5", 50_000, 5_000)+usage.Estimate("anthropic", "claude-haiku-5", 1_000_000, 0))
	if len(s.ByDay) != 4 || s.ByDay[0].Key != "2026-11-01" || s.ByDay[3].Key != "2026-11-04" {
		t.Errorf("by_day = %+v", s.ByDay)
	}
	near(t, "task cost_used matches usage", e.task(t1[0]).CostUsed, usage.Estimate("anthropic", "claude-sonnet-5-5", 200_000, 20_000))

	var since usage.Summary
	e.get("/usage/summary?since=2026-11-03T00:00:00Z", &since)
	if since.Totals.Calls != 2 || since.Since == nil {
		t.Errorf("since totals = %+v", since.Totals)
	}
	near(t, "since cost", since.Totals.CostEUR, usage.Estimate("anthropic", "claude-sonnet-5-5", 50_000, 5_000)+usage.Estimate("anthropic", "claude-haiku-5", 1_000_000, 0))

	resp, err := http.Get(e.ts.URL + "/usage/summary?since=yesterday")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad since = %d", resp.StatusCode)
	}
}

func TestProviderAvailability(t *testing.T) {
	e := newEnv(t,
		capacity.Limit{Provider: "anthropic", Model: "*", MaxConcurrency: 3, RequestsPerMin: 60},
		capacity.Limit{Provider: "openai", Model: "gpt-5", MaxConcurrency: 2},
	)
	e.agent("w1", model.RoleWorker, "claude-code", "claude-sonnet-5-5")
	_, ids := e.brief(0, board.TaskInput{})
	e.assignAndClaim("w1", ids[0])

	find := func(v usage.ProviderView, provider, mdl string) *usage.Provider {
		for i := range v.Providers {
			if v.Providers[i].Provider == provider && v.Providers[i].Model == mdl {
				return &v.Providers[i]
			}
		}
		return nil
	}

	if out, isErr := e.call("w1", "report_usage", report(ids[0], 1000, 1000)); isErr {
		t.Fatal(out)
	}
	e.advance(time.Minute)
	for range 2 {
		if out, isErr := e.call("w1", "report_provider_error", map[string]any{"provider": "anthropic", "model": "claude-sonnet-5-5", "code": "overloaded"}); isErr {
			t.Fatal(out)
		}
		e.advance(time.Second)
	}

	var v usage.ProviderView
	e.get("/usage/providers", &v)
	a := find(v, "anthropic", "claude-sonnet-5-5")
	if a == nil {
		t.Fatalf("anthropic missing: %+v", v)
	}
	if a.Healthy || a.Errors != 2 || a.ErrorsSinceSuccess != 2 || a.LastErrorCode != "overloaded" || a.LastSuccessAt == nil || a.Calls != 1 {
		t.Errorf("anthropic = %+v", a)
	}
	if len(a.Limits) != 1 || a.Limits[0].MaxConcurrency != 3 || a.Limits[0].InUse != 1 || a.Limits[0].Available != 2 {
		t.Errorf("anthropic limits = %+v", a.Limits)
	}
	o := find(v, "openai", "gpt-5")
	if o == nil || o.LastSuccessAt != nil || len(o.Limits) != 1 || o.Limits[0].Available != 2 {
		t.Errorf("openai = %+v", o)
	}
	if v.GlobalMaxAgents != 10 || v.AgentsInUse != 1 {
		t.Errorf("view = %+v", v)
	}

	if out, isErr := e.call("w1", "report_usage", report(ids[0], 10, 10)); isErr {
		t.Fatal(out)
	}
	e.get("/usage/providers", &v)
	if a = find(v, "anthropic", "claude-sonnet-5-5"); !a.Healthy || a.Errors != 2 || a.ErrorsSinceSuccess != 0 {
		t.Errorf("anthropic after success = %+v", a)
	}

	if out, isErr := e.call("w1", "report_provider_error", map[string]any{"provider": "anthropic", "model": "", "code": "x"}); !isErr || errCode(out) != "bad_request" {
		t.Errorf("empty model: %v", out)
	}
}
