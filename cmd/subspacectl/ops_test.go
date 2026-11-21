package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/doctor"
	"github.com/SigmaUno/subspace/internal/testdb"
)

func TestSchedulesLifecycle(t *testing.T) {
	h := newHarness(t)
	spec := `{"name":"weekly audit","cron":"0 9 * * 1","timezone":"UTC","action":"create_brief",
		"brief":{"template_id":"security-audit","vars":{"repo":"o/r","title":"Audit","scope":"auth"}}}`
	created, err := h.exec(spec, "schedules", "create", "--file", "-")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var sc map[string]any
	if err := json.Unmarshal([]byte(created), &sc); err != nil || sc["id"] != "SC-1" || sc["enabled"] != true {
		t.Fatalf("create = %s (%v)", created, err)
	}
	if got := list(t, h.object("schedules", "list"), "schedules"); len(got) != 1 {
		t.Fatalf("list = %v", got)
	}
	if off := h.object("schedules", "disable", "SC-1"); off["enabled"] != false || off["next_run_at"] != nil {
		t.Fatalf("disable = %v", off)
	}
	if on := h.object("schedules", "enable", "SC-1"); on["enabled"] != true || on["next_run_at"] == nil {
		t.Fatalf("enable = %v", on)
	}
	renamed, err := h.exec(`{"name":"renamed"}`, "schedules", "edit", "SC-1", "--file", "-")
	if err != nil || !strings.Contains(renamed, `"renamed"`) {
		t.Fatalf("edit = %s (%v)", renamed, err)
	}
	if runs := list(t, h.object("schedules", "runs", "SC-1", "--limit", "5"), "runs"); len(runs) != 0 {
		t.Fatalf("runs = %v", runs)
	}
	preview := h.object("schedules", "preview", "--cron", "*/15 * * * *", "--count", "3")
	if next := list(t, preview, "next"); len(next) != 3 {
		t.Fatalf("preview = %v", preview)
	}
	h.fails(exitUsage, "schedules", "preview", "--cron", "not cron")
	h.fails(exitUsage, "schedules", "preview")
	h.fails(exitUsage, "schedules", "create")
	if _, err := h.exec(`[]`, "schedules", "create", "--file", "-"); exitCode(err) != exitUsage {
		t.Fatalf("array body: %v", err)
	}
	h.text("schedules", "delete", "SC-1")
	h.fails(exitNotFound, "schedules", "show", "SC-1")
}

func TestIncidentStartAndEnd(t *testing.T) {
	h := newHarness(t)
	h.env.Register("claude-1", "worker", "claude-code")
	started := h.object("incident", "start", "--reason", "leaked token", "--actions", "pause_all,revoke_agents", "--revoke-role", "worker")
	if started["id"] != "INC-1" || started["succeeded"] != float64(2) || started["failed"] != float64(0) {
		t.Fatalf("start = %v", started)
	}
	if h.object("state")["paused"] != true {
		t.Fatal("system not paused")
	}
	if got := list(t, h.object("incident", "list"), "incidents"); len(got) != 1 {
		t.Fatalf("list = %v", got)
	}
	ended := h.object("incident", "end", "INC-1", "--note", "rotated", "--resume-agents")
	if ended["resumed_all"] != true || ended["ended_at"] == nil {
		t.Fatalf("end = %v", ended)
	}
	if h.object("state")["paused"] != false {
		t.Fatal("system still paused")
	}
	h.fails(exitUsage, "incident", "start", "--reason", "x")
	h.fails(exitUsage, "incident", "start", "--reason", "x", "--actions", "explode")
	h.fails(exitNotFound, "incident", "end", "INC-9")
}

func TestDrillsRunAndList(t *testing.T) {
	h := newHarness(t)
	if testdb.Postgres() {
		if ran := h.object("drills", "run"); ran["status"] != "unsupported" {
			t.Fatalf("run = %v", ran)
		}
		return
	}
	h.fails(exitFailure, "drills", "run")
	h.object("backups", "create")
	ran := h.object("drills", "run")
	if ran["status"] != "ok" || ran["id"] != "D-2" {
		t.Fatalf("run = %v", ran)
	}
	got := list(t, h.object("drills", "list", "--limit", "5"), "drills")
	if len(got) != 2 || got[0].(map[string]any)["id"] != "D-2" || got[1].(map[string]any)["status"] != "failed" {
		t.Fatalf("list = %v", got)
	}
}

func TestKnowledgeModeration(t *testing.T) {
	h := newHarness(t)
	lead := h.env.Register("rex", "leader", "other")
	brief := h.brief("Knowledge")
	h.plan(lead, brief, "One")

	entry := h.object("knowledge", "add", "--brief", brief, "--kind", "decision", "--title", "Use sqlite", "--body", "single writer", "--tags", "store,db")
	if entry["id"] != "K-1" || entry["status"] != "active" || entry["author"] != "owner" {
		t.Fatalf("add = %v", entry)
	}
	h.env.Must(lead, "add_knowledge", map[string]any{"brief_id": brief, "kind": "pitfall", "title": "WAL files", "body": "copy all three files"})
	pending := list(t, h.object("knowledge", "list", "--brief", brief, "--status", "pending"), "entries")
	if len(pending) != 1 {
		t.Fatalf("pending = %v", pending)
	}
	id := pending[0].(map[string]any)["id"].(string)
	if approved := h.object("knowledge", "approve", id); approved["status"] != "active" {
		t.Fatalf("approve = %v", approved)
	}
	h.fails(exitConflict, "knowledge", "approve", id)
	if pinned := h.object("knowledge", "pin", id); pinned["pinned"] != true {
		t.Fatalf("pin = %v", pinned)
	}
	if unpinned := h.object("knowledge", "pin", id, "--off"); unpinned["pinned"] != false {
		t.Fatalf("unpin = %v", unpinned)
	}
	if edited := h.object("knowledge", "edit", id, "--title", "WAL sidecars"); edited["title"] != "WAL sidecars" {
		t.Fatalf("edit = %v", edited)
	}
	if replaced := h.object("knowledge", "supersede", "K-1", "--by", id); replaced["status"] != "superseded" {
		t.Fatalf("supersede = %v", replaced)
	}
	if hits := list(t, h.object("knowledge", "list", "--q", "sidecars"), "entries"); len(hits) != 1 {
		t.Fatalf("search = %v", hits)
	}
	h.text("knowledge", "delete", id)
	h.fails(exitNotFound, "knowledge", "show", id)
	h.fails(exitUsage, "knowledge", "edit", "K-1")
	h.fails(exitUsage, "knowledge", "add", "--brief", brief, "--kind", "decision", "--title", "x")
}

func TestAnnouncementsQuotasAndLimits(t *testing.T) {
	h := newHarness(t)
	worker := h.env.Register("claude-1", "worker", "claude-code")

	published := h.object("announcements", "publish", "--text", "freeze at noon", "--expires-at", time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	if published["delivered"] != float64(1) {
		t.Fatalf("publish = %v", published)
	}
	id := published["announcement"].(map[string]any)["id"].(string)
	if got := list(t, h.object("announcements", "list"), "announcements"); len(got) != 1 {
		t.Fatalf("list = %v", got)
	}
	instructions := h.env.Must(worker, "get_instructions", map[string]any{})["instructions"].(string)
	if !strings.Contains(instructions, "freeze at noon") {
		t.Fatalf("announcement not delivered: %s", instructions)
	}
	h.text("announcements", "delete", id)
	h.fails(exitNotFound, "announcements", "delete", id)
	h.fails(exitUsage, "announcements", "publish", "--text", "x", "--expires-at", "tomorrow")

	set := h.object("quotas", "set", "claude-1", "--max-cost-eur-per-day", "5", "--max-tasks-per-day", "10", "--max-concurrent-tasks", "2")
	if set["quota"].(map[string]any)["max_tasks_per_day"] != float64(10) {
		t.Fatalf("quota set = %v", set)
	}
	if shown := h.object("quotas", "show", "claude-1"); shown["quota"].(map[string]any)["max_concurrent_tasks"] != float64(2) {
		t.Fatalf("quota show = %v", shown)
	}
	h.env.Register("claude-2", "worker", "claude-code")
	bulk := h.object("quotas", "bulk", "claude-1", "claude-2", "claude-9", "--max-cost-eur-per-day", "1", "--max-tasks-per-day", "0", "--max-concurrent-tasks", "1")
	if bulk["succeeded"] != float64(2) || bulk["failed"] != float64(1) {
		t.Fatalf("quota bulk = %v", bulk)
	}
	h.fails(exitUsage, "quotas", "set", "claude-1", "--max-cost-eur-per-day", "5")
	h.fails(exitNotFound, "quotas", "set", "ghost", "--max-cost-eur-per-day", "5", "--max-tasks-per-day", "1", "--max-concurrent-tasks", "1")

	limits := h.object("spend-limits", "set", "--daily-eur", "10", "--weekly-eur", "50")
	if limits["daily_cost_eur"] != float64(10) || h.object("spend-limits", "get")["weekly_cost_eur"] != float64(50) {
		t.Fatalf("limits = %v", limits)
	}
	h.fails(exitUsage, "spend-limits", "set", "--daily-eur", "10")
	h.fails(exitUsage, "spend-limits", "set", "--daily-eur", "-1", "--weekly-eur", "0")
}

func TestWebhooksOutAndTokens(t *testing.T) {
	h := newHarness(t)
	hook := h.object("webhooks-out", "create", "--target-url", "https://hooks.example.com/subspace", "--events", "task.updated,decision.*")
	id := hook["webhook"].(map[string]any)["id"].(string)
	if !strings.HasPrefix(hook["secret"].(string), "whsec_") {
		t.Fatalf("create = %v", hook)
	}
	if got := list(t, h.object("webhooks-out", "list"), "webhooks"); len(got) != 1 {
		t.Fatalf("list = %v", got)
	}
	if h.object("webhooks-out", "show", id)["id"] != id {
		t.Fatal("show mismatch")
	}
	if got := list(t, h.object("webhooks-out", "deliveries", id, "--state", "dead"), "deliveries"); len(got) != 0 {
		t.Fatalf("deliveries = %v", got)
	}
	h.fails(exitNotFound, "webhooks-out", "redeliver", id, "D-404")
	h.text("webhooks-out", "delete", id)
	h.fails(exitNotFound, "webhooks-out", "show", id)
	h.fails(exitUsage, "webhooks-out", "create", "--target-url", "https://hooks.example.com/x")

	created := h.object("tokens", "create", "--name", "ci dashboard", "--scope", "viewer")
	secret := created["secret"].(string)
	if !strings.HasPrefix(secret, "ssu_") {
		t.Fatalf("create = %v", created)
	}
	tokenID := created["token"].(map[string]any)["id"].(string)
	if got := list(t, h.object("tokens", "list"), "tokens"); len(got) != 1 {
		t.Fatalf("list = %v", got)
	}
	if revoked := h.object("tokens", "revoke", tokenID); revoked["revoked_at"] == nil {
		t.Fatalf("revoke = %v", revoked)
	}
	h.fails(exitConflict, "tokens", "revoke", tokenID)
	h.fails(exitUsage, "tokens", "create", "--name", "x")
	h.fails(exitUsage, "tokens", "create", "--name", "x", "--scope", "root")
}

func TestReplayCostsEstimatesAndOperations(t *testing.T) {
	h := newHarness(t)
	lead := h.env.Register("rex", "leader", "other")
	h.env.Register("claude-1", "worker", "claude-code")
	brief := h.brief("Replay")
	plan := h.plan(lead, brief, "One", "Two")
	h.text("plans", "approve", plan)

	if snap := h.object("replay", "show", brief); snap["brief_id"] != brief || snap["total"] != float64(2) {
		t.Fatalf("snapshot = %v", snap)
	}
	if h.object("replay", "show", brief, "--at", "1")["audit_id"] == nil {
		t.Fatal("snapshot at 1")
	}
	if tl := h.object("replay", "timeline", brief); tl["brief_id"] != brief {
		t.Fatalf("timeline = %v", tl)
	}
	if h.object("replay", "diff", brief, "--from", "1")["brief_id"] != brief {
		t.Fatal("diff")
	}
	h.fails(exitUsage, "replay", "show", brief, "--at", "yesterday")
	h.fails(exitNotFound, "replay", "timeline", "B-404")

	if costs := h.object("costs", "--group-by", "day", "--brief", brief); costs["group_by"] != "day" {
		t.Fatalf("costs = %v", costs)
	}
	h.fails(exitUsage, "costs", "--group-by", "color")
	if est := h.object("estimates", "brief", brief); est["id"] != brief {
		t.Fatalf("estimates = %v", est)
	}
	h.fails(exitNotFound, "estimates", "plan", "P-404")
	if h.object("insights")["findings"] == nil || h.object("insights", "claude-1")["findings"] == nil {
		t.Fatal("insights")
	}
	h.fails(exitNotFound, "insights", "ghost")
	h.fails(exitUsage, "insights", "claude-1", "--severity", "high")
	if h.object("alerts") == nil || h.object("breakers", "list") == nil {
		t.Fatal("alerts or breakers empty")
	}
	h.fails(exitNotFound, "breakers", "reset", "nobody")
	if perf := h.object("agents", "performance", "claude-1"); perf == nil {
		t.Fatal("performance")
	}
	if shown := h.object("agents", "show", "claude-1"); shown["id"] != "claude-1" {
		t.Fatalf("agents show = %v", shown)
	}
	rotated := h.object("agents", "rotate-token", "claude-1")
	if !strings.Contains(rotated["connect"].(map[string]any)["token"].(string), "ssp_") {
		t.Fatalf("rotate = %v", rotated)
	}
	if status := h.object("retention", "status"); status == nil {
		t.Fatal("retention status")
	}
	h.fails(exitNotFound, "retention", "restore", "999")
}

func TestOpenAPIDump(t *testing.T) {
	h := newHarness(t)
	file := filepath.Join(t.TempDir(), "openapi.json")
	out := h.object("openapi", "--out", file)
	if out["path"] != file {
		t.Fatalf("out = %v", out)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil || doc["openapi"] != "3.1.0" {
		t.Fatalf("document: %v %v", err, doc["openapi"])
	}
	if got := h.object("--json", "openapi"); got["openapi"] != "3.1.0" {
		t.Fatalf("stdout document = %v", got["openapi"])
	}
}

func TestReloadReportsApplied(t *testing.T) {
	h := newHarness(t)
	out := h.object("reload")
	if len(list(t, out, "applied")) == 0 || out["ignored"] == nil {
		t.Fatalf("reload = %v", out)
	}
}

func TestDoctorRunsLocally(t *testing.T) {
	vars := map[string]string{"SUBSPACE_ADDR": "127.0.0.1:8080"}
	prev := doctorEnv
	doctorEnv = func() doctor.Env {
		e := doctor.SystemEnv()
		e.Getenv = func(k string) string { return vars[k] }
		e.HTTP = &http.Client{Transport: refuse{}}
		return e
	}
	t.Cleanup(func() { doctorEnv = prev })

	var out bytes.Buffer
	err := run(t.Context(), []string{"--json", "doctor"}, func(string) string {
		t.Error("doctor must not read credentials")
		return ""
	}, strings.NewReader(""), &out)
	var report doctor.Report
	if jerr := json.Unmarshal(out.Bytes(), &report); jerr != nil || len(report.Results) == 0 {
		t.Fatalf("report: %v\n%s", jerr, &out)
	}
	if report.Errors == 0 || exitCode(err) != exitFailure {
		t.Fatalf("missing owner token must fail: err=%v errors=%d", err, report.Errors)
	}
	if err := run(t.Context(), []string{"doctor", "extra"}, func(string) string { return "" }, strings.NewReader(""), &out); exitCode(err) != exitUsage {
		t.Fatalf("extra argument: %v", err)
	}
}

type refuse struct{}

func (refuse) RoundTrip(*http.Request) (*http.Response, error) { return nil, errors.New("offline") }

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOutputTailBacklogAndFollow(t *testing.T) {
	h := newHarness(t)
	worker := h.env.Register("claude-1", "worker", "claude-code")
	other := h.env.Register("claude-2", "worker", "codex")
	log := func(tok, text string) {
		h.env.Must(tok, "log_output", map[string]any{"stream": "stdout", "text": text})
	}
	log(worker, "compiling")
	log(worker, "tests green")
	log(other, "idle")

	if got := h.text("output", "tail", "claude-1", "--lines", "1"); got != "tests green\n" {
		t.Fatalf("backlog = %q", got)
	}
	lines := strings.Split(strings.TrimSpace(h.text("--json", "output", "tail", "claude-1")), "\n")
	var first map[string]any
	if len(lines) != 2 || json.Unmarshal([]byte(lines[0]), &first) != nil || first["text"] != "compiling" || first["seq"] != float64(1) {
		t.Fatalf("json backlog = %q", lines)
	}
	if latest := h.object("output", "latest"); len(list(t, latest, "agents")) != 2 {
		t.Fatalf("latest = %v", latest)
	}
	h.fails(exitNotFound, "output", "tail", "ghost")
	h.fails(exitUsage, "output", "tail")
	h.fails(exitUsage, "output", "tail", "--all")
	h.fails(exitUsage, "output", "tail", "claude-1", "--all", "--follow")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var out syncBuffer
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"output", "tail", "--follow", "claude-1", "claude-2", "--lines", "5"}, h.getenv, strings.NewReader(""), &out)
	}()
	waitFor(t, "backlog", func() bool { return strings.Contains(out.String(), "claude-2: idle") })
	log(worker, "deploying")
	log(other, "woke up")
	waitFor(t, "live lines", func() bool {
		return strings.Contains(out.String(), "claude-1: deploying") && strings.Contains(out.String(), "claude-2: woke up")
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("follow: %v", err)
	}
	for _, want := range []string{"claude-1: compiling", "claude-1: tests green", "claude-1: deploying"} {
		if strings.Count(out.String(), want) != 1 {
			t.Fatalf("%q must appear exactly once in\n%s", want, out.String())
		}
	}
	if strings.Index(out.String(), "claude-1: tests green") > strings.Index(out.String(), "claude-1: deploying") {
		t.Fatalf("lines out of order:\n%s", out.String())
	}
}

func TestOutputTailFollowAllFiltersByTask(t *testing.T) {
	h := newHarness(t)
	lead := h.env.Register("rex", "leader", "other")
	worker := h.env.Register("claude-1", "worker", "claude-code")
	brief := h.brief("Tail")
	h.text("plans", "approve", h.plan(lead, brief, "One"))
	h.env.Must(lead, "assign", map[string]any{"task_id": "T-1", "agent_id": "claude-1"})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var out syncBuffer
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--json", "output", "tail", "--all", "--follow", "--task", "T-1"}, h.getenv, strings.NewReader(""), &out)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(out.String(), "wanted") && time.Now().Before(deadline) {
		h.env.Must(worker, "log_output", map[string]any{"stream": "stderr", "text": "noise"})
		h.env.Must(worker, "log_output", map[string]any{"stream": "stdout", "text": "wanted", "task_id": "T-1"})
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("follow: %v", err)
	}
	if strings.Contains(out.String(), "noise") || !strings.Contains(out.String(), `"text":"wanted"`) {
		t.Fatalf("output = %s", out.String())
	}
}

func TestOutputTailFollowFailsFastWhenUnreachable(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	err := run(t.Context(), []string{"output", "tail", "--follow", "a1"}, mapEnv(map[string]string{"SUBSPACE_URL": url, "SUBSPACE_OWNER_TOKEN": "token"}), strings.NewReader(""), &bytes.Buffer{})
	if exitCode(err) != exitUnavailable {
		t.Fatalf("err = %v (exit %d)", err, exitCode(err))
	}
}
