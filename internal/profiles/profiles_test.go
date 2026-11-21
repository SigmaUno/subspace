package profiles_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/profiles"
	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
	"github.com/SigmaUno/subspace/internal/spawner"
)

const profilesPath = "/api/v1/agent-profiles"

func start(t *testing.T) *servertest.Env {
	t.Helper()
	return servertest.Start(t, server.Config{PublicURL: "http://sub.test", Spawner: spawner.Noop{},
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})
}

func rest(t *testing.T, e *servertest.Env, want int, method, path string, body any) map[string]any {
	t.Helper()
	code, out := e.REST(method, path, body)
	if code != want {
		t.Fatalf("%s %s: %d, want %d: %v", method, path, code, want, out)
	}
	return out
}

func names(t *testing.T, e *servertest.Env) []string {
	t.Helper()
	var out []string
	for _, p := range rest(t, e, 200, "GET", profilesPath, nil)["profiles"].([]any) {
		out = append(out, p.(map[string]any)["name"].(string))
	}
	return out
}

func errorCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func TestBuiltinProfilesSeededOnceAndEditable(t *testing.T) {
	e := start(t)
	want := []string{"claude-code-worker", "codex-worker", "opencode-deepseek-worker"}
	if got := names(t, e); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("profiles = %v, want %v", got, want)
	}
	edited := rest(t, e, 200, "PUT", profilesPath+"/codex-worker", map[string]any{"kind": "codex", "model": "gpt-5", "description": "mine"})
	if edited["model"] != "gpt-5" || edited["role"] != "worker" {
		t.Fatalf("edited = %v", edited)
	}
	rest(t, e, 204, "DELETE", profilesPath+"/claude-code-worker", nil)
	if err := (&profiles.Store{St: e.Srv.Store}).Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := names(t, e); fmt.Sprint(got) != fmt.Sprint(want[1:]) {
		t.Fatalf("a deleted built-in came back: %v", got)
	}
	rest(t, e, 404, "GET", profilesPath+"/claude-code-worker", nil)
	rest(t, e, 404, "DELETE", profilesPath+"/claude-code-worker", nil)
}

func TestProfileValidation(t *testing.T) {
	e := start(t)
	valid := func() map[string]any { return map[string]any{"name": "mine", "kind": "claude-code"} }
	with := func(k string, v any) map[string]any {
		m := valid()
		m[k] = v
		return m
	}
	for name, body := range map[string]map[string]any{
		"bad name":          with("name", "Bad Name"),
		"missing kind":      {"name": "mine"},
		"unknown kind":      with("kind", "gpt"),
		"bad role":          with("role", "boss"),
		"bad model":         with("model", "-x"),
		"provider mismatch": with("provider", "openai"),
		"bad provider":      with("provider", "No Good"),
		"bad expiry":        with("expires_in", "soon"),
		"negative expiry":   with("expires_in", "-1h"),
		"count too big":     with("count", 1001),
		"negative count":    with("count", -1),
		"relative callback": with("callback_url", "/hook"),
		"callback creds":    with("callback_url", "https://u:p@example.com/hook"),
		"callback scheme":   with("callback_url", "ftp://example.com/hook"),
		"unknown field":     with("flavour", "x"),
	} {
		if out := rest(t, e, 400, "POST", profilesPath, body); errorCode(out) != "bad_request" {
			t.Errorf("%s: %v", name, out)
		}
	}
	if got := names(t, e); len(got) != 3 {
		t.Fatalf("invalid profiles were stored: %v", got)
	}
	rest(t, e, 201, "POST", profilesPath, with("provider", "anthropic"))
	if out := rest(t, e, 409, "POST", profilesPath, valid()); errorCode(out) != "conflict" {
		t.Fatalf("duplicate: %v", out)
	}
	if out := rest(t, e, 400, "PUT", profilesPath+"/mine", with("name", "other")); errorCode(out) != "bad_request" {
		t.Fatalf("rename: %v", out)
	}
	rest(t, e, 404, "PUT", profilesPath+"/ghost", map[string]any{"kind": "codex"})
	viewer := rest(t, e, 201, "POST", "/api/v1/tokens", map[string]any{"name": "v", "scope": "viewer"})["secret"].(string)
	if code, _ := e.RESTAs(viewer, "GET", profilesPath, nil); code != 200 {
		t.Fatalf("viewer read: %d", code)
	}
	if code, _ := e.RESTAs(viewer, "POST", profilesPath, valid()); code != 403 {
		t.Fatalf("viewer write: %d", code)
	}
	if code, _ := e.RESTAs("", "GET", profilesPath, nil); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
}

func TestRegistrationFromProfileExplicitFieldsOverride(t *testing.T) {
	e := start(t)
	rest(t, e, 201, "POST", profilesPath, map[string]any{
		"name": "fleet", "role": "tester", "kind": "claude-code", "model": "m-profile", "provider": "anthropic",
		"expires_in": "720h", "count": 3,
	})
	agent := func(out map[string]any) map[string]any { return out["agent"].(map[string]any) }

	got := agent(rest(t, e, 201, "POST", "/api/v1/agents", map[string]any{"id": "a-one", "profile": "fleet"}))
	if got["role"] != "tester" || got["kind"] != "claude-code" || got["model"] != "m-profile" || got["provider"] != "anthropic" || got["token_expires_at"] == nil {
		t.Fatalf("profile defaults: %v", got)
	}
	got = agent(rest(t, e, 201, "POST", "/api/v1/agents", map[string]any{"id": "a-two", "profile": "fleet", "role": "worker", "model": "m-explicit", "provider": ""}))
	if got["role"] != "worker" || got["model"] != "m-explicit" || got["kind"] != "claude-code" {
		t.Fatalf("explicit overrides: %v", got)
	}
	got = agent(rest(t, e, 201, "POST", "/api/v1/agents", map[string]any{"id": "a-three", "profile": "fleet", "kind": "codex"}))
	if got["kind"] != "codex" || got["provider"] != "openai" || got["model"] != "m-profile" {
		t.Fatalf("kind override must not inherit the provider: %v", got)
	}

	bulk := rest(t, e, 201, "POST", "/api/v1/agents/bulk", map[string]any{"prefix": "b", "profile": "fleet"})["agents"].([]any)
	if len(bulk) != 3 || agent(bulk[0].(map[string]any))["id"] != "b-001" || agent(bulk[2].(map[string]any))["role"] != "tester" {
		t.Fatalf("bulk from profile: %v", bulk)
	}
	bulk = rest(t, e, 201, "POST", "/api/v1/agents/bulk", map[string]any{"prefix": "c", "profile": "fleet", "count": 2, "model": "m-explicit"})["agents"].([]any)
	if len(bulk) != 2 || agent(bulk[1].(map[string]any))["model"] != "m-explicit" {
		t.Fatalf("bulk overrides: %v", bulk)
	}

	if out := rest(t, e, 400, "POST", "/api/v1/agents", map[string]any{"id": "x", "profile": "ghost"}); errorCode(out) != "bad_request" {
		t.Fatalf("unknown profile: %v", out)
	}
	rest(t, e, 400, "POST", "/api/v1/agents", map[string]any{"id": "x", "profile": 7})
	rest(t, e, 400, "POST", "/api/v1/agents", map[string]any{"id": "x", "profile": "fleet", "role": "boss"})
	rest(t, e, 400, "POST", "/api/v1/agents/bulk", map[string]any{"prefix": "x", "profile": "fleet", "count": 1001})
	if code, _ := e.RESTAs("", "POST", "/api/v1/agents", map[string]any{"id": "x", "profile": "ghost"}); code != 401 {
		t.Fatalf("anonymous profile lookup: %d", code)
	}
	if code, _ := e.RESTAs("ssu_invalid", "POST", "/api/v1/agents", map[string]any{"id": "x", "profile": "ghost"}); code != 401 {
		t.Fatalf("invalid token profile lookup: %d", code)
	}
	if _, out := e.REST("GET", "/api/v1/agents/x", nil); errorCode(out) != "not_found" {
		t.Fatalf("a rejected registration created an agent: %v", out)
	}
}

func TestProfileCallbackDefault(t *testing.T) {
	t.Setenv("SUBSPACE_CALLBACK_ALLOW_PRIVATE", "true")
	e := start(t)
	rest(t, e, 201, "POST", profilesPath, map[string]any{"name": "hooked", "kind": "codex", "model": "m", "callback_url": "http://127.0.0.1:9/wake"})
	out := rest(t, e, 201, "POST", "/api/v1/agents", map[string]any{"id": "a-hook", "role": "worker", "profile": "hooked"})
	cb, _ := out["callback"].(map[string]any)
	if cb["url"] != "http://127.0.0.1:9/wake" || cb["secret"] == "" {
		t.Fatalf("callback = %v", out["callback"])
	}
	out = rest(t, e, 201, "POST", "/api/v1/agents", map[string]any{"id": "a-nohook", "role": "worker", "profile": "hooked", "callback_url": ""})
	if out["callback"] == nil {
		t.Fatalf("an empty callback_url is not an override: %v", out)
	}
}

func TestRequestSpawnFromProfile(t *testing.T) {
	e := start(t)
	lead := e.Register("rex", "leader", "other")
	brief := rest(t, e, 201, "POST", "/api/v1/briefs", map[string]any{"title": "t", "body": "b", "repos": []string{"o/r"}, "max_cost_eur": 10, "max_parallel_agents": 10})["id"].(string)

	spawns := e.Must(lead, "request_spawn", map[string]any{"brief_id": brief, "profile": "claude-code-worker", "n": 1})["spawns"].([]any)
	if s := spawns[0].(map[string]any); s["kind"] != "claude-code" || s["model"] != "claude-sonnet-5-5" || s["provider"] != "anthropic" {
		t.Fatalf("spawn = %v", s)
	}
	spawns = e.Must(lead, "request_spawn", map[string]any{"brief_id": brief, "profile": "claude-code-worker", "model": "other-model", "worker_kind": "codex", "n": 1})["spawns"].([]any)
	if s := spawns[0].(map[string]any); s["kind"] != "codex" || s["model"] != "other-model" || s["provider"] != "openai" {
		t.Fatalf("overridden spawn = %v", s)
	}
	e.MustFail(lead, "request_spawn", map[string]any{"brief_id": brief, "profile": "ghost", "n": 1}, "bad_request")
	e.MustFail(lead, "request_spawn", map[string]any{"brief_id": brief, "n": 1}, "bad_request")

	tools := e.RPC(lead, "tools/list", map[string]any{})["result"].(map[string]any)["tools"].([]any)
	for _, x := range tools {
		tool := x.(map[string]any)
		if tool["name"] != "request_spawn" {
			continue
		}
		schema := tool["inputSchema"].(map[string]any)
		if _, ok := schema["properties"].(map[string]any)["profile"]; !ok || fmt.Sprint(schema["required"]) != "[brief_id n]" {
			t.Fatalf("request_spawn schema = %v", schema)
		}
	}
}

func TestAutopilotSpawnOptionsUseProfileAndBlockDeletion(t *testing.T) {
	t.Setenv("SUBSPACE_AUTOPILOT", "on")
	t.Setenv("SUBSPACE_AUTOPILOT_INTERVAL", "50ms")
	e := start(t)
	lead := e.Register("rex", "leader", "other")
	brief := rest(t, e, 201, "POST", "/api/v1/briefs", map[string]any{"title": "t", "body": "b", "repos": []string{"o/r"}, "max_cost_eur": 10, "max_parallel_agents": 10})["id"].(string)
	spawnPath := "/api/v1/autopilot/briefs/" + brief + "/spawn"

	rest(t, e, 400, "PUT", spawnPath, map[string]any{"profile": "ghost"})
	rest(t, e, 400, "PUT", spawnPath, map[string]any{"worker_kind": "gpt", "model": "m"})
	rest(t, e, 400, "PUT", spawnPath, map[string]any{"model": "m"})
	rest(t, e, 404, "PUT", "/api/v1/autopilot/briefs/B-404/spawn", map[string]any{"profile": "codex-worker"})

	out := rest(t, e, 200, "PUT", spawnPath, map[string]any{"profile": "claude-code-worker", "model": "m-explicit"})
	if s, _ := out["spawn"].(map[string]any); s["profile"] != "claude-code-worker" || s["model"] != "m-explicit" {
		t.Fatalf("spawn options = %v", out)
	}
	if out := rest(t, e, 409, "DELETE", profilesPath+"/claude-code-worker", nil); errorCode(out) != "conflict" {
		t.Fatalf("delete in use: %v", out)
	}
	rest(t, e, 204, "DELETE", profilesPath+"/codex-worker", nil)

	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": brief, "tasks": []any{map[string]any{"title": "One", "spec": "s", "repo": "o/r"}}})["plan"].(map[string]any)
	rest(t, e, 200, "POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{})
	rest(t, e, 200, "POST", "/api/v1/autopilot/briefs/"+brief, map[string]any{"enabled": true})

	deadline := time.Now().Add(10 * time.Second)
	for {
		spawns := rest(t, e, 200, "GET", "/api/v1/spawns?brief_id="+brief, nil)["spawns"].([]any)
		if len(spawns) > 0 {
			if s := spawns[0].(map[string]any); s["kind"] != "claude-code" || s["model"] != "m-explicit" || s["provider"] != "anthropic" {
				t.Fatalf("autopilot spawn = %v", s)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("autopilot did not spawn from the profile")
		}
		time.Sleep(25 * time.Millisecond)
	}

	rest(t, e, 200, "PUT", spawnPath, map[string]any{})
	if flags := rest(t, e, 200, "GET", "/api/v1/autopilot", nil)["briefs"].([]any); flags[0].(map[string]any)["spawn"] != nil {
		t.Fatalf("spawn options not cleared: %v", flags)
	}
	rest(t, e, 204, "DELETE", profilesPath+"/claude-code-worker", nil)
	events := rest(t, e, 200, "GET", "/api/v1/audit?tool=delete_agent_profile", nil)["events"].([]any)
	if len(events) != 2 || events[1].(map[string]any)["note"] != "token=root; profile claude-code-worker" {
		t.Fatalf("audit = %v", events)
	}
}
