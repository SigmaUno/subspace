package server_test

import (
	"context"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
	"github.com/SigmaUno/subspace/internal/spawner"
)

type lifeSpawner struct {
	mu      sync.Mutex
	tokens  map[string]string
	stopped []string
	status  map[string]spawner.Status
}

func (*lifeSpawner) Name() string { return "fake" }

func (s *lifeSpawner) Spawn(_ context.Context, r spawner.Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokens == nil {
		s.tokens = map[string]string{}
	}
	s.tokens[r.AgentID] = r.Token
	return nil
}

func (s *lifeSpawner) Stop(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = append(s.stopped, id)
	return nil
}

func (s *lifeSpawner) Status(_ context.Context, id string) (spawner.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.status[id]; ok {
		return st, nil
	}
	return spawner.StatusUnknown, nil
}

func (s *lifeSpawner) token(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens[id]
}

func (s *lifeSpawner) stops() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.stopped)
}

func lifeConfig(sp spawner.Spawner, db string) server.Config {
	return server.Config{PublicURL: "http://sub.test", DBPath: db, Spawner: sp,
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute}
}

func lifeSpawn(e *servertest.Env, n int) (string, []map[string]any) {
	e.T.Helper()
	code, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B", "repos": []string{"o/r"}})
	if code != 201 {
		e.T.Fatalf("brief: %d %v", code, br)
	}
	code, out := e.REST("POST", "/api/v1/bots/spawn", map[string]any{"brief_id": br["id"], "kind": "claude-code", "model": "m", "n": n})
	if code != 201 {
		e.T.Fatalf("spawn: %d %v", code, out)
	}
	var sps []map[string]any
	for _, s := range out["spawns"].([]any) {
		sps = append(sps, s.(map[string]any))
	}
	return br["id"].(string), sps
}

func lifeSpawns(e *servertest.Env, query string) map[string]map[string]any {
	e.T.Helper()
	code, out := e.REST("GET", "/api/v1/spawns"+query, nil)
	if code != 200 {
		e.T.Fatalf("list spawns: %d %v", code, out)
	}
	m := map[string]map[string]any{}
	for _, s := range out["spawns"].([]any) {
		sp := s.(map[string]any)
		m[sp["id"].(string)] = sp
	}
	return m
}

func lifeWait(e *servertest.Env, id string, ok func(map[string]any) bool) map[string]any {
	e.T.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		sp := lifeSpawns(e, "")[id]
		if ok(sp) {
			return sp
		}
		if time.Now().After(deadline) {
			e.T.Fatalf("spawn %s never reached the expected state: %v", id, sp)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func lifeCode(e *servertest.Env, method, path, tok string) int {
	e.T.Helper()
	req, _ := http.NewRequest(method, e.TS.URL+path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.T.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestSpawnLifecycleOwnerStop(t *testing.T) {
	sp := &lifeSpawner{}
	e := servertest.Start(t, lifeConfig(sp, ""))
	bid, sps := lifeSpawn(e, 2)
	first, second := sps[0], sps[1]
	agent := first["agent_id"].(string)

	listed := lifeSpawns(e, "?brief_id="+bid)
	if len(listed) != 2 {
		t.Fatalf("list: %v", listed)
	}
	if s := listed[first["id"].(string)]; s["lifecycle"] != "connecting" || s["connected_at"] != nil || s["stop_reason"] != nil {
		t.Fatalf("fresh spawn: %v", s)
	}
	if len(lifeSpawns(e, "?brief_id=B-404")) != 0 {
		t.Fatal("brief filter ignored")
	}

	tok := sp.token(agent)
	e.Must(tok, "heartbeat", map[string]any{})
	if s := lifeSpawns(e, "")[first["id"].(string)]; s["connected_at"] == nil || s["lifecycle"] == "connecting" {
		t.Fatalf("connected spawn: %v", s)
	}

	code, out := e.REST("POST", "/api/v1/spawns/"+first["id"].(string)+"/stop", map[string]any{})
	if code != 200 || out["status"] != "stopped" || out["lifecycle"] != "stopped" || out["stop_reason"] != "owner" || out["stopped_at"] == nil {
		t.Fatalf("stop: %d %v", code, out)
	}
	if got := sp.stops(); !slices.Equal(got, []string{agent}) {
		t.Fatalf("spawner stops: %v", got)
	}
	if _, a := e.REST("GET", "/api/v1/agents/"+agent, nil); a["status"] != "revoked" {
		t.Fatalf("agent after stop: %v", a)
	}
	if code := lifeCode(e, "POST", "/mcp", tok); code != http.StatusUnauthorized {
		t.Fatalf("revoked agent on /mcp: %d", code)
	}
	if s := lifeSpawns(e, "")[second["id"].(string)]; s["status"] != "spawned" {
		t.Fatalf("other spawn touched: %v", s)
	}

	if code, _ := e.REST("POST", "/api/v1/spawns/"+first["id"].(string)+"/stop", nil); code != http.StatusConflict {
		t.Fatalf("second stop: %d", code)
	}
	if code, _ := e.REST("POST", "/api/v1/spawns/S-404/stop", nil); code != http.StatusNotFound {
		t.Fatalf("unknown stop: %d", code)
	}
	if code := lifeCode(e, "POST", "/api/v1/spawns/"+second["id"].(string)+"/stop", ""); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated stop: %d", code)
	}
	if code := lifeCode(e, "GET", "/api/v1/spawns", sp.token(second["agent_id"].(string))); code != http.StatusForbidden {
		t.Fatalf("agent token on spawns list: %d", code)
	}
}

func TestSpawnConnectTimeoutFromEnv(t *testing.T) {
	t.Setenv("SUBSPACE_SPAWN_CONNECT_TIMEOUT", "300ms")
	t.Setenv("SUBSPACE_SPAWN_REAP_INTERVAL", "50ms")
	sp := &lifeSpawner{}
	e := servertest.Start(t, lifeConfig(sp, ""))
	_, sps := lifeSpawn(e, 1)
	id, agent := sps[0]["id"].(string), sps[0]["agent_id"].(string)
	got := lifeWait(e, id, func(s map[string]any) bool { return s["status"] != "spawned" })
	if got["status"] != "failed" || got["stop_reason"] != "connect_timeout" || got["error"] == nil {
		t.Fatalf("timed out spawn: %v", got)
	}
	if _, a := e.REST("GET", "/api/v1/agents/"+agent, nil); a["status"] != "revoked" {
		t.Fatalf("agent: %v", a)
	}
}

func TestSpawnReconcileAfterRestart(t *testing.T) {
	sp := &lifeSpawner{}
	db := filepath.Join(t.TempDir(), "t.db")
	var sps []map[string]any
	t.Run("boot", func(t *testing.T) {
		e := servertest.Start(t, lifeConfig(sp, db))
		_, sps = lifeSpawn(e, 3)
	})
	gone, alive, unsure := sps[0], sps[1], sps[2]
	sp.mu.Lock()
	sp.status = map[string]spawner.Status{gone["agent_id"].(string): spawner.StatusExited, alive["agent_id"].(string): spawner.StatusRunning}
	sp.mu.Unlock()

	e := servertest.Start(t, lifeConfig(sp, db))
	got := lifeWait(e, gone["id"].(string), func(s map[string]any) bool { return s["status"] == "stopped" })
	if got["stop_reason"] != "exited" {
		t.Fatalf("exited spawn: %v", got)
	}
	if _, a := e.REST("GET", "/api/v1/agents/"+gone["agent_id"].(string), nil); a["status"] != "revoked" {
		t.Fatalf("exited agent: %v", a)
	}
	got = lifeWait(e, alive["id"].(string), func(s map[string]any) bool { return s["process"] == "running" })
	if got["status"] != "spawned" || got["process_checked_at"] == nil {
		t.Fatalf("running spawn: %v", got)
	}
	if s := lifeSpawns(e, "")[unsure["id"].(string)]; s["status"] != "spawned" || s["process"] != "unknown" {
		t.Fatalf("unknown spawn: %v", s)
	}
}
