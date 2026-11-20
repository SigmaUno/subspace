package mcp

import "testing"

func TestRoleFiltering(t *testing.T) {
	r := DefaultRegistry()
	has := func(role, name string) bool { _, ok := r.Get(name, role); return ok }
	cases := []struct {
		role, tool string
		want       bool
	}{
		{"worker", "claim_task", true}, {"worker", "review", false}, {"worker", "assign", false},
		{"tester", "report_tests", true}, {"worker", "report_tests", false},
		{"reviewer", "review", true}, {"reviewer", "claim_task", false},
		{"leader", "propose_plan", true}, {"leader", "claim_task", false},
		{"owner", "get_instructions", false},
	}
	for _, c := range cases {
		if got := has(c.role, c.tool); got != c.want {
			t.Errorf("%s/%s = %v want %v", c.role, c.tool, got, c.want)
		}
	}
}
