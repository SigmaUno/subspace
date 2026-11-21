package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestInstructionsVersionedAndScoped(t *testing.T) {
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	wk := e.register("claude-1", "worker", "claude-code")
	other := e.register("claude-2", "worker", "claude-code")

	brief := func(title string) string {
		_, br := e.rest("POST", "/api/v1/briefs", map[string]any{"title": title, "body": "b", "repos": []string{"o/r"}, "max_cost_eur": 10, "max_parallel_agents": 5})
		return br["id"].(string)
	}
	b1, b2 := brief("one"), brief("two")
	plan := func(bid string) string {
		p := e.must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{map[string]any{"title": "t", "spec": "s", "repo": "o/r"}}})["plan"].(map[string]any)
		if code, _ := e.rest("POST", "/api/v1/plans/"+p["id"].(string)+"/approve", map[string]any{}); code != 200 {
			t.Fatal("approve")
		}
		return p["tasks"].([]any)[0].(map[string]any)["id"].(string)
	}
	t1, t2 := plan(b1), plan(b2)
	e.must(lead, "assign", map[string]any{"task_id": t1, "agent_id": "claude-1"})
	e.must(lead, "assign", map[string]any{"task_id": t2, "agent_id": "claude-2"})

	instr := func(tok string) string {
		return e.must(tok, "get_instructions", nil)["instructions"].(string)
	}
	base := instr(wk)
	if strings.Contains(base, "Owner instructions") || !strings.HasSuffix(base, "--- Owner charter ---\n") {
		t.Fatalf("unexpected default instructions: %q", base)
	}

	if code, _ := e.rest("PUT", "/api/v1/charter", map[string]any{"text": "CHARTER-TEXT"}); code != 200 {
		t.Fatal("charter")
	}
	put := func(scope, kind, text string) (int, map[string]any) {
		title := "Global"
		if scope != "global" {
			title = "Brief"
		}
		return e.rest("PUT", "/api/v1/instructions/"+scope+"/"+kind, map[string]any{"text": "## " + title + "\n" + text, "message": "edit"})
	}
	if code, out := put("global", "rules", "GLOBAL-RULE-1"); code != http.StatusCreated || out["version"] != float64(1) {
		t.Fatalf("save global: %d %v", code, out)
	}
	if code, _ := put("project:"+b1, "rules", "BRIEF1-RULE"); code != http.StatusCreated {
		t.Fatal("save brief 1")
	}
	if code, _ := put("project:"+b2, "rules", "BRIEF2-RULE"); code != http.StatusCreated {
		t.Fatal("save brief 2")
	}

	got := instr(wk)
	if !strings.HasPrefix(got, strings.TrimSuffix(base, "\n--- Owner charter ---\n")) {
		t.Fatalf("base text changed: %q", got)
	}
	for _, want := range []string{"GLOBAL-RULE-1", "BRIEF1-RULE"} {
		if !strings.Contains(got, want) {
			t.Fatalf("worker 1 missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "BRIEF2-RULE") {
		t.Fatalf("worker 1 sees another brief's instructions: %q", got)
	}
	if !strings.HasSuffix(got, "--- Owner charter ---\nCHARTER-TEXT") {
		t.Fatalf("charter must stay last: %q", got)
	}
	if o := instr(other); !strings.Contains(o, "BRIEF2-RULE") || strings.Contains(o, "BRIEF1-RULE") {
		t.Fatalf("worker 2 scoping wrong: %q", o)
	}
	if l := instr(lead); !strings.Contains(l, "GLOBAL-RULE-1") || strings.Contains(l, "BRIEF") {
		t.Fatalf("leader without task should only see global: %q", l)
	}

	put("global", "rules", "GLOBAL-RULE-2")
	if g := instr(wk); !strings.Contains(g, "GLOBAL-RULE-2") || strings.Contains(g, "GLOBAL-RULE-1") {
		t.Fatalf("new version not active: %q", g)
	}

	code, hist := e.rest("GET", "/api/v1/instructions/global/rules/versions", nil)
	if code != 200 || len(hist["versions"].([]any)) != 2 {
		t.Fatalf("history: %d %v", code, hist)
	}
	if code, out := e.rest("POST", "/api/v1/instructions/global/rules/rollback", map[string]any{"to": 1}); code != http.StatusCreated || out["version"] != float64(3) {
		t.Fatalf("rollback: %d %v", code, out)
	}
	if g := instr(wk); !strings.Contains(g, "GLOBAL-RULE-1") || strings.Contains(g, "GLOBAL-RULE-2") {
		t.Fatalf("rollback not active: %q", g)
	}
	if code, _ := e.rest("POST", "/api/v1/instructions/global/rules/rollback", map[string]any{"to": 3}); code != http.StatusConflict {
		t.Fatalf("rollback to head: %d", code)
	}
	if code, _ := e.rest("POST", "/api/v1/instructions/global/rules/rollback", map[string]any{"to": 9}); code != http.StatusNotFound {
		t.Fatalf("rollback to missing: %d", code)
	}
	if code, _ := put("global", "bogus", "x"); code != http.StatusBadRequest {
		t.Fatalf("bad kind: %d", code)
	}

	if code, out := e.rest("GET", "/api/v1/audit?tool=instructions_rollback", nil); code != 200 || len(out["events"].([]any)) != 1 {
		t.Fatalf("audit: %d %v", code, out)
	}

	req, _ := http.NewRequest("GET", e.ts.URL+"/api/v1/instructions/global/rules", nil)
	req.Header.Set("Authorization", "Bearer "+wk)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("agent token on instructions REST: %d", resp.StatusCode)
	}
}
