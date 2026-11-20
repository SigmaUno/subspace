package mcp

import (
	"context"
	"errors"
)

var errToolTODO = errors.New("tool not implemented yet")

func todo(context.Context, *Call) (any, error) { return nil, errToolTODO }

func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str() map[string]any { return map[string]any{"type": "string"} }

// DefaultRegistry registers every tool with its role set. Handlers are stubs
// until wired to board.Service.
// TODO(split C*): each tool group gets its real handler + exact schema from docs/api.md §5.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	add := func(name, desc string, roles []string, ro bool, schema map[string]any) {
		if schema == nil {
			schema = obj(map[string]any{})
		}
		r.Register(&Tool{Name: name, Description: desc, Roles: roles, ReadOnly: ro, Schema: schema, Handler: todo})
	}
	taskID := obj(map[string]any{"task_id": str()}, "task_id")
	add("get_instructions", "Return your rules and role instructions.", AllRoles, true, nil)
	add("whoami", "Return your agent record.", AllRoles, true, nil)
	add("list_my_tasks", "List tasks assigned to you.", AllRoles, true, nil)
	add("get_task", "Get one task.", AllRoles, true, taskID)
	add("heartbeat", "Extend your leases.", AllRoles, true, nil)
	add("update_status", "Update task status/progress.", AllRoles, false, nil)
	add("add_note", "Add a note to a thread.", AllRoles, false, nil)
	add("ask_owner", "Ask the owner a question.", AllRoles, false, nil)
	add("post_message", "Post to a thread.", AllRoles, false, nil)
	add("read_thread", "Read a thread.", AllRoles, true, nil)
	add("claim_task", "Claim an assigned task (lease).", WorkTester, false, taskID)
	add("release_task", "Release a claimed task.", WorkTester, false, taskID)
	add("submit", "Submit a task with its PR.", WorkTester, false, nil)
	add("report_tests", "Report test results.", TesterOnly, false, nil)
	add("list_reviews", "List tasks awaiting your review.", ReviewOnly, true, nil)
	add("review", "Review a submitted task.", ReviewOnly, false, nil)
	for _, n := range []string{"get_brief", "list_briefs", "board", "list_agents", "capacity"} {
		add(n, n, LeaderOnly, true, nil)
	}
	for _, n := range []string{"propose_plan", "create_task", "update_task", "assign", "reassign", "request_spawn"} {
		add(n, n, LeaderOnly, false, nil)
	}
	return r
}
