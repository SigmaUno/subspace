package repos_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
	"github.com/SigmaUno/subspace/internal/spawner"
)

type recordingSpawner struct {
	mu   sync.Mutex
	reqs []spawner.Request
	fail string
}

func (*recordingSpawner) Name() string { return "recording" }

func (r *recordingSpawner) Spawn(_ context.Context, req spawner.Request) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	if r.fail != "" {
		return errors.New("push to remote failed: " + r.fail)
	}
	return nil
}

func (*recordingSpawner) Stop(context.Context, string) error { return nil }

func (*recordingSpawner) Status(context.Context, string) (spawner.Status, error) {
	return spawner.StatusRunning, nil
}

func (r *recordingSpawner) last(t *testing.T) spawner.Request {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reqs) == 0 {
		t.Fatal("no spawn reached the spawner")
	}
	return r.reqs[len(r.reqs)-1]
}

func TestSpawnPassesAgentCredentialsOfTheBriefRepo(t *testing.T) {
	const secret = "forge-bot-credential-for-tests-0123456789"
	t.Setenv("FORGE_AGENT_TOKEN", secret)
	rec := &recordingSpawner{}
	e := servertest.Start(t, server.Config{PublicURL: "http://sub.test", Spawner: rec,
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})
	rest(t, e, 201, "POST", reposPath, repo("acme/api", map[string]any{"token_env": "FORGE_SERVER_TOKEN", "agent_token_env": "FORGE_AGENT_TOKEN"}))
	rest(t, e, 201, "POST", reposPath, repo("acme/plain", nil))
	lead := e.Register("rex", "leader", "other")
	brief := func(repos ...string) string {
		return rest(t, e, 201, "POST", "/api/v1/briefs", map[string]any{"title": "B", "repos": repos})["id"].(string)
	}
	spawn := func(bid string) []any {
		return e.Must(lead, "request_spawn", map[string]any{"brief_id": bid, "worker_kind": "claude-code", "model": "m", "n": 1})["spawns"].([]any)
	}

	spawn(brief("acme/api"))
	env := rec.last(t).Env
	want := map[string]string{
		"SUBSPACE_GIT_TOKEN": secret, "SUBSPACE_GIT_URL": "https://forge.example.com", "SUBSPACE_GIT_DEFAULT_BRANCH": "main",
		"GITEA_SERVER_TOKEN": secret, "GITEA_SERVER_URL": "https://forge.example.com",
		"GIT_CONFIG_KEY_0": "credential.https://forge.example.com.helper",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, env[k], v)
		}
	}
	if !strings.Contains(env["GIT_CONFIG_VALUE_0"], "$SUBSPACE_GIT_TOKEN") || strings.Contains(env["GIT_CONFIG_VALUE_0"], secret) {
		t.Errorf("credential helper %q must read the token from the environment", env["GIT_CONFIG_VALUE_0"])
	}

	spawn(brief("acme/plain"))
	if env := rec.last(t).Env; len(env) != 0 {
		t.Errorf("repo without agent_token_env got %v", env)
	}
	spawn(brief("acme/api", "acme/plain"))
	if env := rec.last(t).Env; len(env) != 0 {
		t.Errorf("a brief with several repos got %v", env)
	}

	rec.mu.Lock()
	rec.fail = secret
	rec.mu.Unlock()
	out := spawn(brief("acme/api"))[0].(map[string]any)
	if out["status"] != "failed" || strings.Contains(fmt.Sprint(out), secret) {
		t.Fatalf("failed spawn leaks the credential: %v", out)
	}

	_, audit := e.REST("GET", "/api/v1/audit?limit=1000", nil)
	_, listed := e.REST("GET", "/api/v1/spawns", nil)
	_, got := e.REST("GET", reposPath+"/acme/api", nil)
	for name, body := range map[string]any{"audit": audit, "spawns": listed, "repo": got} {
		if strings.Contains(fmt.Sprint(body), secret) {
			t.Errorf("%s carries the credential", name)
		}
	}
}
