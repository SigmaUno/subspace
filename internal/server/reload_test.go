package server_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

type reloadFixture struct {
	*servertest.Env
	path  string
	level *slog.LevelVar
}

func newReloadFixture(t *testing.T, initial string) *reloadFixture {
	t.Helper()
	dir := t.TempDir()
	f := &reloadFixture{path: filepath.Join(dir, "config.json"), level: new(slog.LevelVar)}
	f.write(initial)
	if err := os.WriteFile(filepath.Join(dir, "capacity.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.Env = servertest.Start(t, server.Config{
		PublicURL: "http://sub.test", FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond,
		Lease: time.Minute, ConfigFile: f.path, LogLevel: f.level, CapacityFile: filepath.Join(dir, "capacity.json"),
	})
	return f
}

func (f *reloadFixture) write(content string) {
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		panic(err)
	}
	if err := os.Rename(tmp, f.path); err != nil {
		panic(err)
	}
}

func (f *reloadFixture) reload() (int, map[string]any) {
	f.T.Helper()
	return f.REST("POST", "/api/v1/reload", map[string]any{})
}

func mcpPing(t *testing.T, e *servertest.Env, tok string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", e.TS.URL+"/mcp", bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func keysOf(v any) []string {
	var out []string
	for _, k := range v.([]any) {
		switch x := k.(type) {
		case string:
			out = append(out, x)
		case map[string]any:
			out = append(out, x["key"].(string))
		}
	}
	return out
}

func TestReloadRateLimitFromFileWithoutRestart(t *testing.T) {
	f := newReloadFixture(t, `{}`)
	tok := f.Register("w1", "worker", "opencode")
	for i := 0; i < 6; i++ {
		if code := mcpPing(t, f.Env, tok); code != 200 {
			t.Fatalf("unlimited ping %d: %d", i, code)
		}
	}

	f.write(`{"SUBSPACE_RATE_MCP":"1/m:2"}`)
	code, out := f.reload()
	if code != 200 || !slices.Contains(keysOf(out["applied"]), "SUBSPACE_RATE_MCP") {
		t.Fatalf("reload: %d %v", code, out)
	}
	got := []int{mcpPing(t, f.Env, tok), mcpPing(t, f.Env, tok), mcpPing(t, f.Env, tok)}
	if !slices.Equal(got, []int{200, 200, 429}) {
		t.Fatalf("limited pings: %v", got)
	}

	f.write(`{"SUBSPACE_RATE_MCP":"off"}`)
	if code, out := f.reload(); code != 200 {
		t.Fatalf("reload: %d %v", code, out)
	}
	if code := mcpPing(t, f.Env, tok); code != 200 {
		t.Fatalf("ping after lifting the limit: %d", code)
	}
}

func TestReloadAllowlistsApplyToTheNextRequest(t *testing.T) {
	f := newReloadFixture(t, `{}`)
	tok := f.Register("w1", "worker", "opencode")

	f.write(`{"SUBSPACE_AGENT_ALLOW_CIDRS":["10.0.0.0/8","192.168.0.0/16"],"SUBSPACE_OWNER_ALLOW_CIDRS":"127.0.0.0/8"}`)
	if code, out := f.reload(); code != 200 {
		t.Fatalf("reload: %d %v", code, out)
	}
	if code := mcpPing(t, f.Env, tok); code != 403 {
		t.Fatalf("agent outside the allowlist: %d", code)
	}
	if code, _ := f.REST("GET", "/api/v1/state", nil); code != 200 {
		t.Fatalf("owner inside the allowlist: %d", code)
	}

	f.write(`{"SUBSPACE_AGENT_ALLOW_CIDRS":"127.0.0.1"}`)
	if code, out := f.reload(); code != 200 {
		t.Fatalf("reload: %d %v", code, out)
	}
	if code := mcpPing(t, f.Env, tok); code != 200 {
		t.Fatalf("agent inside the allowlist: %d", code)
	}

	f.write(`{"SUBSPACE_OWNER_ALLOW_CIDRS":"10.0.0.0/8"}`)
	if code, _ := f.reload(); code != 200 {
		t.Fatal("reload rejected")
	}
	if code, _ := f.REST("GET", "/api/v1/state", nil); code != 403 {
		t.Fatalf("owner outside the new allowlist: %d", code)
	}
	f.write(`{}`)
	if _, err := f.Srv.Reload(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.REST("GET", "/api/v1/state", nil); code != 200 {
		t.Fatalf("owner after clearing the allowlist: %d", code)
	}
}

func TestInvalidReloadIsRejectedAsAWholeAndKeepsTheOldConfiguration(t *testing.T) {
	f := newReloadFixture(t, `{"SUBSPACE_RATE_MCP":"1/m:1"}`)
	tok := f.Register("w1", "worker", "opencode")
	f.level.Set(slog.LevelWarn)

	f.write(`{"SUBSPACE_RATE_MCP":"off","SUBSPACE_LOG_LEVEL":"debug","SUBSPACE_OWNER_ALLOW_CIDRS":"not-a-cidr","SUBSPACE_ALERTS_STALL_AFTER":"soon","SUBSPACE_NOTIFY_WEBHOOK_URL":"ftp://x.test/h","SUBSPACE_NOPE":"1"}`)
	code, out := f.reload()
	if code != 400 {
		t.Fatalf("reload: %d %v", code, out)
	}
	e := out["error"].(map[string]any)
	if e["code"] != "bad_request" || len(e["errors"].([]any)) < 5 {
		t.Fatalf("error body: %v", e)
	}
	if f.level.Level() != slog.LevelWarn {
		t.Fatal("log level changed by a rejected reload")
	}
	if got := []int{mcpPing(t, f.Env, tok), mcpPing(t, f.Env, tok)}; !slices.Equal(got, []int{200, 429}) {
		t.Fatalf("rate limit after rejected reload: %v", got)
	}
	if code, out := f.REST("GET", "/api/v1/notify/status", nil); code != 200 || len(out["sinks"].([]any)) != 0 {
		t.Fatalf("notify status: %d %v", code, out)
	}

	f.write(`{`)
	if code, _ := f.reload(); code != 400 {
		t.Fatalf("malformed file: %d", code)
	}
	if err := os.Remove(f.path); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.reload(); code != 400 {
		t.Fatalf("missing file: %d", code)
	}
}

func TestReloadNeverChangesTokenDBOrAddress(t *testing.T) {
	f := newReloadFixture(t, `{}`)
	f.write(`{"SUBSPACE_OWNER_TOKEN":"another-owner-token-0123456789","SUBSPACE_DB":"/nowhere/x.db","SUBSPACE_ADDR":":1","SUBSPACE_LOG_LEVEL":"error"}`)
	code, out := f.reload()
	if code != 200 {
		t.Fatalf("reload: %d %v", code, out)
	}
	if got := keysOf(out["ignored"]); !slices.Equal(got, []string{"SUBSPACE_ADDR", "SUBSPACE_DB", "SUBSPACE_OWNER_TOKEN"}) {
		t.Fatalf("ignored = %v", got)
	}
	for _, r := range out["ignored"].([]any) {
		if r.(map[string]any)["reason"] != "requires restart" {
			t.Fatalf("reason: %v", r)
		}
	}
	if !slices.Contains(keysOf(out["applied"]), "SUBSPACE_LOG_LEVEL") || f.level.Level() != slog.LevelError {
		t.Fatalf("applied = %v level %v", out["applied"], f.level.Level())
	}
	if code, _ := f.REST("GET", "/api/v1/state", nil); code != 200 {
		t.Fatalf("original owner token stopped working: %d", code)
	}
	if code, _ := f.RESTAs("another-owner-token-0123456789", "GET", "/api/v1/state", nil); code != 401 {
		t.Fatalf("token from the file was accepted: %d", code)
	}
}

func TestReloadNotifySinksAndCapacity(t *testing.T) {
	f := newReloadFixture(t, `{}`)
	hook := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(hook.Close)

	if err := os.WriteFile(filepath.Join(filepath.Dir(f.path), "capacity.json"), []byte(`{"global_max_agents":7,"limits":[{"provider":"anthropic","model":"*","max_concurrency":3}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.write(fmt.Sprintf(`{"SUBSPACE_NOTIFY_WEBHOOK_URL":%q,"SUBSPACE_NOTIFY_ALLOW_PRIVATE":true}`, hook.URL))
	code, out := f.reload()
	if code != 200 {
		t.Fatalf("reload: %d %v", code, out)
	}
	if got := keysOf(out["applied"]); !slices.Contains(got, "SUBSPACE_NOTIFY_WEBHOOK_URL") || !slices.Contains(got, "SUBSPACE_CAPACITY_FILE") {
		t.Fatalf("applied = %v", got)
	}
	if _, st := f.REST("GET", "/api/v1/notify/status", nil); len(st["sinks"].([]any)) != 1 {
		t.Fatalf("notify status: %v", st)
	}
	if code, res := f.REST("POST", "/api/v1/notify/test", map[string]any{}); code != 200 || len(res["results"].([]any)) != 1 {
		t.Fatalf("notify test: %d %v", code, res)
	}
	if _, c := f.REST("GET", "/api/v1/capacity", nil); c["global_max_agents"] != float64(7) {
		t.Fatalf("capacity: %v", c)
	}

	if err := os.WriteFile(filepath.Join(filepath.Dir(f.path), "capacity.json"), []byte(`{"global_max_agents":-1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.reload(); code != 400 {
		t.Fatalf("invalid capacity file: %d", code)
	}
	if _, c := f.REST("GET", "/api/v1/capacity", nil); c["global_max_agents"] != float64(7) {
		t.Fatalf("capacity after rejected reload: %v", c)
	}

	if err := os.WriteFile(filepath.Join(filepath.Dir(f.path), "capacity.json"), []byte(`{"global_max_agents":9}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.reload(); code != 200 {
		t.Fatal("reload rejected")
	}
	if _, c := f.REST("GET", "/api/v1/capacity", nil); c["global_max_agents"] != float64(9) {
		t.Fatalf("capacity: %v", c)
	}

	if code, _ := f.REST("PUT", "/api/v1/capacity", map[string]any{"global_max_agents": 4, "limits": []any{}}); code != 200 {
		t.Fatal("PUT capacity failed")
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(f.path), "capacity.json"), []byte(`{"global_max_agents":11}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out = f.reload()
	if code != 200 || !slices.Contains(keysOf(out["ignored"]), "SUBSPACE_CAPACITY_FILE") {
		t.Fatalf("reload: %d %v", code, out)
	}
	if _, c := f.REST("GET", "/api/v1/capacity", nil); c["global_max_agents"] != float64(4) {
		t.Fatalf("database capacity was replaced by the file: %v", c)
	}
}

func TestReloadIsOwnerOnlyAndAudited(t *testing.T) {
	f := newReloadFixture(t, `{}`)
	_, tok := createToken(f.Env, "ro", "viewer", nil)
	if code, _ := f.RESTAs(tok, "POST", "/api/v1/reload", map[string]any{}); code != 403 {
		t.Fatalf("viewer: %d", code)
	}
	if code, _ := f.RESTAs("", "POST", "/api/v1/reload", map[string]any{}); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
	if code, _ := f.reload(); code != 200 {
		t.Fatal("owner reload failed")
	}
	if _, err := f.Srv.Reload(context.Background(), "sighup"); err != nil {
		t.Fatal(err)
	}
	code, out := f.REST("GET", "/api/v1/audit?tool=reload_config", nil)
	evs, _ := out["events"].([]any)
	if code != 200 || len(evs) != 2 {
		t.Fatalf("audit: %d %v", code, out)
	}
	notes := []any{evs[0].(map[string]any)["note"], evs[1].(map[string]any)["note"]}
	if evs[0].(map[string]any)["role"] != model.RoleOwner || notes[0] != "token=root" || notes[1] != "sighup" {
		t.Fatalf("audit notes: %v", notes)
	}
}

func TestConcurrentReloadUnderTraffic(t *testing.T) {
	f := newReloadFixture(t, `{}`)
	tok := f.Register("w1", "worker", "opencode")
	hook := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(hook.Close)
	configs := []string{
		`{"SUBSPACE_RATE_MCP":"off","SUBSPACE_RATE_AUTHFAIL":"off"}`,
		fmt.Sprintf(`{"SUBSPACE_RATE_MCP":"6000/m:1000","SUBSPACE_RATE_AUTHFAIL":"600/m:100","SUBSPACE_RATE_ACTIONLINK":"600/m:100","SUBSPACE_OWNER_ALLOW_CIDRS":["127.0.0.0/8","::1"],"SUBSPACE_AGENT_ALLOW_CIDRS":"127.0.0.1,::1","SUBSPACE_TRUSTED_PROXIES":"10.0.0.0/8","SUBSPACE_ALERTS_STALL_AFTER":"1m","SUBSPACE_ALERTS_PAUSED_MAX":9,"SUBSPACE_LOG_LEVEL":"debug","SUBSPACE_DIGEST_AT":"08:00","SUBSPACE_DIGEST_TZ":"Europe/Berlin","SUBSPACE_NOTIFY_WEBHOOK_URL":%q,"SUBSPACE_NOTIFY_ALLOW_PRIVATE":true}`, hook.URL),
		`{"SUBSPACE_RATE_MCP":"120/s:50","SUBSPACE_ALERTS_STALL_AFTER":"off","SUBSPACE_LOG_LEVEL":"warn","SUBSPACE_DIGEST_WEEKLY":"mon@09:00"}`,
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var served, bad atomic.Int64
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			for ctx.Err() == nil {
				code := mcpPing(t, f.Env, tok)
				if code != 200 && code != 429 {
					bad.Add(1)
				}
				if code, _ := f.REST("GET", "/api/v1/state", nil); code != 200 {
					bad.Add(1)
				}
				served.Add(1)
			}
		})
	}
	for i := 0; i < 60; i++ {
		f.write(configs[i%len(configs)])
		if _, err := f.Srv.Reload(context.Background(), "test"); err != nil {
			t.Fatalf("reload %d: %v", i, err)
		}
		if code, out := f.reload(); code != 200 {
			t.Fatalf("reload %d over HTTP: %d %v", i, code, out)
		}
	}
	cancel()
	wg.Wait()
	if bad.Load() != 0 || served.Load() == 0 {
		t.Fatalf("served %d requests, %d unexpected responses", served.Load(), bad.Load())
	}
}
