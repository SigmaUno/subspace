package server_test

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

func claudeLine(t *testing.T, path, id string, in, out int) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	_, err = fmt.Fprintf(f, `{"type":"assistant","sessionId":"s1","timestamp":%q,"requestId":"req-%s","message":{"id":"msg-%s","model":"claude-sonnet-5-5","usage":{"input_tokens":%d,"output_tokens":%d}}}`+"\n",
		time.Now().UTC().Format(time.RFC3339Nano), id, id, in, out)
	if err != nil {
		t.Fatal(err)
	}
}

func sqlExec(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
}

func usageGroups(t *testing.T, e *servertest.Env, by string) map[string]map[string]any {
	t.Helper()
	code, out := e.REST("GET", "/api/v1/usage/summary?group_by="+by, nil)
	if code != 200 {
		t.Fatalf("summary %s: %d %v", by, code, out)
	}
	groups := map[string]map[string]any{}
	for _, g := range out["groups"].([]any) {
		m := g.(map[string]any)
		groups[m["key"].(string)] = m
	}
	return groups
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestUsageDashboardEndToEnd(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "projects", "p", "s1.jsonl")
	if err := os.MkdirAll(filepath.Dir(log), 0o700); err != nil {
		t.Fatal(err)
	}
	claudeLine(t, log, "1", 2000, 200)
	oc := filepath.Join(dir, "opencode.db")
	sqlExec(t, oc, `CREATE TABLE session_v2 (id text PRIMARY KEY, model text, cost real DEFAULT 0 NOT NULL,
		tokens_input integer DEFAULT 0 NOT NULL, tokens_output integer DEFAULT 0 NOT NULL,
		tokens_reasoning integer DEFAULT 0 NOT NULL, tokens_cache_read integer DEFAULT 0 NOT NULL,
		tokens_cache_write integer DEFAULT 0 NOT NULL, time_updated integer NOT NULL)`,
		fmt.Sprintf(`INSERT INTO session_v2 VALUES ('ses_a','{"id":"deepseek-chat","providerID":"deepseek"}',1.0,10000,2000,0,0,0,%d)`, time.Now().UnixMilli()))
	t.Setenv("SUBSPACE_USAGE_CLAUDE", "cc-1="+filepath.Join(dir, "projects"))
	t.Setenv("SUBSPACE_USAGE_OPENCODE", "oc-1="+oc)
	t.Setenv("SUBSPACE_USAGE_INTERVAL", "1s")
	t.Setenv("SUBSPACE_USAGE_CREDITS_USD", "10")

	cfg := server.Config{PublicURL: "http://sub.test", FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond,
		Lease: time.Minute, DBPath: servertest.DB(t)}
	e := servertest.Start(t, cfg)
	wk := e.Register("w1", "worker", "claude-code")
	lead := e.Register("rex", "leader", "other")
	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B", "repos": []string{"o/r"}, "max_cost_eur": 1})
	bid := br["id"].(string)
	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{map[string]any{"title": "a", "spec": "s", "repo": "o/r"}}})["plan"].(map[string]any)
	if code, _ := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatal("approve")
	}
	e.Must(lead, "assign", map[string]any{"task_id": "T-1", "agent_id": "w1"})
	if code, out := e.REST("PUT", "/api/v1/capacity", map[string]any{"global_max_agents": 10,
		"limits": []any{map[string]any{"provider": "anthropic", "model": "*", "max_concurrency": 3}}}); code != 200 {
		t.Fatalf("capacity: %d %v", code, out)
	}

	e.Must(wk, "report_usage", map[string]any{"model": "claude-sonnet-5-5", "task_id": "T-1", "tokens_in": 1000, "tokens_out": 100})

	waitFor(t, "collectors", func() bool {
		g := usageGroups(t, e, "source")
		return g["claude"] != nil && g["opencode"] != nil
	})
	agents := usageGroups(t, e, "agent")
	if w := agents["w1"]; w == nil || !near(w["cost_usd"].(float64), 0.0045) || w["tokens_in"] != float64(1000) {
		t.Fatalf("w1 usage: %v", agents)
	}
	if c := agents["cc-1"]; c == nil || c["tokens_in"] != float64(2000) || !near(c["cost_usd"].(float64), 0.009) {
		t.Fatalf("collected usage: %v", agents)
	}
	if task := usageGroups(t, e, "task")["T-1"]; task == nil || task["requests"] != float64(1) {
		t.Fatalf("task usage: %v", task)
	}

	_, sum := e.REST("GET", "/api/v1/usage/summary", nil)
	credits := sum["credits"].(map[string]any)
	if !near(credits["remaining_usd"].(float64), 10-1.0135) {
		t.Fatalf("credits: %v", credits)
	}
	var budget map[string]any
	for _, b := range sum["briefs"].([]any) {
		if b.(map[string]any)["brief_id"] == bid {
			budget = b.(map[string]any)
		}
	}
	if budget == nil || !near(budget["spent_usd"].(float64), 0.0045) || budget["budget_usd"] != float64(1) {
		t.Fatalf("brief budget: %v", sum["briefs"])
	}

	code, series := e.REST("GET", "/api/v1/usage/series?bucket=hour&group_by=provider", nil)
	if ss, _ := series["series"].([]any); code != 200 || len(ss) != 2 || ss[0].(map[string]any)["key"] != "anthropic" || ss[1].(map[string]any)["key"] != "deepseek" {
		t.Fatalf("series: %d %v", code, series)
	}

	_, caps := e.REST("GET", "/api/v1/usage/capacity", nil)
	model := func(caps map[string]any, provider, name string) map[string]any {
		for _, m := range caps["models"].([]any) {
			if mm := m.(map[string]any); mm["provider"] == provider && mm["model"] == name {
				return mm
			}
		}
		t.Fatalf("no %s/%s in %v", provider, name, caps)
		return nil
	}
	if m := model(caps, "anthropic", "claude-sonnet-5-5"); m["spawnable"] != float64(3) || m["limited_by"] != "concurrency anthropic/*" {
		t.Fatalf("spawnable before 429: %v", m)
	}

	e.Must(wk, "report_usage", map[string]any{"model": "claude-sonnet-5-5", "rate_limited": true})
	_, provs := e.REST("GET", "/api/v1/usage/providers", nil)
	var anthropic map[string]any
	for _, p := range provs["providers"].([]any) {
		if p.(map[string]any)["provider"] == "anthropic" {
			anthropic = p.(map[string]any)
		}
	}
	if anthropic == nil || anthropic["status"] != "throttled" || anthropic["rate_limited_5m"] != float64(1) || anthropic["max_concurrency"] != float64(3) {
		t.Fatalf("provider availability: %v", provs)
	}
	_, caps = e.REST("GET", "/api/v1/usage/capacity", nil)
	if m := model(caps, "anthropic", "claude-sonnet-5-5"); m["spawnable"] != float64(0) || m["limited_by"] != "rate_limited" {
		t.Fatalf("spawnable after 429: %v", m)
	}

	if code, _ := e.REST("POST", "/api/v1/pause-all", map[string]any{"reason": "t"}); code != 200 {
		t.Fatal("pause")
	}
	_, caps = e.REST("GET", "/api/v1/usage/capacity", nil)
	if m := model(caps, "anthropic", "claude-sonnet-5-5"); m["spawnable"] != float64(0) || m["limited_by"] != "paused" {
		t.Fatalf("spawnable while paused: %v", m)
	}

	e.Stop()
	e = servertest.Start(t, cfg)
	claudeLine(t, log, "2", 10, 1)
	sqlExec(t, oc, fmt.Sprintf(`UPDATE session_v2 SET cost=1.5, tokens_input=15000, time_updated=%d WHERE id='ses_a'`, time.Now().UnixMilli()+1))
	waitFor(t, "usage collected after restart", func() bool {
		g := usageGroups(t, e, "agent")
		return g["cc-1"] != nil && g["cc-1"]["tokens_in"] == float64(2010) && g["oc-1"] != nil && g["oc-1"]["tokens_in"] == float64(15000)
	})
	agents = usageGroups(t, e, "agent")
	if c := agents["cc-1"]; c["requests"] != float64(2) {
		t.Fatalf("claude log re-ingested after restart: %v", c)
	}
	if o := agents["oc-1"]; o["requests"] != float64(2) || !near(o["cost_usd"].(float64), 1.5) || o["tokens_out"] != float64(2000) {
		t.Fatalf("opencode session re-ingested after restart: %v", o)
	}
	if w := agents["w1"]; w == nil || !near(w["cost_usd"].(float64), 0.0045) {
		t.Fatalf("usage lost on restart: %v", agents)
	}
}

func TestUsageEndpointsValidateAndRequireOwner(t *testing.T) {
	e := servertest.New(t)
	wk := e.Register("w1", "worker", "claude-code")
	for path, want := range map[string]int{
		"/api/v1/usage/summary?group_by=color":                                    400,
		"/api/v1/usage/summary?from=2026-11-21T00:00:00Z&to=2026-11-20T00:00:00Z": 400,
		"/api/v1/usage/series?bucket=week":                                        400,
		"/api/v1/usage/summary?group_by=model":                                    200,
		"/api/v1/usage/providers":                                                 200,
		"/api/v1/usage/capacity":                                                  200,
	} {
		if code, _ := e.REST("GET", path, nil); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}
	if code, _ := e.RESTAs(wk, "GET", "/api/v1/usage/summary", nil); code != 403 {
		t.Errorf("agent token: %d", code)
	}
}
