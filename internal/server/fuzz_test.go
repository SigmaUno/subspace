package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

var fuzzLeaks = regexp.MustCompile(`\.go:\d+|goroutine \d+|runtime error|runtime\.|panic|nil pointer|Go struct field|cannot unmarshal|json: |sql: |sqlite|constraint failed|github\.com/|/usr/|/home/|/root/|/var/|/tmp/|/data/|\*?(model|board|store|api|mcp|json|sql|http|reflect|strconv|time|os|fs|errors|fmt)\.[A-Z]\w*`)

type fuzzEnv struct {
	f      *testing.F
	h      http.Handler
	dir    string
	agents map[string]string
	brief  string
	tasks  []string
	plan   string
}

func newFuzzEnv(f *testing.F) *fuzzEnv {
	f.Helper()
	f.Setenv("SUBSPACE_RATE_MCP", "off")
	f.Setenv("SUBSPACE_RATE_AUTHFAIL", "off")
	dir, err := filepath.EvalSymlinks(f.TempDir())
	if err != nil {
		f.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s, err := server.New(ctx, server.Config{DBPath: filepath.Join(dir, "f.db"), OwnerToken: servertest.OwnerToken,
		PublicURL: "http://sub.test", Lease: time.Minute, FlushEvery: 50 * time.Millisecond,
		SweepEvery: 50 * time.Millisecond, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		cancel()
		f.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		s.Board.Run(ctx)
		close(done)
	}()
	f.Cleanup(func() { cancel(); <-done; _ = s.Close() })
	e := &fuzzEnv{f: f, h: s.Handler(), dir: dir, agents: map[string]string{}}
	e.seed()
	return e
}

func (e *fuzzEnv) call(method, target, tok string, body any) (int, map[string]any) {
	var rd io.Reader = http.NoBody
	if body != nil {
		rd = bytes.NewReader(mustJSON(body))
	}
	req := httptest.NewRequest(method, target, rd)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (e *fuzzEnv) mustREST(method, target string, want int, body any) map[string]any {
	e.f.Helper()
	code, out := e.call(method, target, servertest.OwnerToken, body)
	if code != want {
		e.f.Fatalf("%s %s: %d %v", method, target, code, out)
	}
	return out
}

func (e *fuzzEnv) mustTool(role, tool string, args any) map[string]any {
	e.f.Helper()
	code, out := e.call("POST", "/mcp", e.agents[role], map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args}})
	res, _ := out["result"].(map[string]any)
	if code != 200 || res == nil || res["isError"] == true {
		e.f.Fatalf("%s: %d %v", tool, code, out)
	}
	sc, _ := res["structuredContent"].(map[string]any)
	return sc
}

func (e *fuzzEnv) seed() {
	roles := [][3]string{{"leader", "leader-1", "other"}, {"worker", "worker-1", "claude-code"},
		{"tester", "tester-1", "opencode"}, {"reviewer", "reviewer-1", "codex"}}
	for _, r := range roles {
		out := e.mustREST("POST", "/api/v1/agents", 201, map[string]any{"id": r[1], "role": r[0], "kind": r[2], "model": "m"})
		e.agents[r[0]] = out["connect"].(map[string]any)["token"].(string)
	}
	br := e.mustREST("POST", "/api/v1/briefs", 201, map[string]any{"title": "Fuzz", "body": "b", "repos": []string{"o/r"},
		"max_cost_eur": 10, "max_parallel_agents": 5})
	e.brief = br["id"].(string)
	plan := e.mustTool("leader", "propose_plan", map[string]any{"brief_id": e.brief, "tasks": []any{
		map[string]any{"ref": "a", "title": "One", "spec": "s", "repo": "o/r", "acceptance_criteria": []string{"ok"}},
		map[string]any{"ref": "b", "title": "Two", "spec": "s", "repo": "o/r", "depends_on": []string{"a"}},
	}})["plan"].(map[string]any)
	e.plan = plan["id"].(string)
	for _, t := range plan["tasks"].([]any) {
		e.tasks = append(e.tasks, t.(map[string]any)["id"].(string))
	}
	e.mustREST("POST", "/api/v1/plans/"+e.plan+"/approve", 200, map[string]any{})
	e.mustTool("leader", "assign", map[string]any{"task_id": e.tasks[0], "agent_id": "worker-1"})
	e.mustTool("worker", "claim_task", map[string]any{"task_id": e.tasks[0]})
}

type leakCheck struct {
	inputs string
	dir    string
}

func (c leakCheck) find(body string) string {
	if c.dir != "" && strings.Contains(body, c.dir) {
		return c.dir
	}
	for _, m := range fuzzLeaks.FindAllString(body, -1) {
		if !strings.Contains(c.inputs, m) {
			return m
		}
	}
	return ""
}

func validHeader(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}

func pick[T any](xs []T, i uint8) T { return xs[int(i)%len(xs)] }

var restRoutes = []string{
	"/healthz",
	"/api/v1/state",
	"/api/v1/pause-all",
	"/api/v1/resume-all",
	"/api/v1/charter",
	"/api/v1/backup",
	"/api/v1/briefs",
	"/api/v1/briefs/{a}",
	"/api/v1/briefs/{a}/board",
	"/api/v1/briefs/{a}/architecture",
	"/api/v1/briefs/{a}/components",
	"/api/v1/briefs/{a}/report",
	"/api/v1/briefs/{a}/report.md",
	"/api/v1/plans",
	"/api/v1/plans/{a}",
	"/api/v1/plans/{a}/approve",
	"/api/v1/plans/{a}/reject",
	"/api/v1/tasks",
	"/api/v1/tasks/{a}",
	"/api/v1/tasks/{a}/done",
	"/api/v1/tasks/{a}/cancel",
	"/api/v1/tasks/{a}/reopen",
	"/api/v1/agents",
	"/api/v1/agents/bulk",
	"/api/v1/agents/{a}",
	"/api/v1/agents/{a}/connect",
	"/api/v1/agents/{a}/rotate-token",
	"/api/v1/agents/{a}/revoke",
	"/api/v1/agents/{a}/resume",
	"/api/v1/questions",
	"/api/v1/questions/{a}/answer",
	"/api/v1/threads/{a}/{b}/messages",
	"/api/v1/capacity",
	"/api/v1/spawns",
	"/api/v1/audit",
	"/api/v1/audit/verify",
	"/api/v1/audit/export",
	"/api/v1/decisions",
	"/api/v1/decisions/next",
	"/api/v1/decisions/{a}",
	"/api/v1/messages",
	"/api/v1/conversations",
	"/api/v1/conversations/{a}",
	"/api/v1/prompts",
	"/api/v1/prompts/{a}",
	"/api/v1/prompts/{a}/render",
	"/api/v1/hillclimb/jobs",
	"/api/v1/hillclimb/jobs/{a}/series",
	"/api/v1/artifacts",
	"/api/v1/artifacts/{a}",
	"/api/v1/bots",
	"/api/v1/bots/bulk",
	"/api/v1/bots/spawn",
	"/api/v1/bots/{a}",
	"/api/v1/bots/{a}/revoke",
	"/api/v1/bots/{a}/rotate-token",
	"/api/v1/bots/{a}/pause",
	"/api/v1/bots/{a}/resume",
	"/api/v1/bots/{a}/kill",
	"/api/v1/instructions/effective",
	"/api/v1/instructions/{a}/{b}",
	"/api/v1/instructions/{a}/{b}/versions",
	"/api/v1/instructions/{a}/{b}/versions/{c}",
	"/api/v1/instructions/{a}/{b}/diff",
	"/api/v1/instructions/{a}/{b}/rollback",
	"/api/v1/search/",
	"/api/v1/notify/status",
	"/api/v1/mergequeue/candidates",
	"/api/v1/mergequeue/candidates/{a}",
	"/api/v1/autopilot",
	"/api/v1/nope",
	"/mcp",
	"/",
}

var (
	restMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
	restTypes   = []string{"application/json", "", "text/plain", "multipart/form-data; boundary=x", "application/json; charset=utf-16", "\x00"}
)

func authHeader(env *fuzzEnv, mode uint8, garbage string) string {
	switch mode % 12 {
	case 0:
		return ""
	case 1:
		return "Bearer " + servertest.OwnerToken
	case 2:
		return "bearer " + servertest.OwnerToken
	case 3:
		return "Bearer " + garbage
	case 4:
		return garbage
	case 5:
		return "Bearer " + env.agents["leader"]
	case 6:
		return "Bearer " + env.agents["worker"]
	case 7:
		return "Bearer " + env.agents["tester"]
	case 8:
		return "Bearer " + env.agents["reviewer"]
	case 9:
		return "Bearer " + servertest.OwnerToken + " "
	case 10:
		return "Basic " + servertest.OwnerToken
	default:
		return "Bearer " + servertest.OwnerToken[:len(servertest.OwnerToken)-1]
	}
}

func isOwnerCredential(h string) bool { return h == "Bearer "+servertest.OwnerToken }

func rejected(code int) bool {
	return code == http.StatusUnauthorized || code == http.StatusForbidden
}

func redirect(code int) bool {
	return code == http.StatusMovedPermanently || code == http.StatusPermanentRedirect || code == http.StatusTemporaryRedirect
}

func fillRoute(tmpl, a, b, c string, raw bool) string {
	esc := func(s string) string {
		if raw {
			return s
		}
		return url.PathEscape(s)
	}
	r := strings.NewReplacer("{a}", esc(a), "{b}", esc(b), "{c}", esc(c))
	return r.Replace(tmpl)
}

func FuzzREST(f *testing.F) {
	env := newFuzzEnv(f)
	f.Fuzz(func(t *testing.T, method, route, mode, ctype uint8, a, b, c, query, garbage string, raw bool, body []byte) {
		if !validHeader(garbage) {
			t.Skip()
		}
		target := fillRoute(pick(restRoutes, route), a, b, c, raw)
		if query != "" {
			target += "?" + query
		}
		req, err := http.NewRequest(pick(restMethods, method), "http://sub.test"+target, bytes.NewReader(body))
		if err != nil || !strings.HasPrefix(req.URL.Path, "/") {
			t.Skip()
		}
		if clean := path.Clean(req.URL.Path); clean == "/api/v1/events" {
			t.Skip()
		}
		if ct := pick(restTypes, ctype); ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		hd := authHeader(env, mode, garbage)
		if hd != "" {
			req.Header.Set("Authorization", hd)
		}
		rec := httptest.NewRecorder()
		env.h.ServeHTTP(rec, req)

		desc := fmt.Sprintf("%s %s auth=%q", req.Method, req.URL.RequestURI(), hd)
		if rec.Code >= 500 {
			t.Fatalf("%s: status %d: %s", desc, rec.Code, rec.Body.String())
		}
		inputs := target + " " + string(body) + " " + hd + " " + garbage + " " + a + b + c + " " + string(mustJSON(string(body)))
		backedUp := rec.Code == http.StatusOK && path.Clean(req.URL.Path) == "/api/v1/backup"
		if leak := (leakCheck{inputs: inputs, dir: env.dir}).find(rec.Body.String()); leak != "" && !backedUp {
			t.Fatalf("%s: response leaks %q: %s", desc, leak, rec.Body.String())
		}
		if strings.HasPrefix(req.URL.Path, "/api/v1/") && !isOwnerCredential(hd) && !rejected(rec.Code) && !redirect(rec.Code) {
			t.Fatalf("%s: owner endpoint answered %d without owner credentials", desc, rec.Code)
		}
		if req.URL.Path == "/mcp" && isOwnerCredential(hd) && rec.Code != http.StatusForbidden && rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: /mcp answered %d to the owner token", desc, rec.Code)
		}
	})
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

var mcpMethods = []string{"POST", "POST", "POST", "DELETE", "GET", "PUT"}

func FuzzMCP(f *testing.F) {
	env := newFuzzEnv(f)
	f.Fuzz(func(t *testing.T, method, mode, kind uint8, rpcMethod, tool, id, session, garbage string, payload []byte) {
		if !validHeader(garbage) || !validHeader(session) {
			t.Skip()
		}
		var body []byte
		switch kind % 3 {
		case 0:
			body = payload
		case 1:
			body = []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"name":%s,"arguments":%s}}`,
				rawOr(id, `1`), mustJSON(tool), rawOr(string(payload), `{}`)))
		default:
			body = []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":%s,"params":%s}`,
				rawOr(id, `1`), mustJSON(rpcMethod), rawOr(string(payload), `{}`)))
		}
		req, err := http.NewRequest(pick(mcpMethods, method), "http://sub.test/mcp", bytes.NewReader(body))
		if err != nil {
			t.Skip()
		}
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		hd := authHeader(env, mode, garbage)
		if hd != "" {
			req.Header.Set("Authorization", hd)
		}
		rec := httptest.NewRecorder()
		env.h.ServeHTTP(rec, req)

		desc := fmt.Sprintf("%s /mcp auth=%.12q body=%q", req.Method, hd, body)
		if rec.Code >= 500 {
			t.Fatalf("%s: status %d: %s", desc, rec.Code, rec.Body.String())
		}
		inputs := string(body) + " " + hd + " " + garbage + " " + tool + rpcMethod + id + session + " " + string(mustJSON(string(body))) +
			string(mustJSON(tool)) + string(mustJSON(string(payload)))
		if leak := (leakCheck{inputs: inputs, dir: env.dir}).find(rec.Body.String()); leak != "" {
			t.Fatalf("%s: response leaks %q: %s", desc, leak, rec.Body.String())
		}
		switch {
		case isOwnerCredential(hd):
			if rec.Code != http.StatusForbidden && rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s: /mcp answered %d to the owner token", desc, rec.Code)
			}
		case !isAgentHeader(env, hd):
			if !rejected(rec.Code) && rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s: /mcp answered %d to unknown credentials", desc, rec.Code)
			}
		}
		if bytes.Contains(rec.Body.Bytes(), []byte("agent_paused")) {
			env.resume()
		}
	})
}

func rawOr(s, def string) string {
	if json.Valid([]byte(s)) {
		return s
	}
	if s == "" {
		return def
	}
	return string(mustJSON(s))
}

func isAgentHeader(env *fuzzEnv, hd string) bool {
	for _, tok := range env.agents {
		if hd == "Bearer "+tok {
			return true
		}
	}
	return false
}

func (e *fuzzEnv) resume() {
	for _, id := range []string{"leader-1", "worker-1", "tester-1", "reviewer-1"} {
		e.call("POST", "/api/v1/agents/"+id+"/resume", servertest.OwnerToken, map[string]any{})
	}
}

func TestBackupTwiceInOneSecond(t *testing.T) {
	e := servertest.New(t)
	seen := map[string]bool{}
	for range 3 {
		code, out := e.REST("POST", "/api/v1/backup", map[string]any{})
		if code != http.StatusOK {
			t.Fatalf("backup: %d %v", code, out)
		}
		p, _ := out["path"].(string)
		if seen[p] {
			t.Fatalf("backup path reused: %s", p)
		}
		seen[p] = true
	}
}
