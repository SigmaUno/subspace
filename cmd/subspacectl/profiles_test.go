package main

import "testing"

func TestProfilesAndRegistrationFromProfile(t *testing.T) {
	h := newHarness(t)
	builtin := list(t, h.object("profiles", "list"), "profiles")
	if len(builtin) != 3 {
		t.Fatalf("profiles = %v", builtin)
	}

	created := h.object("profiles", "create", "tester-fleet", "--kind", "codex", "--role", "tester", "--model", "gpt-5",
		"--expires-in", "24h", "--count", "2", "--description", "cheap testers")
	if created["name"] != "tester-fleet" || created["provider"] != "" || created["count"] != float64(2) {
		t.Fatalf("create = %v", created)
	}
	h.fails(exitConflict, "profiles", "create", "tester-fleet", "--kind", "codex")
	h.fails(exitUsage, "profiles", "create", "no-kind")
	h.fails(exitUsage, "profiles", "create", "../x", "--kind", "codex")
	h.fails(exitUsage, "profiles", "create", "bad-kind", "--kind", "gpt")

	one := h.object("agents", "register", "solo", "--profile", "tester-fleet", "--model", "gpt-5-mini")["agent"].(map[string]any)
	if one["role"] != "tester" || one["kind"] != "codex" || one["model"] != "gpt-5-mini" || one["provider"] != "openai" || one["token_expires_at"] == nil {
		t.Fatalf("agent = %v", one)
	}
	many := list(t, h.object("agents", "register", "--prefix", "fleet", "--profile", "tester-fleet"), "agents")
	if len(many) != 2 || many[1].(map[string]any)["agent"].(map[string]any)["id"] != "fleet-002" {
		t.Fatalf("bulk = %v", many)
	}
	many = list(t, h.object("register-agents", "--prefix", "more", "--count", "3", "--profile", "claude-code-worker"), "agents")
	if len(many) != 3 || many[0].(map[string]any)["agent"].(map[string]any)["kind"] != "claude-code" {
		t.Fatalf("register-agents = %v", many)
	}
	h.object("register-agent", "plain", "--profile", "codex-worker")
	h.fails(exitUsage, "agents", "register", "ghost-agent", "--profile", "no-such-profile")
	h.fails(exitUsage, "agents", "register", "x", "--prefix", "y", "--profile", "codex-worker")
	h.fails(exitUsage, "agents", "register", "needs-fields")
	h.fails(exitUsage, "register-agents", "--prefix", "z", "--profile", "codex-worker", "--count", "1001")

	h.text("profiles", "delete", "tester-fleet")
	h.fails(exitNotFound, "profiles", "delete", "tester-fleet")
	if got := list(t, h.object("profiles", "list"), "profiles"); len(got) != 3 {
		t.Fatalf("profiles after delete = %v", got)
	}
}
