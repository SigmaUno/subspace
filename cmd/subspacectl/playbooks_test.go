package main

import (
	"encoding/json"
	"testing"
)

func TestPlaybookCommands(t *testing.T) {
	h := newHarness(t)
	if got := list(t, h.object("playbooks", "list"), "playbooks"); len(got) == 0 {
		t.Fatalf("no built-in playbooks")
	}

	created := h.object("playbooks", "create", "hotfix-strict", "--description", "tight", "--coverage", "tester",
		"--allowed-sizes", "s,m", "--max-minutes", "45", "--max-cost-eur", "2", "--cross-review")
	budget := created["default_task_budget"].(map[string]any)
	if created["name"] != "hotfix-strict" || created["coverage"] != "tester" || created["cross_review"] != true ||
		len(created["allowed_sizes"].([]any)) != 2 || budget["max_minutes"] != float64(45) || budget["max_cost_eur"] != float64(2) {
		t.Fatalf("create = %v", created)
	}
	h.fails(exitConflict, "playbooks", "create", "hotfix-strict")
	h.fails(exitUsage, "playbooks", "create", "Bad_Name")
	h.fails(exitUsage, "playbooks", "create", "bad-coverage", "--coverage", "everyone")

	updated := h.object("playbooks", "update", "hotfix-strict", "--instructions", "keep it small", "--max-minutes", "30")
	budget = updated["default_task_budget"].(map[string]any)
	if updated["instructions"] != "keep it small" || updated["coverage"] != "tester" || updated["cross_review"] != true ||
		budget["max_minutes"] != float64(30) || budget["max_cost_eur"] != float64(2) {
		t.Fatalf("update must keep the fields it was not given: %v", updated)
	}
	h.fails(exitUsage, "playbooks", "update", "hotfix-strict")
	h.fails(exitNotFound, "playbooks", "update", "ghost", "--description", "x")

	fromFile := `{"name":"ignored","description":"from file","coverage":"","required_tasks":[],"criteria_rules":[],` +
		`"allowed_sizes":[],"default_task_budget":{"max_minutes":10,"max_cost_eur":1},"brief_cost_multiplier":0.5,` +
		`"cross_review":false,"instructions":"file"}`
	out, err := h.exec(fromFile, "playbooks", "update", "hotfix-strict", "--file", "-")
	if err != nil {
		t.Fatal(err)
	}
	var replaced map[string]any
	if err := json.Unmarshal([]byte(out), &replaced); err != nil {
		t.Fatal(err)
	}
	if replaced["name"] != "hotfix-strict" || replaced["brief_cost_multiplier"] != 0.5 || replaced["instructions"] != "file" {
		t.Fatalf("update from file = %v", replaced)
	}
	h.fails(exitUsage, "playbooks", "update", "hotfix-strict", "--file", "-", "--description", "x")

	if shown := h.object("playbooks", "show", "hotfix-strict"); shown["description"] != "from file" {
		t.Fatalf("show = %v", shown)
	}
	cloned := h.object("playbooks", "clone", "hotfix-strict", "hotfix-copy", "--description", "copy")
	if cloned["name"] != "hotfix-copy" || cloned["builtin"] == true {
		t.Fatalf("clone = %v", cloned)
	}
	h.fails(exitConflict, "playbooks", "clone", "hotfix-strict", "hotfix-copy")
	h.fails(exitNotFound, "playbooks", "clone", "ghost", "another")

	h.text("playbooks", "delete", "hotfix-copy")
	h.fails(exitNotFound, "playbooks", "show", "hotfix-copy")
	h.fails(exitUsage, "playbooks", "show", "../etc")
	h.text("playbooks", "delete", "hotfix-strict")
	h.fails(exitNotFound, "playbooks", "delete", "hotfix-strict")
}
