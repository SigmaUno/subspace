package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/servertest"
)

type harness struct {
	t   *testing.T
	env *servertest.Env
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return &harness{t: t, env: servertest.New(t)}
}

func (h *harness) getenv(key string) string {
	return map[string]string{"SUBSPACE_URL": h.env.TS.URL, "SUBSPACE_OWNER_TOKEN": servertest.OwnerToken}[key]
}

func (h *harness) exec(stdin string, args ...string) (string, error) {
	h.t.Helper()
	var out bytes.Buffer
	err := run(h.t.Context(), args, h.getenv, strings.NewReader(stdin), &out)
	return out.String(), err
}

func (h *harness) text(args ...string) string {
	h.t.Helper()
	out, err := h.exec("", args...)
	if err != nil {
		h.t.Fatalf("%v: %v", args, err)
	}
	return out
}

func (h *harness) object(args ...string) map[string]any {
	h.t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(h.text(args...)), &v); err != nil {
		h.t.Fatalf("%v: %v", args, err)
	}
	return v
}

func (h *harness) fails(code int, args ...string) {
	h.t.Helper()
	_, err := h.exec("", args...)
	if err == nil || exitCode(err) != code {
		h.t.Fatalf("%v: err = %v (exit %d), want exit %d", args, err, exitCode(err), code)
	}
}

func (h *harness) brief(title string) string {
	h.t.Helper()
	return h.object("create-brief", "--title", title, "--body", "build it", "--repos", "o/r")["id"].(string)
}

func (h *harness) plan(leader, brief string, titles ...string) string {
	h.t.Helper()
	var tasks []any
	for _, title := range titles {
		tasks = append(tasks, map[string]any{"title": title, "spec": "spec for " + title, "repo": "o/r"})
	}
	plan := h.env.Must(leader, "propose_plan", map[string]any{"brief_id": brief, "tasks": tasks})["plan"].(map[string]any)
	return plan["id"].(string)
}

func list(t *testing.T, v map[string]any, key string) []any {
	t.Helper()
	items, ok := v[key].([]any)
	if !ok {
		t.Fatalf("%s is not a list in %v", key, v)
	}
	return items
}

func TestDecisionsListNextAct(t *testing.T) {
	h := newHarness(t)
	lead := h.env.Register("rex", "leader", "other")
	worker := h.env.Register("claude-1", "worker", "claude-code")
	brief := h.brief("Decisions")
	h.plan(lead, brief, "One")
	h.env.Must(worker, "ask_owner", map[string]any{"question": "which database?", "options": []string{"sqlite", "postgres"}})

	ds := list(t, h.object("decisions", "list"), "decisions")
	if len(ds) != 2 {
		t.Fatalf("decisions = %v", ds)
	}
	next := h.object("decisions", "next")["decision"].(map[string]any)
	if next["id"] != "plan:P-1" {
		t.Fatalf("next = %v", next)
	}
	out := h.object("decisions", "act", "q:Q-1", "--action", "answer", "--answer", "sqlite")
	if out["action"] != "answer" || out["next"].(map[string]any)["id"] != "plan:P-1" {
		t.Fatalf("answer = %v", out)
	}
	out = h.object("decisions", "act", "plan:P-1", "--action", "approve", "--note", "go")
	if out["next"] != nil || h.object("plans", "show", "P-1")["status"] != "approved" {
		t.Fatalf("approve = %v", out)
	}
	h.fails(exitUsage, "decisions", "act", "plan:P-1")
	h.fails(exitNotFound, "decisions", "act", "plan:P-9", "--action", "approve")
	h.fails(exitUsage, "decisions", "act", "../x", "--action", "approve")
}

func TestChat(t *testing.T) {
	h := newHarness(t)
	h.env.Register("claude-1", "worker", "claude-code")

	conv := h.object("chat", "send", "--to", "agent:claude-1", "--body", "status please")
	id := conv["id"].(string)
	if !strings.HasPrefix(id, "C-") || conv["to"] != "agent:claude-1" {
		t.Fatalf("send = %v", conv)
	}
	if got := list(t, h.object("chat", "list"), "conversations"); len(got) != 1 {
		t.Fatalf("list = %v", got)
	}
	shown := h.object("chat", "show", id)
	msgs := list(t, shown, "messages")
	if len(msgs) != 1 || msgs[0].(map[string]any)["body"] != "status please" {
		t.Fatalf("show = %v", shown)
	}
	out, err := h.exec("from stdin", "chat", "send", "--to", "leader", "--body-file", "-")
	if err == nil || exitCode(err) != exitNotFound || out != "" {
		t.Fatalf("leader send: %q %v", out, err)
	}
	h.fails(exitUsage, "chat", "send", "--to", "everyone", "--body", "x")
	h.fails(exitUsage, "chat", "send", "--to", "leader")
}

func TestSearchReportForecast(t *testing.T) {
	h := newHarness(t)
	lead := h.env.Register("rex", "leader", "other")
	brief := h.brief("Zebra migration")
	h.plan(lead, brief, "Port zebra service")
	h.text("plans", "approve", "P-1")

	results := list(t, h.object("search", "zebra", "--kind", "task"), "results")
	if len(results) == 0 || results[0].(map[string]any)["kind"] != "task" {
		t.Fatalf("search = %v", results)
	}
	h.fails(exitUsage, "search", "zebra", "--kind", "bogus")

	report := h.object("report", brief)
	if report["brief_id"] != brief || report["total"] != float64(1) {
		t.Fatalf("report = %v", report)
	}
	md := h.text("report", brief, "--format", "md")
	if json.Valid([]byte(md)) || !strings.Contains(md, "Zebra migration") {
		t.Fatalf("markdown report = %q", md)
	}
	wrapped := h.object("--json", "report", brief, "--format", "md")
	if !strings.Contains(wrapped["text"].(string), "Zebra migration") {
		t.Fatalf("json markdown = %v", wrapped)
	}
	if f := h.object("forecast", brief); f == nil {
		t.Fatal("forecast empty")
	}
	h.fails(exitNotFound, "forecast", "B-404")
	h.fails(exitUsage, "report", brief, "--format", "pdf")
}

func TestPlanEditingAndTaskBulk(t *testing.T) {
	h := newHarness(t)
	lead := h.env.Register("rex", "leader", "other")
	brief := h.brief("Plans")
	plan := h.plan(lead, brief, "First")

	added := h.object("plans", "add-task", plan, "--title", "Second", "--spec", "do it", "--repo", "o/r",
		"--criterion", "works", "--criterion", "tested", "--depends-on", "T-1", "--max-minutes", "30", "--max-cost-eur", "2")
	tasks := list(t, added, "tasks")
	if len(tasks) != 2 {
		t.Fatalf("add-task = %v", added)
	}
	second := tasks[1].(map[string]any)
	if len(second["acceptance_criteria"].([]any)) != 2 || second["budget"].(map[string]any)["max_minutes"] != float64(30) {
		t.Fatalf("second = %v", second)
	}
	edited := h.object("plans", "edit-task", "T-2", "--title", "Second renamed", "--depends-on", "")
	if edited["title"] != "Second renamed" || len(edited["depends_on"].([]any)) != 0 {
		t.Fatalf("edit-task = %v", edited)
	}
	h.fails(exitUsage, "plans", "edit-task", "T-2")
	h.fails(exitUsage, "plans", "edit-task", "T-2", "--max-minutes", "5")
	dropped := h.object("plans", "drop-task", plan, "T-2")
	if h.object("tasks", "show", "T-2")["status"] != "cancelled" || dropped["id"] != plan {
		t.Fatalf("drop-task = %v", dropped)
	}
	rejected := h.object("plans", "reject", plan, "--reason", "redo")
	if rejected["status"] != "rejected" {
		t.Fatalf("reject = %v", rejected)
	}
	h.fails(exitConflict, "plans", "approve", plan)

	plan2 := h.plan(lead, brief, "Third", "Fourth")
	h.object("plans", "approve", plan2)
	ready := list(t, h.object("tasks", "list", "--brief", brief, "--status", "ready"), "tasks")
	if len(ready) != 2 {
		t.Fatalf("ready = %v", ready)
	}

	out, err := h.exec("", "tasks", "bulk", "cancel", "T-3", "T-404", "T-3", "--reason", "scope cut")
	if err == nil || exitCode(err) != exitFailure || !strings.Contains(err.Error(), "1 of 2") {
		t.Fatalf("bulk error = %v", err)
	}
	var summary struct {
		Failed  int
		Results []struct {
			ID    string
			OK    bool
			Error struct{ Status int }
		}
	}
	if err := json.Unmarshal([]byte(out), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Failed != 1 || len(summary.Results) != 2 || !summary.Results[0].OK || summary.Results[1].Error.Status != 404 {
		t.Fatalf("bulk summary = %s", out)
	}
	if h.object("tasks", "show", "T-3")["status"] != "cancelled" {
		t.Fatal("T-3 not cancelled")
	}
	if _, err := h.exec("T-4\n", "tasks", "bulk", "cancel", "-", "--reason", "stdin"); err != nil {
		t.Fatal(err)
	}
	if h.object("tasks", "show", "T-4")["status"] != "cancelled" {
		t.Fatal("T-4 not cancelled")
	}
	h.fails(exitUsage, "tasks", "bulk", "explode", "T-3")
}

func TestAgentStatsAndBulkActions(t *testing.T) {
	h := newHarness(t)
	h.text("register-agents", "--prefix", "w", "--count", "3", "--role", "worker", "--kind", "claude-code", "--model", "m")
	h.text("register-agent", "rex", "--role", "leader", "--kind", "other", "--model", "m")

	stats := h.object("agents", "stats")
	if stats["total"] != float64(4) || stats["by_role"].(map[string]any)["worker"] != float64(3) ||
		stats["by_provider"].(map[string]any)["anthropic"] != float64(3) {
		t.Fatalf("stats = %v", stats)
	}
	paused := h.object("agents", "bulk-action", "pause", "w-001", "w-002", "--reason", "cost")
	if paused["failed"] != float64(0) || len(list(t, h.object("agents", "--status", "paused"), "agents")) != 2 {
		t.Fatalf("pause = %v", paused)
	}
	h.object("agents", "bulk-action", "resume", "w-001", "w-002")
	if len(list(t, h.object("agents", "--status", "paused"), "agents")) != 0 {
		t.Fatal("agents still paused")
	}
	if _, err := h.exec("", "agents", "bulk-action", "revoke", "w-003", "w-404"); err == nil || exitCode(err) != exitFailure {
		t.Fatalf("revoke with an unknown agent: %v", err)
	}
	if len(list(t, h.object("agents", "--status", "revoked"), "agents")) != 1 {
		t.Fatal("w-003 not revoked")
	}
	if len(list(t, h.object("agents", "--role", "leader"), "agents")) != 1 {
		t.Fatal("agent list filter broken")
	}
}

func TestSpawns(t *testing.T) {
	h := newHarness(t)
	lead := h.env.Register("rex", "leader", "other")
	brief := h.brief("Spawns")
	h.plan(lead, brief, "One")
	h.text("plans", "approve", "P-1")
	code, out := h.env.REST("POST", "/api/v1/bots/spawn", map[string]any{"brief_id": brief, "kind": "claude-code", "model": "m", "n": 1})
	if code != 201 {
		t.Fatalf("spawn: %d %v", code, out)
	}
	spawns := list(t, h.object("spawns", "list", "--brief", brief), "spawns")
	if len(spawns) != 1 {
		t.Fatalf("spawns = %v", spawns)
	}
	spawn := spawns[0].(map[string]any)
	stopped := h.object("spawns", "stop", spawn["id"].(string), "--reason", "done")
	if stopped["agent_id"] != spawn["agent_id"] {
		t.Fatalf("stop = %v", stopped)
	}
	h.fails(exitNotFound, "spawns", "stop", "S-404")
}

func TestAutopilot(t *testing.T) {
	t.Setenv("SUBSPACE_AUTOPILOT", "on")
	t.Setenv("SUBSPACE_AUTOPILOT_INTERVAL", "1h")
	h := newHarness(t)
	brief := h.brief("Autopilot")

	status := h.object("autopilot", "status")
	briefs := list(t, status, "briefs")
	if len(briefs) != 1 || briefs[0].(map[string]any)["enabled"] != false {
		t.Fatalf("status = %v", status)
	}
	if on := h.object("autopilot", "enable", brief); on["enabled"] != true {
		t.Fatalf("enable = %v", on)
	}
	if list(t, h.object("autopilot", "status"), "briefs")[0].(map[string]any)["enabled"] != true {
		t.Fatal("autopilot not enabled")
	}
	if off := h.object("autopilot", "disable", brief); off["enabled"] != false {
		t.Fatalf("disable = %v", off)
	}
	h.fails(exitNotFound, "autopilot", "enable", "B-404")
}

func TestMergeQueue(t *testing.T) {
	dir := t.TempDir()
	cfg, err := json.Marshal(map[string]any{
		"workdir":  filepath.Join(dir, "work"),
		"interval": "1h",
		"repos":    map[string]any{"o/r": map[string]any{"remote": "file://" + filepath.Join(dir, "missing.git"), "verify": []string{"true"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mq.json")
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUBSPACE_MQ_CONFIG", path)
	h := newHarness(t)
	lead := h.env.Register("rex", "leader", "other")
	worker := h.env.Register("claude-1", "worker", "claude-code")
	reviewer := h.env.Register("codex-r", "reviewer", "codex")
	brief := h.brief("Queue")
	h.plan(lead, brief, "Ship")
	h.text("plans", "approve", "P-1")
	h.env.Must(lead, "assign", map[string]any{"task_id": "T-1", "agent_id": "claude-1"})
	h.env.Must(worker, "claim_task", map[string]any{"task_id": "T-1"})
	h.env.Must(worker, "submit", map[string]any{"task_id": "T-1", "pr_url": "https://git.example/o/r/pulls/7", "summary": "done"})

	h.fails(exitConflict, "mergequeue", "enqueue", "T-1")
	h.env.Must(reviewer, "review", map[string]any{"task_id": "T-1", "verdict": "approve", "comments": "ok"})
	c := h.object("mergequeue", "enqueue", "T-1")
	if c["pr"] != float64(7) || c["task_id"] != "T-1" || c["repo"] != "o/r" {
		t.Fatalf("enqueue = %v", c)
	}
	id := c["id"].(string)
	if got := list(t, h.object("mergequeue", "list", "--repo", "o/r"), "candidates"); len(got) != 1 {
		t.Fatalf("list = %v", got)
	}
	if got := h.object("mergequeue", "show", id); got["id"] != id {
		t.Fatalf("show = %v", got)
	}
	h.fails(exitNotFound, "mergequeue", "show", "MQ-404")
}

func TestBackupsAndAudit(t *testing.T) {
	h := newHarness(t)
	h.brief("Audit me")

	created := h.object("backups", "create")
	if created["bytes"].(float64) <= 0 {
		t.Fatalf("backup = %v", created)
	}
	backups := list(t, h.object("backups", "list"), "backups")
	if len(backups) != 1 || backups[0].(map[string]any)["verified"] != true {
		t.Fatalf("backups = %v", backups)
	}

	events := list(t, h.object("audit", "list", "--tool", "create_brief"), "events")
	if len(events) != 1 {
		t.Fatalf("audit list = %v", events)
	}
	verify := h.object("audit", "verify")
	if verify["ok"] != true || verify["events"].(float64) < 1 {
		t.Fatalf("verify = %v", verify)
	}

	exported := h.text("audit", "export")
	lines := strings.Split(strings.TrimSpace(exported), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], `{"chain"`) {
		t.Fatalf("export = %q", exported)
	}
	file := filepath.Join(t.TempDir(), "audit.ndjson")
	saved := h.object("audit", "export", "--out", file)
	data, err := os.ReadFile(file) //nolint:gosec
	if err != nil || string(data) != exported || saved["bytes"] != float64(len(data)) {
		t.Fatalf("saved export: %v %v", err, saved)
	}
	if out := h.text("--json", "audit", "export"); out != exported {
		t.Fatal("--json changed NDJSON output")
	}
}

func TestArtifacts(t *testing.T) {
	t.Setenv("SUBSPACE_DATA_DIR", t.TempDir())
	h := newHarness(t)
	brief := h.brief("Files")
	dir := t.TempDir()
	src := filepath.Join(dir, "run.log")
	content := []byte("line one\nline two\n")
	if err := os.WriteFile(src, content, 0o600); err != nil {
		t.Fatal(err)
	}

	art := h.object("artifacts", "upload", "--scope", "project:"+brief, "--file", src)
	id := art["id"].(string)
	if art["name"] != "run.log" || art["size"] != float64(len(content)) || art["scope"] != "project:"+brief {
		t.Fatalf("upload = %v", art)
	}
	if got := list(t, h.object("artifacts", "list", "--scope", "project:"+brief), "artifacts"); len(got) != 1 {
		t.Fatalf("list = %v", got)
	}
	dst := filepath.Join(dir, "copy.log")
	saved := h.object("artifacts", "download", id, "--out", dst)
	got, err := os.ReadFile(dst) //nolint:gosec
	if err != nil || !bytes.Equal(got, content) || saved["sha256"] != art["sha256"] {
		t.Fatalf("download: %v %v %q", err, saved, got)
	}
	if out := h.text("artifacts", "download", id, "--out", "-"); out != string(content) {
		t.Fatalf("stdout download = %q", out)
	}
	h.fails(exitUsage, "--json", "artifacts", "download", id, "--out", "-")
	if out, err := h.exec("piped", "artifacts", "upload", "--scope", "project:"+brief, "--file", "-", "--name", "piped.txt"); err != nil || !strings.Contains(out, "piped.txt") {
		t.Fatalf("stdin upload: %q %v", out, err)
	}
	h.text("artifacts", "delete", id)
	h.fails(exitNotFound, "artifacts", "download", id, "--out", dst)
	h.fails(exitUsage, "artifacts", "upload", "--scope", "everywhere", "--file", src)
}

func TestInstructions(t *testing.T) {
	h := newHarness(t)
	brief := h.brief("Rules")

	if _, err := h.exec("", "instructions", "get", "--kind", "rules"); exitCode(err) != exitNotFound {
		t.Fatalf("unsaved instructions: %v", err)
	}
	v1 := h.object("instructions", "set", "--kind", "rules", "--text", "Be brief.", "--message", "first")
	if v1["version"] != float64(1) || v1["text"] != "Be brief." {
		t.Fatalf("set = %v", v1)
	}
	h.text("instructions", "set", "--kind", "style", "--text", "Use tabs.")
	out, err := h.exec("Review every PR.\n", "instructions", "set", "--kind", "rules", "--scope", "project:"+brief, "--text-file", "-")
	if err != nil || !strings.Contains(out, "Review every PR.") {
		t.Fatalf("project set: %q %v", out, err)
	}
	head := h.object("instructions", "get", "--kind", "rules")
	if head["text"] != "Be brief." {
		t.Fatalf("get = %v", head)
	}
	effective := h.object("instructions", "get", "--project", brief)
	if text := effective["text"].(string); !strings.Contains(text, "Use tabs.") || !strings.Contains(text, "Review every PR.") {
		t.Fatalf("effective = %v", effective)
	}
	h.fails(exitUsage, "instructions", "set", "--kind", "poetry", "--text", "x")
	h.fails(exitUsage, "instructions", "set", "--kind", "rules")
	h.fails(exitUsage, "instructions", "get", "--kind", "rules", "--scope", "../x")
}

func TestPromptsAndBriefDocuments(t *testing.T) {
	h := newHarness(t)

	if got := list(t, h.object("prompts", "list", "--tag", "performance"), "prompts"); len(got) == 0 {
		t.Fatal("no performance prompts")
	}
	prompt := h.object("prompts", "show", "add-otel-measure-bottlenecks")
	if prompt["id"] != "add-otel-measure-bottlenecks" {
		t.Fatalf("show = %v", prompt)
	}
	rendered := h.object("prompts", "render", "add-otel-measure-bottlenecks", "--var", "target=the billing API")
	if !strings.Contains(rendered["text"].(string), "the billing API") {
		t.Fatalf("render = %v", rendered)
	}
	h.fails(exitUsage, "prompts", "render", "add-otel-measure-bottlenecks")
	h.fails(exitUsage, "prompts", "render", "add-otel-measure-bottlenecks", "--var", "novalue")
	h.fails(exitNotFound, "prompts", "show", "nope")

	templates := list(t, h.object("briefs", "templates"), "templates")
	if len(templates) == 0 {
		t.Fatal("no brief templates")
	}
	var variables []any
	for _, tpl := range templates {
		if tpl.(map[string]any)["id"] == "feature" {
			variables = tpl.(map[string]any)["variables"].([]any)
		}
	}
	args := []string{"briefs", "templates", "feature"}
	for _, v := range variables {
		args = append(args, "--var", v.(string)+"=Search")
	}
	doc := h.object(args...)["markdown"].(string)

	out, err := h.exec(doc, "briefs", "import", "--file", "-", "--dry-run")
	if err != nil || !strings.Contains(out, `"preview"`) {
		t.Fatalf("dry run: %q %v", out, err)
	}
	if got := list(t, h.object("briefs"), "briefs"); len(got) != 0 {
		t.Fatalf("dry run created briefs: %v", got)
	}
	file := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(file, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	imported := h.object("briefs", "import", "--file", file)
	if imported["brief"].(map[string]any)["id"] != "B-1" {
		t.Fatalf("import = %v", imported)
	}
	h.fails(exitUsage, "briefs", "import", "--file", filepath.Join(t.TempDir(), "absent.md"))
	if _, err := h.exec("no frontmatter", "briefs", "import", "--file", "-"); exitCode(err) != exitUsage {
		t.Fatalf("invalid document: %v", err)
	}
}

func TestLimits(t *testing.T) {
	h := newHarness(t)
	cfg := `{"global_max_agents":50,"limits":[{"provider":"anthropic","model":"*","max_concurrency":5,"requests_per_min":60},{"provider":"openai","model":"*","max_concurrency":2,"requests_per_min":10}]}`
	if _, err := h.exec(cfg, "capacity", "set", "--file", "-"); err != nil {
		t.Fatal(err)
	}
	all := h.object("limits")
	if len(list(t, all, "limits")) != 2 || all["global_max_agents"] != float64(50) {
		t.Fatalf("limits = %v", all)
	}
	one := list(t, h.object("limits", "--provider", "openai"), "limits")
	if len(one) != 1 || one[0].(map[string]any)["max_concurrency"] != float64(2) {
		t.Fatalf("openai limits = %v", one)
	}
}

func TestJSONOutputAndExitCodes(t *testing.T) {
	h := newHarness(t)
	h.brief("Codes")

	out := h.text("--json", "briefs")
	if strings.Count(strings.TrimSpace(out), "\n") != 0 || !json.Valid([]byte(out)) {
		t.Fatalf("--json output is not compact: %q", out)
	}
	if pretty := h.text("briefs"); strings.Count(pretty, "\n") < 3 {
		t.Fatalf("default output is not indented: %q", pretty)
	}
}

func TestErrorsAndCredentials(t *testing.T) {
	h := newHarness(t)
	h.brief("Credentials")
	run := func(env map[string]string, args ...string) (string, error) {
		var out bytes.Buffer
		err := runWith(t, env, &out, args...)
		return out.String(), err
	}
	good := map[string]string{"SUBSPACE_URL": h.env.TS.URL, "SUBSPACE_OWNER_TOKEN": servertest.OwnerToken}

	missing := map[string]string{"SUBSPACE_URL": h.env.TS.URL}
	if _, err := run(missing, "briefs"); exitCode(err) != exitAuth {
		t.Fatalf("missing token: %v", err)
	}
	wrong := "wrong-token-that-must-never-be-echoed-0123"
	_, err := run(map[string]string{"SUBSPACE_URL": h.env.TS.URL, "SUBSPACE_OWNER_TOKEN": wrong}, "briefs")
	if exitCode(err) != exitAuth || strings.Contains(render(err), wrong) {
		t.Fatalf("wrong token: %v", err)
	}
	if _, err := run(map[string]string{"SUBSPACE_URL": "http://127.0.0.1:1", "SUBSPACE_OWNER_TOKEN": wrong}, "briefs"); exitCode(err) != exitUnavailable {
		t.Fatalf("unreachable: %v", err)
	}
	if _, err := run(good, "--url", "ftp://example.com", "briefs"); exitCode(err) != exitUsage {
		t.Fatalf("bad url: %v", err)
	}

	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(servertest.OwnerToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(map[string]string{"SUBSPACE_URL": h.env.TS.URL}, "--token-file", tokenFile, "briefs"); err != nil {
		t.Fatalf("token file flag: %v", err)
	}
	if _, err := run(map[string]string{"SUBSPACE_URL": h.env.TS.URL, "SUBSPACE_OWNER_TOKEN_FILE": tokenFile, "SUBSPACE_OWNER_TOKEN": wrong}, "briefs"); err != nil {
		t.Fatalf("token file env must win over the token env: %v", err)
	}
	if _, err := run(map[string]string{"SUBSPACE_URL": h.env.TS.URL}, "--token="+servertest.OwnerToken, "briefs"); err != nil {
		t.Fatalf("token flag: %v", err)
	}
	_, err = run(missing, "--token-file", filepath.Join(t.TempDir(), "absent"), "briefs")
	if exitCode(err) != exitAuth {
		t.Fatalf("absent token file: %v", err)
	}

	_, err = run(good, "--json", "board", "B-404")
	if exitCode(err) != exitNotFound {
		t.Fatalf("not found: %v", err)
	}
	var body struct {
		Error struct {
			Code   string
			Status int
			Exit   int
		}
	}
	if err := json.Unmarshal([]byte(render(err)), &body); err != nil || body.Error.Code != "not_found" || body.Error.Status != 404 || body.Error.Exit != exitNotFound {
		t.Fatalf("JSON error = %q (%v)", render(err), err)
	}
	var plain *cliError
	if _, err := run(good, "board", "B-404"); !errors.As(err, &plain) || !strings.HasPrefix(render(err), "subspacectl: HTTP 404") {
		t.Fatalf("plain error = %v", err)
	}
	if _, err := run(good, "nonsense"); exitCode(err) != exitUsage {
		t.Fatalf("unknown command: %v", err)
	}
	if _, err := run(good, "decisions"); exitCode(err) != exitUsage {
		t.Fatalf("missing subcommand: %v", err)
	}
}

func runWith(t *testing.T, env map[string]string, out *bytes.Buffer, args ...string) error {
	t.Helper()
	return run(t.Context(), args, mapEnv(env), strings.NewReader(""), out)
}

func TestCompletionScriptsCoverEveryCommand(t *testing.T) {
	getenv := func(key string) string {
		t.Errorf("completion read %s", key)
		return ""
	}
	for _, shell := range []string{"bash", "zsh", "fish"} {
		var out bytes.Buffer
		if err := run(t.Context(), []string{"completion", shell}, getenv, strings.NewReader(""), &out); err != nil {
			t.Fatal(err)
		}
		for name, e := range registry() {
			if !strings.Contains(out.String(), name) {
				t.Errorf("%s completion misses %s", shell, name)
			}
			for _, sub := range e.subs {
				if !strings.Contains(out.String(), sub) {
					t.Errorf("%s completion misses %s %s", shell, name, sub)
				}
			}
		}
	}
	if err := run(t.Context(), []string{"completion", "powershell"}, getenv, strings.NewReader(""), io.Discard); exitCode(err) != exitUsage {
		t.Fatalf("unknown shell: %v", err)
	}
	var wrapped bytes.Buffer
	if err := run(t.Context(), []string{"--json", "completion", "bash"}, getenv, strings.NewReader(""), &wrapped); err != nil || !json.Valid(wrapped.Bytes()) {
		t.Fatalf("json completion: %v", err)
	}
}

func TestRegistryMatchesParsers(t *testing.T) {
	for name, e := range registry() {
		if _, err := parse([]string{name, "--help"}, strings.NewReader("")); !errors.Is(err, flag.ErrHelp) {
			t.Errorf("%s --help = %v", name, err)
		}
		for _, sub := range e.subs {
			if _, err := parse([]string{name, sub, "--help"}, strings.NewReader("")); !errors.Is(err, flag.ErrHelp) {
				t.Errorf("%s %s --help = %v", name, sub, err)
			}
		}
	}
}
