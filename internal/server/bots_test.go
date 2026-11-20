package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/spawner"
)

type stopSpawner struct {
	mu      sync.Mutex
	spawned []string
	stopped []string
}

func (*stopSpawner) Name() string { return "test" }

func (s *stopSpawner) Spawn(_ context.Context, r spawner.Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spawned = append(s.spawned, r.AgentID)
	return nil
}

func (s *stopSpawner) Stop(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = append(s.stopped, id)
	return nil
}

func newBotsEnv(t *testing.T, sp spawner.Spawner) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s, err := New(ctx, Config{DBPath: filepath.Join(t.TempDir(), "t.db"), OwnerToken: ownerTok, PublicURL: "http://sub.test",
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute, Spawner: sp})
	if err != nil {
		t.Fatal(err)
	}
	go s.Board.Run(ctx)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); cancel(); _ = s.Close() })
	return &env{t: t, ts: ts, srv: s}
}

func (e *env) restAs(tok, method, path string) int {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.ts.URL+path, nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func botByID(t *testing.T, out map[string]any, id string) map[string]any {
	t.Helper()
	for _, b := range out["bots"].([]any) {
		if m := b.(map[string]any); m["id"] == id {
			return m
		}
	}
	t.Fatalf("bot %s not listed: %v", id, out)
	return nil
}

func TestBotsLifecycle(t *testing.T) {
	sp := &stopSpawner{}
	e := newBotsEnv(t, sp)

	code, out := e.rest("POST", "/api/v1/bots", map[string]any{"name": "ds-1", "kind": "opencode-deepseek", "model": "deepseek-chat"})
	if code != 201 {
		t.Fatalf("create: %d %v", code, out)
	}
	bot := out["bot"].(map[string]any)
	if bot["provider"] != "deepseek" || bot["kind"] != "opencode" || bot["role"] != "worker" {
		t.Fatalf("bot defaults: %v", bot)
	}
	tok := out["connect"].(map[string]any)["token"].(string)

	if code, out := e.rest("POST", "/api/v1/bots", map[string]any{"name": "ds-1", "kind": "codex"}); code != 409 {
		t.Fatalf("duplicate: %d %v", code, out)
	}
	if code, _ := e.rest("POST", "/api/v1/bots", map[string]any{"name": "x", "kind": "nope"}); code != 400 {
		t.Fatalf("bad kind: %d", code)
	}
	if code, _ := e.rest("POST", "/api/v1/bots", map[string]any{"name": "Bad Name", "kind": "codex"}); code != 400 {
		t.Fatalf("bad name: %d", code)
	}

	_, list := e.rest("GET", "/api/v1/bots", nil)
	if botByID(t, list, "ds-1")["live"] != false {
		t.Fatalf("unseen bot must not be live: %v", list)
	}
	e.must(tok, "whoami", map[string]any{})
	_, list = e.rest("GET", "/api/v1/bots", nil)
	if botByID(t, list, "ds-1")["live"] != true {
		t.Fatalf("bot that called MCP must be live: %v", list)
	}

	code, out = e.rest("POST", "/api/v1/bots/ds-1/rotate-token", nil)
	if code != 200 {
		t.Fatalf("rotate: %d %v", code, out)
	}
	fresh := out["connect"].(map[string]any)["token"].(string)
	if fresh == tok {
		t.Fatal("rotate returned the same token")
	}
	if res := e.rpc(tok, "tools/list", nil); res["error"] == nil && res["result"] != nil {
		t.Fatal("old token still works after rotate")
	}
	e.must(fresh, "whoami", map[string]any{})

	if code, out = e.rest("POST", "/api/v1/bots/ds-1/pause", map[string]any{"reason": "cooldown"}); code != 200 {
		t.Fatalf("pause: %d %v", code, out)
	}
	if b := out["bot"].(map[string]any); b["status"] != "paused" || b["pause_reason"] != "cooldown" || b["live"] != false {
		t.Fatalf("paused bot: %v", b)
	}
	if code, out = e.rest("POST", "/api/v1/bots/ds-1/resume", nil); code != 200 || out["bot"].(map[string]any)["status"] != "active" {
		t.Fatalf("resume: %d %v", code, out)
	}

	code, out = e.rest("POST", "/api/v1/bots/ds-1/kill", map[string]any{"reason": "runaway"})
	if code != 200 || out["stopped"] != true || out["bot"].(map[string]any)["status"] != "paused" {
		t.Fatalf("kill: %d %v", code, out)
	}
	if len(sp.stopped) != 1 || sp.stopped[0] != "ds-1" {
		t.Fatalf("spawner stops: %v", sp.stopped)
	}

	if code, out = e.rest("DELETE", "/api/v1/bots/ds-1", nil); code != 200 || out["bot"].(map[string]any)["status"] != "revoked" {
		t.Fatalf("remove: %d %v", code, out)
	}
	for _, p := range []string{"revoke", "pause", "kill", "resume", "rotate-token"} {
		if code, out := e.rest("POST", "/api/v1/bots/ds-1/"+p, nil); code != 409 {
			t.Fatalf("%s on revoked bot: %d %v", p, code, out)
		}
	}
	if code, _ := e.rest("POST", "/api/v1/bots/ghost/pause", nil); code != 404 {
		t.Fatalf("missing bot pause: %d", code)
	}
	if code, _ := e.rest("GET", "/api/v1/bots/ghost", nil); code != 404 {
		t.Fatalf("missing bot get: %d", code)
	}

	_, list = e.rest("GET", "/api/v1/bots?status=revoked", nil)
	botByID(t, list, "ds-1")
}

func TestBotsBulkAndSpawn(t *testing.T) {
	sp := &stopSpawner{}
	e := newBotsEnv(t, sp)

	code, out := e.rest("POST", "/api/v1/bots/bulk", map[string]any{"prefix": "cc", "count": 3, "kind": "claude-code", "model": "m"})
	if code != 201 || len(out["bots"].([]any)) != 3 {
		t.Fatalf("bulk: %d %v", code, out)
	}
	_, list := e.rest("GET", "/api/v1/bots?role=worker", nil)
	if botByID(t, list, "cc-003")["provider"] != "anthropic" {
		t.Fatalf("bulk provider: %v", list)
	}

	if code, out := e.rest("POST", "/api/v1/bots/spawn", map[string]any{"brief_id": "B-404", "kind": "codex", "model": "m"}); code != 404 {
		t.Fatalf("spawn unknown brief: %d %v", code, out)
	}
	_, br := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "T", "body": "b", "repos": []string{"o/r"}, "max_cost_eur": 10, "max_parallel_agents": 5})
	bid := br["id"].(string)

	code, out = e.rest("POST", "/api/v1/bots/spawn", map[string]any{"brief_id": bid, "kind": "codex", "model": "gpt", "n": 2})
	if code != 201 || len(out["spawns"].([]any)) != 2 {
		t.Fatalf("spawn: %d %v", code, out)
	}
	for _, s := range out["spawns"].([]any) {
		if s.(map[string]any)["status"] != "spawned" || s.(map[string]any)["spawner"] != "test" {
			t.Fatalf("spawn record: %v", s)
		}
	}
	if len(sp.spawned) != 2 {
		t.Fatalf("spawner calls: %v", sp.spawned)
	}
	_, list = e.rest("GET", "/api/v1/bots", nil)
	if botByID(t, list, sp.spawned[0])["spawned"] != true {
		t.Fatalf("spawned bot flag: %v", list)
	}

	e.rest("POST", "/api/v1/pause-all", map[string]any{"reason": "stop"})
	if code, out := e.rest("POST", "/api/v1/bots/spawn", map[string]any{"brief_id": bid, "kind": "codex", "model": "gpt"}); code != 423 {
		t.Fatalf("spawn while paused: %d %v", code, out)
	}
	if code, _ := e.rest("POST", "/api/v1/bots/spawn", map[string]any{"brief_id": bid, "kind": "other", "model": "gpt"}); code != 400 {
		t.Fatalf("spawn other kind: %d", code)
	}
}

func TestBotsRequireOwner(t *testing.T) {
	e := newBotsEnv(t, nil)
	tok := e.register("w1", "worker", "claude-code")

	for _, path := range []string{"/api/v1/bots", "/api/v1/bots/w1"} {
		if code := e.restAs(tok, "GET", path); code != 403 {
			t.Fatalf("agent token on %s: %d", path, code)
		}
		if code := e.restAs("", "GET", path); code != 401 {
			t.Fatalf("anonymous on %s: %d", path, code)
		}
	}
	if code := e.restAs(tok, "POST", "/api/v1/bots/w1/revoke"); code != 403 {
		t.Fatalf("agent revoking via bots: %d", code)
	}
}
