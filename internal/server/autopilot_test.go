package server

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAutopilotOffByDefault(t *testing.T) {
	t.Setenv("SUBSPACE_AUTOPILOT", "")
	e := newEnv(t)
	if code, _ := e.rest("GET", "/api/v1/autopilot", nil); code != 404 {
		t.Fatalf("autopilot mounted while off: %d", code)
	}
}

func TestAutopilotAssignsAndPersistsFlagsAcrossRestart(t *testing.T) {
	t.Setenv("SUBSPACE_AUTOPILOT", "on")
	t.Setenv("SUBSPACE_AUTOPILOT_INTERVAL", "20ms")
	db := filepath.Join(t.TempDir(), "t.db")

	start := func() (*env, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		s, err := New(ctx, Config{DBPath: db, OwnerToken: ownerTok, PublicURL: "http://sub.test",
			FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		go s.Board.Run(ctx)
		ts := httptest.NewServer(s.Handler())
		var once sync.Once
		stop := func() { once.Do(func() { ts.Close(); cancel(); _ = s.Close() }) }
		t.Cleanup(stop)
		return &env{t: t, ts: ts, srv: s}, stop
	}

	e, stop := start()
	lead := e.register("rex", "leader", "other")
	e.register("claude-1", "worker", "claude-code")
	e.register("claude-r", "reviewer", "claude-code")
	e.register("codex-r", "reviewer", "codex")
	_, br := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "Demo", "body": "b", "repos": []string{"o/r"},
		"max_cost_eur": 10, "max_parallel_agents": 5})
	bid := br["id"].(string)
	plan := e.must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{
		map[string]any{"title": "One", "spec": "s", "repo": "o/r"},
	}})["plan"].(map[string]any)

	if code, out := e.rest("GET", "/api/v1/autopilot", nil); code != 200 || out["state"] != "running" {
		t.Fatalf("status: %d %v", code, out)
	}
	if code, _ := e.rest("POST", "/api/v1/autopilot/briefs/B-404", map[string]any{"enabled": true}); code != 404 {
		t.Fatalf("unknown brief: %d", code)
	}
	if code, _ := e.rest("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatal("approve")
	}
	time.Sleep(100 * time.Millisecond)
	if _, out := e.rest("GET", "/api/v1/tasks/T-1", nil); out["status"] != "ready" {
		t.Fatalf("acted on a disabled brief: %v", out)
	}

	if code, out := e.rest("POST", "/api/v1/autopilot/briefs/"+bid, map[string]any{"enabled": true}); code != 200 || out["enabled"] != true {
		t.Fatalf("enable: %d %v", code, out)
	}
	waitFor(t, "task assigned", func() bool {
		_, out := e.rest("GET", "/api/v1/tasks/T-1", nil)
		return out["status"] == "assigned" && out["assignee"] == "claude-1"
	})
	stop()

	e, stop = start()
	_, out := e.rest("GET", "/api/v1/autopilot", nil)
	bs := out["briefs"].([]any)
	if len(bs) != 1 || bs[0].(map[string]any)["enabled"] != true {
		t.Fatalf("flag not restored after restart: %v", out)
	}

	e.rest("POST", "/api/v1/autopilot/briefs/"+bid, map[string]any{"enabled": false})
	stop()
	e, _ = start()
	_, out = e.rest("GET", "/api/v1/autopilot", nil)
	if bs := out["briefs"].([]any); bs[0].(map[string]any)["enabled"] != false {
		t.Fatalf("disable not persisted: %v", out)
	}
}
