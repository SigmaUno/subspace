package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/doctor"
	"github.com/SigmaUno/subspace/internal/servertest"
)

type rig struct {
	t   *testing.T
	env *servertest.Env
	dir string
}

func newRig(t *testing.T) *rig {
	t.Setenv("SUBSPACE_AUTOPILOT", "on")
	t.Setenv("SUBSPACE_AUTOPILOT_INTERVAL", "1h")
	dir := t.TempDir()
	for _, name := range []string{"bootstrap.json", "charter.md.tmpl", "brief.md"} {
		raw, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &rig{t: t, env: servertest.New(t), dir: dir}
}

func (r *rig) getenv(k string) string {
	return map[string]string{"SUBSPACE_URL": r.env.TS.URL, "SUBSPACE_OWNER_TOKEN": servertest.OwnerToken}[k]
}

func (r *rig) bootstrap(want int, args ...string) string {
	r.t.Helper()
	var out, errb bytes.Buffer
	args = append([]string{"-config", filepath.Join(r.dir, "bootstrap.json")}, args...)
	if code := run(r.t.Context(), args, r.getenv, &out, &errb); code != want {
		r.t.Fatalf("bootstrap %v: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", args, code, want, out.String(), errb.String())
	}
	return out.String() + errb.String()
}

func (r *rig) rest(method, path string, body any) map[string]any {
	r.t.Helper()
	code, out := r.env.REST(method, path, body)
	if code >= 300 {
		r.t.Fatalf("%s %s: %d %v", method, path, code, out)
	}
	return out
}

func (r *rig) tokens() map[string]string {
	r.t.Helper()
	f, err := os.Open(filepath.Join(r.dir, "agents.secrets"))
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	toks, id := map[string]string{}, ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if rest, ok := strings.CutPrefix(line, "## "); ok {
			id, _, _ = strings.Cut(rest, " ")
		}
		if tok, ok := strings.CutPrefix(line, "token: "); ok {
			toks[id] = tok
		}
	}
	return toks
}

func changes(out string) []string {
	var lines []string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, markCreate+" ") || strings.HasPrefix(line, markUpdate+" ") || strings.HasPrefix(line, markProblem+" ") {
			lines = append(lines, line)
		}
	}
	return lines
}

func TestBootstrapThenFirstBriefCompletes(t *testing.T) {
	r := newRig(t)

	dry := r.bootstrap(exitOK, "-dry-run")
	for _, want := range []string{"would register leader lead", "would register worker cc-002 (anthropic)", "would register worker oc-001 (deepseek)",
		"would register reviewer review (openai)", "would register repo SigmaUno/demo", "would create brief \"Dogfood: greeting endpoint\"",
		"would set quality gates on the new brief", "would enable autopilot for the new brief"} {
		if !strings.Contains(dry, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, dry)
		}
	}
	if agents := r.rest("GET", "/api/v1/agents", nil)["agents"].([]any); len(agents) != 0 {
		t.Fatalf("dry run registered %d agents", len(agents))
	}
	if _, err := os.Stat(filepath.Join(r.dir, "agents.secrets")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run touched the secrets file: %v", err)
	}

	first := r.bootstrap(exitOK)
	if strings.Contains(first, "ssp_") {
		t.Fatalf("tokens on stdout without -show:\n%s", first)
	}
	st, err := os.Stat(filepath.Join(r.dir, "agents.secrets"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("secrets file mode %v", st.Mode().Perm())
	}
	toks := r.tokens()
	for _, id := range []string{"lead", "cc-001", "cc-002", "oc-001", "test", "review"} {
		if !strings.HasPrefix(toks[id], "ssp_") {
			t.Fatalf("no token for %s in secrets file: %v", id, toks)
		}
	}
	if !strings.Contains(first, "no plan proposed") {
		t.Fatalf("first run should wait for a plan:\n%s", first)
	}

	charter := r.rest("GET", "/api/v1/charter", nil)["text"].(string)
	if !strings.Contains(charter, "Owner: Sigma.") || !strings.Contains(charter, "Workers: cc-001, cc-002, oc-001.") {
		t.Fatalf("charter not rendered: %q", charter)
	}
	agent := r.rest("GET", "/api/v1/agents/review", nil)
	if agent["role"] != "reviewer" || agent["provider"] != "openai" {
		t.Fatalf("reviewer = %v", agent)
	}
	if lim := r.rest("GET", "/api/v1/limits", nil); lim["daily_cost_eur"] != 25.0 || lim["weekly_cost_eur"] != 100.0 {
		t.Fatalf("limits = %v", lim)
	}
	if capv := r.rest("GET", "/api/v1/capacity", nil); capv["global_max_agents"] != 20.0 {
		t.Fatalf("capacity = %v", capv)
	}
	if repo := r.rest("GET", "/api/v1/repos/SigmaUno/demo", nil); repo["token_env"] != "GITHUB_TOKEN" {
		t.Fatalf("repo = %v", repo)
	}
	briefs := r.rest("GET", "/api/v1/briefs", nil)["briefs"].([]any)
	if len(briefs) != 1 {
		t.Fatalf("briefs = %v", briefs)
	}
	brief := briefs[0].(map[string]any)
	briefID := brief["id"].(string)
	if g := brief["quality_gates"].(map[string]any); g["tests"] != true || g["min_tests_passed"] != 1.0 || g["review"] != true {
		t.Fatalf("gates = %v", g)
	}
	ap := r.rest("GET", "/api/v1/autopilot", nil)["briefs"].([]any)
	if len(ap) != 1 || ap[0].(map[string]any)["enabled"] != true {
		t.Fatalf("autopilot = %v", ap)
	}

	size := st.Size()
	again := r.bootstrap(exitOK)
	if c := changes(again); len(c) != 0 {
		t.Fatalf("second run changed things: %v", c)
	}
	if st, _ := os.Stat(filepath.Join(r.dir, "agents.secrets")); st.Size() != size {
		t.Fatalf("second run rewrote the secrets file")
	}

	lead, worker, tester, reviewer := toks["lead"], toks["cc-001"], toks["test"], toks["review"]
	plan := r.env.Must(lead, "propose_plan", map[string]any{"brief_id": briefID, "tasks": []any{
		map[string]any{"ref": "w", "title": "Add greeting endpoint", "spec": "GET /greet returns hello", "repo": "SigmaUno/demo",
			"acceptance_criteria": []string{"GET /greet returns 200 with hello"}, "budget": map[string]any{"max_minutes": 60, "max_cost_eur": 3}},
		map[string]any{"ref": "t", "title": "Test greeting endpoint", "spec": "integration test for GET /greet", "repo": "SigmaUno/demo",
			"role": "tester", "acceptance_criteria": []string{"test covers GET /greet"}, "budget": map[string]any{"max_minutes": 30, "max_cost_eur": 1}},
	}})["plan"].(map[string]any)
	planID := plan["id"].(string)

	preview := r.bootstrap(exitOK, "-approve-plan", "-dry-run")
	if !strings.Contains(preview, "plan "+planID+" by lead: 2 tasks") || !strings.Contains(preview, "would approve plan "+planID) ||
		!strings.Contains(preview, "estimates: not enough history") {
		t.Fatalf("plan preview:\n%s", preview)
	}
	if p := r.rest("GET", "/api/v1/plans/"+planID, nil); p["status"] != "proposed" {
		t.Fatalf("dry run approved the plan: %v", p["status"])
	}
	r.bootstrap(exitOK, "-approve-plan")
	if p := r.rest("GET", "/api/v1/plans/"+planID, nil); p["status"] != "approved" {
		t.Fatalf("plan status %v", p["status"])
	}

	tasks := plan["tasks"].([]any)
	work, test := tasks[0].(map[string]any)["id"].(string), tasks[1].(map[string]any)["id"].(string)
	pr := "https://github.com/SigmaUno/demo/pull/1"
	r.env.Must(lead, "assign", map[string]any{"task_id": work, "agent_id": "cc-001"})
	r.env.Must(lead, "assign", map[string]any{"task_id": test, "agent_id": "test"})
	r.env.Must(worker, "claim_task", map[string]any{"task_id": work})
	r.env.Must(worker, "submit", map[string]any{"task_id": work, "pr_url": pr, "summary": "greeting endpoint"})
	r.env.Must(tester, "claim_task", map[string]any{"task_id": test})
	r.env.Must(tester, "report_tests", map[string]any{"task_id": test, "passed": 3, "failed": 0})
	r.env.Must(tester, "submit", map[string]any{"task_id": test, "pr_url": pr, "summary": "tests for greeting"})
	for _, id := range []string{work, test} {
		r.env.Must(lead, "assign", map[string]any{"task_id": id, "agent_id": "review", "as": "reviewer"})
		r.env.Must(reviewer, "review", map[string]any{"task_id": id, "verdict": "approve", "comments": "criterion 1: pass"})
		r.rest("POST", "/api/v1/tasks/"+id+"/done", map[string]any{"note": "merged"})
	}
	board := r.rest("GET", "/api/v1/briefs/"+briefID+"/board", nil)["columns"].(map[string]any)
	if done := board["done"].([]any); len(done) != 2 {
		t.Fatalf("done column = %v", done)
	}
	if c := changes(r.bootstrap(exitOK, "-approve-plan")); len(c) != 0 {
		t.Fatalf("run after completion changed things: %v", c)
	}
}

func TestBootstrapRejectsReviewerSharingWorkerProvider(t *testing.T) {
	r := newRig(t)
	path := filepath.Join(r.dir, "bootstrap.json")
	raw, _ := os.ReadFile(path)
	raw = bytes.Replace(raw, []byte(`"reviewer": {"id": "review", "profile": "codex-worker"}`), []byte(`"reviewer": {"id": "review", "kind": "claude-code"}`), 1)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	out := r.bootstrap(exitFailure)
	if !strings.Contains(out, "cross review needs a different provider") {
		t.Fatalf("output:\n%s", out)
	}
	if agents := r.rest("GET", "/api/v1/agents", nil)["agents"].([]any); len(agents) != 0 {
		t.Fatalf("registered %d agents despite the invalid config", len(agents))
	}
}

func stubDoctor() doctor.Env {
	return doctor.Env{
		Now: time.Now,
		HTTP: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("offline")
		})},
		LookPath:   func(string) (string, error) { return "", errors.New("not found") },
		Output:     func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("not run") },
		LookupHost: func(context.Context, string) ([]string, error) { return nil, errors.New("no such host") },
		DiskFree:   func(string) (uint64, error) { return 100 << 30, nil },
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPreflightReportsGoNoGo(t *testing.T) {
	r := newRig(t)
	prev := doctorEnv
	doctorEnv = stubDoctor
	t.Cleanup(func() { doctorEnv = prev })
	preflight := func() (int, verdict) {
		var out bytes.Buffer
		code := run(t.Context(), []string{"preflight", "-json"}, r.getenv, &out, io.Discard)
		var v verdict
		if err := json.Unmarshal(out.Bytes(), &v); err != nil {
			t.Fatalf("%v: %s", err, out.String())
		}
		return code, v
	}
	status := func(v verdict) map[string]string {
		m := map[string]string{}
		for _, c := range v.Checks {
			m[c.Name] = c.Status
		}
		return m
	}

	code, v := preflight()
	got := status(v)
	if code != exitFailure || v.Verdict != statusNoGo {
		t.Fatalf("exit %d verdict %s: %+v", code, v.Verdict, v.Checks)
	}
	for name, want := range map[string]string{
		"server": statusGo, "owner_auth": statusGo, "database": statusGo, "owner_token": statusGo,
		"transport": statusWarn, "public_url": statusNoGo, "backups": statusNoGo, "notifications": statusWarn,
	} {
		if got[name] != want {
			t.Errorf("%s = %q, want %q (%+v)", name, got[name], want, v.Checks)
		}
	}

	r.rest("POST", "/api/v1/backup", map[string]any{})
	if _, v = preflight(); status(v)["backups"] != statusGo {
		t.Fatalf("backups after a fresh backup: %+v", v.Checks)
	}

	var out bytes.Buffer
	weak := func(k string) string {
		if k == "SUBSPACE_OWNER_TOKEN" {
			return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}
		return r.getenv(k)
	}
	if code := run(t.Context(), []string{"preflight", "-skip-notify"}, weak, &out, io.Discard); code != exitFailure {
		t.Fatalf("weak token: exit %d", code)
	}
	if !strings.Contains(out.String(), "NO-GO  owner_token") || !strings.Contains(out.String(), "NO-GO  owner_auth") {
		t.Fatalf("weak token output:\n%s", out.String())
	}
}

func TestPreflightNeverSendsTheOwnerTokenOverPlainHTTP(t *testing.T) {
	var host string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
			host = n.IP.String()
			break
		}
	}
	if host == "" {
		t.Skip("no non-loopback address")
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Skip(err)
	}
	var seen atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			seen.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok","version":"x","paused":false,"scope":"owner"}`)
	}))
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	prev := doctorEnv
	doctorEnv = stubDoctor
	t.Cleanup(func() { doctorEnv = prev })
	getenv := func(k string) string {
		return map[string]string{"SUBSPACE_URL": srv.URL, "SUBSPACE_OWNER_TOKEN": servertest.OwnerToken}[k]
	}
	var out bytes.Buffer
	if code := run(t.Context(), []string{"preflight", "-skip-notify"}, getenv, &out, io.Discard); code != exitFailure {
		t.Fatalf("plain HTTP to %s: exit %d\n%s", host, code, out.String())
	}
	if n := seen.Load(); n != 0 {
		t.Fatalf("preflight sent the owner token over plain HTTP %d times although it reports NO-GO:\n%s", n, out.String())
	}
	if !strings.Contains(out.String(), "NO-GO  transport") {
		t.Fatalf("output:\n%s", out.String())
	}
}
