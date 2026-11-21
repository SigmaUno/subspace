package server_test

import (
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/servertest"
)

type costGroup struct {
	key     string
	cost    float64
	tokens  float64
	subKeys []string
}

func costGroups(t *testing.T, e *servertest.Env, query url.Values) []costGroup {
	t.Helper()
	code, out := e.REST("GET", "/api/v1/costs?"+query.Encode(), nil)
	if code != 200 {
		t.Fatalf("costs %v: %d %v", query, code, out)
	}
	var groups []costGroup
	for _, g := range out["groups"].([]any) {
		m := g.(map[string]any)
		c := costGroup{key: m["key"].(string), cost: m["cost_eur"].(float64), tokens: m["tokens_in"].(float64) + m["tokens_out"].(float64)}
		split, _ := m["split"].([]any)
		for _, p := range split {
			c.subKeys = append(c.subKeys, p.(map[string]any)["key"].(string))
		}
		groups = append(groups, c)
	}
	return groups
}

func TestCostsAttributionOverHTTP(t *testing.T) {
	e := servertest.New(t)
	lead := e.Register("rex", "leader", "other")
	wk := e.Register("w1", "worker", "claude-code")
	rev := e.Register("r1", "reviewer", "codex")

	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B", "repos": []string{"o/r"}})
	bid := br["id"].(string)
	_, br2 := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B2", "repos": []string{"o/r"}})
	if code, out := e.REST("PUT", "/api/v1/briefs/"+bid+"/components", map[string]any{"components": []any{map[string]any{"name": "api", "paths": []string{"internal/api"}}}}); code != 200 {
		t.Fatalf("components: %d %v", code, out)
	}
	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{
		map[string]any{"title": "a", "spec": "s", "repo": "o/r", "component_id": "C-1"},
		map[string]any{"title": "b", "spec": "s", "repo": "o/r"},
	}})["plan"].(map[string]any)
	if code, _ := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatal("approve")
	}

	e.Must(wk, "report_usage", map[string]any{"model": "claude-sonnet-5-5", "task_id": "T-1", "tokens_in": 100, "tokens_out": 50, "cost_usd": 1.5, "request_id": "r-1"})
	e.Must(wk, "report_usage", map[string]any{"model": "claude-sonnet-5-5", "task_id": "T-1", "tokens_in": 10, "tokens_out": 5, "cost_usd": 0.5})
	e.Must(wk, "report_usage", map[string]any{"model": "claude-sonnet-5-5", "task_id": "T-1", "tokens_in": 200, "tokens_out": 50, "cost_usd": 3, "request_id": "r-1"})
	e.Must(wk, "report_usage", map[string]any{"model": "claude-sonnet-5-5", "rate_limited": true})
	e.Must(rev, "report_usage", map[string]any{"model": "gpt-5", "provider": "openai", "task_id": "T-2", "tokens_in": 20, "tokens_out": 10, "cost_usd": 2})
	e.Must(rev, "report_usage", map[string]any{"model": "gpt-5", "provider": "openai", "brief_id": br2["id"], "tokens_in": 1, "cost_usd": 0.25})

	day := time.Now().UTC().Format("2006-01-02")
	for _, c := range []struct {
		query url.Values
		want  []costGroup
	}{
		{url.Values{"group_by": {"brief"}}, []costGroup{{key: bid, cost: 5.5, tokens: 295}, {key: br2["id"].(string), cost: 0.25, tokens: 1}}},
		{url.Values{"group_by": {"component"}}, []costGroup{{key: "C-1", cost: 3.5, tokens: 265}, {key: "", cost: 2.25, tokens: 31}}},
		{url.Values{"group_by": {"agent"}, "brief_id": {bid}}, []costGroup{{key: "w1", cost: 3.5, tokens: 265}, {key: "r1", cost: 2, tokens: 30}}},
		{url.Values{"group_by": {"role"}}, []costGroup{{key: "worker", cost: 3.5, tokens: 265}, {key: "reviewer", cost: 2.25, tokens: 31}}},
		{url.Values{"group_by": {"kind"}}, []costGroup{{key: "claude-code", cost: 3.5, tokens: 265}, {key: "codex", cost: 2.25, tokens: 31}}},
		{url.Values{"group_by": {"provider"}}, []costGroup{{key: "anthropic", cost: 3.5, tokens: 265}, {key: "openai", cost: 2.25, tokens: 31}}},
		{url.Values{"group_by": {"model"}, "split_by": {"role"}}, []costGroup{
			{key: "claude-sonnet-5-5", cost: 3.5, tokens: 265, subKeys: []string{"worker"}},
			{key: "gpt-5", cost: 2.25, tokens: 31, subKeys: []string{"reviewer"}},
		}},
		{url.Values{"group_by": {"day"}, "split_by": {"provider"}}, []costGroup{{key: day, cost: 5.75, tokens: 296, subKeys: []string{"anthropic", "openai"}}}},
		{url.Values{"group_by": {"task"}, "since": {time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}}, []costGroup{
			{key: "T-1", cost: 3.5, tokens: 265}, {key: "T-2", cost: 2, tokens: 30}, {key: "", cost: 0.25, tokens: 1},
		}},
	} {
		got := costGroups(t, e, c.query)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v: got %+v want %+v", c.query, got, c.want)
		}
	}

	_, out := e.REST("GET", "/api/v1/costs?group_by=brief", nil)
	if total := out["total"].(map[string]any); total["cost_eur"] != 5.75 || total["requests"] != float64(4) {
		t.Fatalf("total: %v", total)
	}
	_, out = e.REST("GET", "/api/v1/costs?group_by=brief&until="+url.QueryEscape(time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339)), nil)
	if len(out["groups"].([]any)) != 0 {
		t.Fatalf("old window: %v", out)
	}
}

func TestCostsRequestValidation(t *testing.T) {
	e := servertest.New(t)
	wk := e.Register("w1", "worker", "claude-code")
	for path, want := range map[string]int{
		"/api/v1/costs?group_by=bogus":                                        400,
		"/api/v1/costs?group_by=agent&split_by=agent":                         400,
		"/api/v1/costs?group_by=agent&split_by=bogus":                         400,
		"/api/v1/costs?since=yesterday":                                       400,
		"/api/v1/costs?since=2026-02-01T00:00:00Z&until=2026-01-01T00:00:00Z": 400,
		"/api/v1/costs?brief_id=B-99":                                         404,
		"/api/v1/costs":                                                       200,
	} {
		if code, _ := e.REST("GET", path, nil); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}
	if code, _ := e.RESTAs("", "GET", "/api/v1/costs", nil); code != 401 {
		t.Errorf("no token: %d", code)
	}
	if code, _ := e.RESTAs(wk, "GET", "/api/v1/costs", nil); code != 403 {
		t.Errorf("agent token: %d", code)
	}
}
