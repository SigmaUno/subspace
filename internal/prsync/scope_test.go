package prsync_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/prsync"
)

func fakeFiles(names ...string) []map[string]any {
	out := make([]map[string]any, len(names))
	for i, n := range names {
		out[i] = map[string]any{"filename": n, "additions": 10, "deletions": 2}
	}
	return out
}

func (h *harness) scope(id string) (int, map[string]any) {
	h.t.Helper()
	return h.e.REST("GET", "/api/v1/tasks/"+id+"/scope", nil)
}

func scopeRules(out map[string]any) map[string]string {
	rules := map[string]string{}
	for _, x := range out["findings"].([]any) {
		f := x.(map[string]any)
		rules[f["rule"].(string)] = f["severity"].(string)
	}
	return rules
}

func (h *harness) mergeReady(id string) (map[string]any, bool) {
	h.t.Helper()
	_, dec := h.e.REST("GET", "/api/v1/decisions", nil)
	for _, x := range dec["decisions"].([]any) {
		if d := x.(map[string]any); d["id"] == "merge-ready:"+id {
			return d, true
		}
	}
	return nil, false
}

func TestScopeFindingsGateMergeReadyOverHTTP(t *testing.T) {
	gh, fj := newFakeHost(t, prsync.KindGitHub), newFakeHost(t, prsync.KindForgejo)
	h := newHarness(t, gh, fj, "", `{"per_minute": 6000, "burst": 1000}`)
	id := h.setup(1)[0]
	if code, out := h.e.REST("PATCH", "/api/v1/tasks/"+id, map[string]any{"paths": []string{"internal/api/**"}}); code != 200 {
		t.Fatalf("patch paths: %d %v", code, out)
	}
	_, task := h.e.REST("GET", "/api/v1/tasks/"+id, nil)
	branch := task["branch"].(string)

	gh.set("o/gh", 1, func(p *fakePR) {
		p.Status, p.Mergeable, p.Ref = "success", ptr(true), "feature/other"
		p.Authors = map[string]string{"a000000000": "mallory"}
		p.Files = fakeFiles("internal/api/a.go", "cmd/main.go", ".github/workflows/ci.yml", "web/pnpm-lock.yaml")
	})
	h.submit(id, ghURL(1))
	if code, out := h.scope(id); code != 404 {
		t.Fatalf("scope before the first sync: %d %v", code, out)
	}
	h.sync()

	code, out := h.scope(id)
	if code != 200 || out["merge_eligible"] != false || out["files"] != 4.0 || out["additions"] != 40.0 || out["deletions"] != 8.0 {
		t.Fatalf("scope: %d %v", code, out)
	}
	rules := scopeRules(out)
	if rules["outside_paths"] != "error" || rules["protected_path"] != "error" || rules["branch_mismatch"] != "error" || rules["unexpected_author"] != "error" || len(rules) != 4 {
		t.Fatalf("rules: %v", rules)
	}
	if got := fmt.Sprint(out["findings"]); !strings.Contains(got, ".github/workflows/ci.yml") {
		t.Fatalf("findings do not name the protected file: %s", got)
	}

	reviews := h.e.Must(h.rev, "list_reviews", nil)
	findings := reviews["scope_findings"].(map[string]any)[id].([]any)
	if len(findings) != 4 || len(reviews["tasks"].([]any)) != 1 {
		t.Fatalf("list_reviews: %v", reviews)
	}

	h.e.Must(h.rev, "review", map[string]any{"task_id": id, "verdict": "approve", "comments": "ok"})
	h.sync()
	if s := h.status(id); s["merge_ready"] != false || s["scope"] == nil {
		t.Fatalf("status with error findings: %v", s)
	}
	if _, ok := h.mergeReady(id); ok {
		t.Fatal("merge-ready decision offered for a task with error findings")
	}

	if code, out := h.e.REST("PATCH", "/api/v1/tasks/"+id, map[string]any{"budget": map[string]any{"max_minutes": 1}}); code != 200 {
		t.Fatalf("set budget: %d %v", code, out)
	}
	gh.set("o/gh", 1, func(p *fakePR) {
		p.Head, p.Commits, p.Ref = "b111111111", []string{"b111111111"}, branch
		p.Authors = map[string]string{"b111111111": "W1"}
		p.Files = []map[string]any{{"filename": "internal/api/a.go", "additions": 300, "deletions": 0}}
	})
	h.sync()
	code, out = h.scope(id)
	if code != 200 || out["merge_eligible"] != true || out["head_sha"] != "b111111111" {
		t.Fatalf("scope after fix: %d %v", code, out)
	}
	if rules := scopeRules(out); len(rules) != 1 || rules["diff_too_large"] != "warning" {
		t.Fatalf("rules after fix: %v", rules)
	}
	d, ok := h.mergeReady(id)
	if !ok || !strings.Contains(d["summary"].(string), "scope: 300 changed lines") {
		t.Fatalf("merge-ready decision: %v", d)
	}
	if _, list := h.e.REST("GET", "/api/v1/prs", nil); list["prs"].([]any)[0].(map[string]any)["diff"] != nil {
		t.Fatalf("pr list carries the file list: %v", list)
	}
}

func TestScopePaginatesAndCapsFiles(t *testing.T) {
	gh, fj := newFakeHost(t, prsync.KindGitHub), newFakeHost(t, prsync.KindForgejo)
	h := newHarness(t, gh, fj, "", `{"per_minute": 6000, "burst": 1000}`)
	ids := h.setup(2)
	names := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("pkg/f%04d.go", i)
		}
		return out
	}
	gh.set("o/gh", 1, func(p *fakePR) { p.Files = fakeFiles(names(120)...) })
	gh.set("o/gh", 2, func(p *fakePR) { p.Files = fakeFiles(names(3100)...) })
	h.submit(ids[0], ghURL(1))
	h.submit(ids[1], ghURL(2))
	h.sync()

	_, out := h.scope(ids[0])
	if out["files"] != 120.0 || out["additions"] != 1200.0 || out["truncated"] != false {
		t.Fatalf("paginated scope: %v", out)
	}
	_, out = h.scope(ids[1])
	if out["files"] != 3000.0 || out["truncated"] != true || scopeRules(out)["diff_truncated"] != "warning" {
		t.Fatalf("capped scope: %v", out)
	}
}
