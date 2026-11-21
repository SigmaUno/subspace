package mcp_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

type infoEnv struct {
	*servertest.Env
	lead, worker, tester, reviewer string
	brief, task                    string
}

func newInfoEnv(t *testing.T) *infoEnv {
	t.Helper()
	e := &infoEnv{Env: servertest.New(t)}
	e.lead = e.Register("rex", "leader", "other")
	e.worker = e.Register("w1", "worker", "claude-code")
	e.tester = e.Register("tess", "tester", "claude-code")
	e.reviewer = e.Register("rev", "reviewer", "codex")
	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "Info", "repos": []string{"o/r"}})
	e.brief = br["id"].(string)
	plan := e.Must(e.lead, "propose_plan", map[string]any{"brief_id": e.brief, "tasks": []any{
		map[string]any{"title": "implement frobnicator", "spec": "s", "repo": "o/r"},
		map[string]any{"title": "document it", "spec": "s", "repo": "o/r"},
	}})["plan"].(map[string]any)
	if code, _ := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	e.task = plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
	e.Must(e.lead, "assign", map[string]any{"task_id": e.task, "agent_id": "w1"})
	e.Must(e.worker, "claim_task", map[string]any{"task_id": e.task})
	e.Must(e.worker, "ask_owner", map[string]any{"task_id": e.task, "question": "which database?"})
	if code, _ := e.REST("PUT", "/api/v1/capacity", map[string]any{"global_max_agents": 300, "limits": []any{}}); code != 200 {
		t.Fatalf("capacity: %d", code)
	}
	e.Must(e.lead, "request_spawn", map[string]any{"brief_id": e.brief, "worker_kind": "claude-code", "model": "m", "n": 1})
	if code, _ := e.REST("PUT", "/api/v1/briefs/"+e.brief+"/components", map[string]any{"components": []any{
		map[string]any{"name": "store", "paths": []string{"internal/store"}},
		map[string]any{"name": "api", "depends_on": []string{"store"}},
	}}); code != 200 {
		t.Fatalf("components: %d", code)
	}
	return e
}

func list(t *testing.T, out map[string]any, key string) []any {
	t.Helper()
	l, ok := out[key].([]any)
	if !ok {
		t.Fatalf("%s is not a list in %v", key, out)
	}
	return l
}

func TestLeaderInfoToolsOverMCP(t *testing.T) {
	e := newInfoEnv(t)

	f := e.Must(e.lead, "get_forecast", map[string]any{"brief_id": e.brief})["forecast"].(map[string]any)
	if f["brief_id"] != e.brief {
		t.Fatalf("forecast %v", f)
	}

	r := e.Must(e.lead, "get_report", map[string]any{"brief_id": e.brief})
	rep := r["report"].(map[string]any)
	if rep["brief_id"] != e.brief || rep["total"] != float64(2) || r["truncated"] != false {
		t.Fatalf("report %v", r)
	}

	s := e.Must(e.lead, "search", map[string]any{"query": "frobnicator", "kind": "task"})
	hits := list(t, s, "results")
	if len(hits) != 1 || hits[0].(map[string]any)["id"] != e.task || s["truncated"] != false {
		t.Fatalf("search %v", s)
	}

	st := e.Must(e.lead, "agent_stats", map[string]any{"agent_id": "w1"})
	agents := list(t, st, "agents")
	if len(agents) != 1 {
		t.Fatalf("agent_stats %v", st)
	}
	a := agents[0].(map[string]any)
	if a["agent_id"] != "w1" || a["role"] != "worker" || a["tasks_completed"] != float64(0) {
		t.Fatalf("w1 stats %v", a)
	}
	all := list(t, e.Must(e.lead, "agent_stats", map[string]any{}), "agents")
	var ids []string
	for _, x := range all {
		ids = append(ids, x.(map[string]any)["agent_id"].(string))
	}
	if !slices.Equal(ids, []string{"claude-code-b-1-1", "rev", "rex", "tess", "w1"}) {
		t.Fatalf("agent ids %v", ids)
	}

	ds := list(t, e.Must(e.lead, "list_decisions", map[string]any{}), "decisions")
	if len(ds) != 1 {
		t.Fatalf("decisions %v", ds)
	}
	d := ds[0].(map[string]any)
	if d["kind"] != "question" || d["task_id"] != e.task {
		t.Fatalf("decision %v", d)
	}
	if _, ok := d["actions"]; ok {
		t.Fatalf("decision carries more than titles: %v", d)
	}

	sp := list(t, e.Must(e.lead, "list_spawns", map[string]any{"brief_id": e.brief}), "spawns")
	if len(sp) != 1 || sp[0].(map[string]any)["brief_id"] != e.brief {
		t.Fatalf("spawns %v", sp)
	}
	if other := list(t, e.Must(e.lead, "list_spawns", map[string]any{"brief_id": "B-999"}), "spawns"); len(other) != 0 {
		t.Fatalf("spawns of another brief %v", other)
	}

	cs := list(t, e.Must(e.lead, "get_components", map[string]any{"brief_id": e.brief}), "components")
	if len(cs) != 2 || cs[1].(map[string]any)["depends_on"].([]any)[0] != "C-1" {
		t.Fatalf("components %v", cs)
	}
}

func TestListDecisionsIgnoresUnflushedActivityAsStale(t *testing.T) {
	e := servertest.Start(t, server.Config{PublicURL: "http://sub.test",
		FlushEvery: time.Hour, SweepEvery: time.Hour, Lease: time.Minute})
	lead := e.Register("rex", "leader", "other")
	worker := e.Register("w1", "worker", "claude-code")
	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "Info", "repos": []string{"o/r"}})
	brief := br["id"].(string)
	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": brief, "tasks": []any{
		map[string]any{"title": "implement frobnicator", "spec": "s", "repo": "o/r"},
	}})["plan"].(map[string]any)
	if code, _ := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	task := plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
	e.Must(lead, "assign", map[string]any{"task_id": task, "agent_id": "w1"})
	e.Must(worker, "claim_task", map[string]any{"task_id": task})

	if ds := list(t, e.Must(lead, "list_decisions", map[string]any{}), "decisions"); len(ds) != 0 {
		t.Fatalf("active agent reported as stale: %v", ds)
	}
}

func TestLeaderInfoToolErrors(t *testing.T) {
	e := newInfoEnv(t)
	for tool, args := range map[string]map[string]any{
		"get_forecast":   {"brief_id": "B-999"},
		"get_report":     {"brief_id": "B-999"},
		"get_components": {"brief_id": "B-999"},
		"agent_stats":    {"agent_id": "nobody"},
	} {
		out, isErr := e.Call(e.lead, tool, args)
		if !isErr || out["error"].(map[string]any)["code"] != "not_found" {
			t.Errorf("%s unknown id: %v", tool, out)
		}
	}
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"get_forecast", map[string]any{}},
		{"get_report", map[string]any{}},
		{"get_components", map[string]any{}},
		{"search", map[string]any{}},
		{"search", map[string]any{"query": "x", "limit": 101}},
	} {
		out, isErr := e.Call(e.lead, c.tool, c.args)
		if !isErr || out["error"].(map[string]any)["code"] != "bad_request" {
			t.Errorf("%s %v: %v", c.tool, c.args, out)
		}
	}
}

func TestLeaderInfoToolsAreLeaderOnly(t *testing.T) {
	e := newInfoEnv(t)
	tools := []string{"get_forecast", "get_report", "search", "agent_stats", "list_decisions", "list_spawns", "get_components"}
	listed := func(tok string) []string {
		res := e.RPC(tok, "tools/list", map[string]any{})["result"].(map[string]any)
		var names []string
		for _, x := range res["tools"].([]any) {
			names = append(names, x.(map[string]any)["name"].(string))
		}
		return names
	}
	for _, n := range tools {
		if !slices.Contains(listed(e.lead), n) {
			t.Errorf("leader lacks %s", n)
		}
	}
	for role, tok := range map[string]string{"worker": e.worker, "tester": e.tester, "reviewer": e.reviewer} {
		names := listed(tok)
		for _, n := range tools {
			if slices.Contains(names, n) {
				t.Errorf("%s lists %s", role, n)
			}
			out, _ := e.Call(tok, n, map[string]any{"brief_id": e.brief, "query": "x"})
			if out["rpc_error"] == nil {
				t.Errorf("%s called %s: %v", role, n, out)
			}
		}
	}
}

func TestLeaderInfoListsAreTruncated(t *testing.T) {
	e := newInfoEnv(t)
	comps := make([]any, 0, 250)
	for i := range 250 {
		comps = append(comps, map[string]any{"name": fmt.Sprintf("c%03d", i)})
	}
	if code, out := e.REST("PUT", "/api/v1/briefs/"+e.brief+"/components", map[string]any{"components": comps}); code != 200 {
		t.Fatalf("components: %d %v", code, out)
	}
	out := e.Must(e.lead, "get_components", map[string]any{"brief_id": e.brief})
	if n := len(list(t, out, "components")); n != 200 || out["truncated"] != true {
		t.Fatalf("components len %d truncated %v", n, out["truncated"])
	}
}

func TestLeaderInfoToolsExemptFromLoopProtection(t *testing.T) {
	e := newInfoEnv(t)
	for range 12 {
		e.Must(e.lead, "list_decisions", map[string]any{})
		e.Must(e.lead, "get_report", map[string]any{"brief_id": e.brief})
	}
	_, a := e.REST("GET", "/api/v1/agents/rex", nil)
	if a["status"] != "active" {
		t.Fatalf("leader %v", a)
	}
}
