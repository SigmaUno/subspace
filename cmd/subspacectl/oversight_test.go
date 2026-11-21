package main

import (
	"strings"
	"testing"
)

func TestBriefReadWriteAndHandoff(t *testing.T) {
	h := newHarness(t)
	h.env.Register("rex", "leader", "other")
	brief := h.brief("Handoff")

	if got := h.object("briefs", "show", brief)["id"]; got != brief {
		t.Fatalf("show id = %v", got)
	}
	h.fails(exitNotFound, "briefs", "show", "B-404")

	updated, err := h.exec(`{"max_cost_eur":12}`, "briefs", "update", brief, "--file", "-")
	if err != nil || !strings.Contains(updated, `"max_cost_eur": 12`) {
		t.Fatalf("update = %s (%v)", updated, err)
	}
	h.fails(exitUsage, "briefs", "update", brief)

	if got := h.object("briefs", "leader", brief, "--agent", "rex")["leader_id"]; got != "rex" {
		t.Fatalf("leader = %v", got)
	}
	if got := h.object("briefs", "leader", brief)["leader_id"]; got != nil {
		t.Fatalf("any leader = %v", got)
	}
	h.fails(exitUsage, "briefs", "leader", brief, "--agent", "not valid")

	components := `{"components":[{"name":"store","paths":["internal/store"]}]}`
	if _, err := h.exec(components, "briefs", "components", "set", brief, "--file", "-"); err != nil {
		t.Fatalf("components set: %v", err)
	}
	nodes := list(t, h.object("briefs", "architecture", brief), "nodes")
	if len(nodes) != 1 || nodes[0].(map[string]any)["name"] != "store" {
		t.Fatalf("architecture = %v", nodes)
	}
}

func TestTaskGateOverride(t *testing.T) {
	h := newHarness(t)
	lead := h.env.Register("rex", "leader", "other")
	brief := h.brief("Overrides")
	h.plan(lead, brief, "One")

	report := h.object("tasks", "override-gates", "T-1", "--reason", "accepted risk", "--gates", "checks")
	if report["task_id"] != "T-1" || report["override"] == nil {
		t.Fatalf("override = %v", report)
	}
	h.fails(exitUsage, "tasks", "override-gates", "T-1")
	h.fails(exitNotFound, "tasks", "override-gates", "T-404", "--reason", "x")
}

func TestThreadsLogLevelAndFeeds(t *testing.T) {
	h := newHarness(t)
	lead := h.env.Register("rex", "leader", "other")
	brief := h.brief("Threads")
	h.plan(lead, brief, "One")

	h.text("threads", "post", "task", "T-1", "--body", "please rebase")
	if _, err := h.exec("status update\n", "threads", "post", "brief", brief, "--body-file", "-"); err != nil {
		t.Fatalf("post from stdin: %v", err)
	}
	if msgs := list(t, h.object("threads", "list", "task", "T-1"), "messages"); len(msgs) != 1 {
		t.Fatalf("task thread = %v", msgs)
	}
	if msgs := list(t, h.object("threads", "list", "brief", brief, "--limit", "5"), "messages"); len(msgs) == 0 {
		t.Fatal("brief thread empty")
	}
	h.fails(exitUsage, "threads", "list", "project", "P-1")
	h.fails(exitUsage, "threads", "post", "task", "T-1")

	if level := h.object("log-level", "set", "--level", "warn")["level"]; level != "warn" {
		t.Fatalf("set level = %v", level)
	}
	if level := h.object("log-level", "get")["level"]; level != "warn" {
		t.Fatalf("get level = %v", level)
	}
	h.fails(exitUsage, "log-level", "set", "--level", "loud")
	h.fails(exitUsage, "log-level", "set")

	if prs := list(t, h.object("prs", "--state", "open"), "prs"); len(prs) != 0 {
		t.Fatalf("prs = %v", prs)
	}
	if reqs := list(t, h.object("help-requests", "--brief-id", brief), "help_requests"); len(reqs) != 0 {
		t.Fatalf("help requests = %v", reqs)
	}
	h.object("activity", "--brief-id", brief)
	h.fails(exitUsage, "help-requests", "--limit", "5000")
}

func TestAutopilotSpawnOptions(t *testing.T) {
	t.Setenv("SUBSPACE_AUTOPILOT", "on")
	t.Setenv("SUBSPACE_AUTOPILOT_INTERVAL", "1h")
	h := newHarness(t)
	brief := h.brief("Spawn")

	set := h.object("autopilot", "spawn", brief, "--worker-kind", "claude-code", "--model", "claude-sonnet-5-5")
	if set["id"] != brief {
		t.Fatalf("spawn = %v", set)
	}
	h.fails(exitUsage, "autopilot", "spawn", brief, "--worker-kind", "claude-code")
	h.text("autopilot", "spawn", brief)
	h.fails(exitNotFound, "autopilot", "spawn", "B-404", "--worker-kind", "codex", "--model", "m")
}

func TestTokenLeakReissueUnknown(t *testing.T) {
	h := newHarness(t)
	h.fails(exitNotFound, "token-leaks", "reissue", "TL-404")
	h.fails(exitUsage, "token-leaks", "reissue", "../x")
}

func TestInstructionHistoryAndRollback(t *testing.T) {
	h := newHarness(t)
	h.object("instructions", "set", "--kind", "rules", "--text", "Be brief.", "--message", "first")
	h.object("instructions", "set", "--kind", "rules", "--text", "Be very brief.", "--message", "second")

	if versions := list(t, h.object("instructions", "versions", "--kind", "rules"), "versions"); len(versions) != 2 {
		t.Fatalf("versions = %v", versions)
	}
	if text := h.object("instructions", "get", "--kind", "rules", "--version", "1")["text"]; text != "Be brief." {
		t.Fatalf("version 1 = %v", text)
	}
	if lines := list(t, h.object("instructions", "diff", "--kind", "rules", "--from", "1", "--to", "2"), "lines"); len(lines) == 0 {
		t.Fatal("empty diff")
	}
	rolled := h.object("instructions", "rollback", "--kind", "rules", "--to", "1", "--message", "revert")
	if rolled["text"] != "Be brief." || rolled["version"] != float64(3) {
		t.Fatalf("rollback = %v", rolled)
	}
	h.fails(exitUsage, "instructions", "diff", "--kind", "rules", "--from", "1")
	h.fails(exitUsage, "instructions", "rollback", "--kind", "rules")
	h.fails(exitNotFound, "instructions", "get", "--kind", "rules", "--version", "9")
}

func TestActionLinksTimeseriesAndHillclimb(t *testing.T) {
	h := newHarness(t)
	lead := h.env.Register("rex", "leader", "other")
	brief := h.brief("Links")
	h.plan(lead, brief, "One")

	h.fails(exitConflict, "action-links", "create", "--decision", "plan:P-1", "--action", "approve", "--ttl", "1h")
	h.fails(exitUsage, "action-links", "create", "--decision", "nope", "--action", "approve")
	h.fails(exitUsage, "action-links", "create", "--decision", "plan:P-1")

	if series := h.object("timeseries", "--metric", "tasks_created", "--bucket", "1h"); series["metric"] != "tasks_created" {
		t.Fatalf("series = %v", series)
	}
	h.fails(exitUsage, "timeseries", "--metric", "loud", "--bucket", "1h")
	h.fails(exitUsage, "timeseries", "--metric", "tasks_done")

	if jobs := list(t, h.object("hillclimb", "list"), "jobs"); len(jobs) != 0 {
		t.Fatalf("jobs = %v", jobs)
	}
	h.fails(exitNotFound, "hillclimb", "series", "H-404")
}
