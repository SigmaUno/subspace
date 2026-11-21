package activity

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

func str(s string) *string { return &s }

func audit(agent, role, tool, task, note string, before, after map[string]any) *model.AuditEvent {
	e := &model.AuditEvent{ID: 7, TS: time.Unix(1700000000, 0).UTC(), AgentID: agent, Role: role, Tool: tool, Before: before, After: after}
	if task != "" {
		e.TaskID = str(task)
	}
	if note != "" {
		e.Note = str(note)
	}
	return e
}

type m = map[string]any

func TestFromAudit(t *testing.T) {
	cases := []struct {
		name     string
		ev       *model.AuditEvent
		text     string
		severity string
		icon     string
		targets  []Ref
	}{
		{"submit", audit("claude-3", "worker", "submit", "T-12", "PR https://github.com/o/r/pull/45", m{"status": "in_progress"}, m{"status": "submitted"}),
			"claude-3 submitted T-12 (PR #45)", SevSuccess, "send", []Ref{{"task", "T-12"}}},
		{"submit without number", audit("claude-3", "worker", "submit", "T-12", "PR https://example.com/x", nil, m{"status": "submitted"}),
			"claude-3 submitted T-12", SevSuccess, "send", []Ref{{"task", "T-12"}}},
		{"propose", audit("rex", "leader", "propose_plan", "", "", nil, m{"plan_id": "P-3", "tasks": float64(14)}),
			"rex proposed plan P-3 with 14 tasks", SevInfo, "plan", []Ref{{"plan", "P-3"}}},
		{"approve", audit("owner", "owner", "approve_plan", "", "", nil, m{"plan_id": "P-3", "status": "approved"}),
			"owner approved plan P-3", SevSuccess, "check", []Ref{{"plan", "P-3"}}},
		{"approve via root token", audit("owner", "owner", "approve_plan", "", "token=root", nil, m{"plan_id": "P-3", "status": "approved"}),
			"owner approved plan P-3", SevSuccess, "check", []Ref{{"plan", "P-3"}}},
		{"reject via api token", audit("owner", "owner", "reject_plan", "", "token=K-4; too big", nil, m{"plan_id": "P-3", "status": "rejected"}),
			"owner rejected plan P-3: too big", SevWarning, "x", []Ref{{"plan", "P-3"}}},
		{"review changes", audit("codex-r", "reviewer", "review", "T-2", "tighten the loop", nil, m{"status": "changes_requested"}),
			"codex-r requested changes on T-2: tighten the loop", SevWarning, "comment", []Ref{{"task", "T-2"}}},
		{"review approved", audit("codex-r", "reviewer", "review", "T-2", "", nil, m{"status": "approved"}),
			"codex-r approved T-2", SevSuccess, "comment", []Ref{{"task", "T-2"}}},
		{"review escalated", audit("codex-r", "reviewer", "review", "T-2", "", nil, m{"status": "needs_owner"}),
			"codex-r escalated T-2 to the owner after repeated reviews", SevWarning, "comment", []Ref{{"task", "T-2"}}},
		{"tests failing", audit("t1", "tester", "report_tests", "T-4", "", nil, m{"passed": float64(10), "failed": float64(2)}),
			"t1 reported tests on T-4: 10 passed, 2 failed", SevWarning, "check", []Ref{{"task", "T-4"}}},
		{"tests passing", audit("t1", "tester", "report_tests", "T-4", "", nil, m{"passed": 10, "failed": 0}),
			"t1 reported tests on T-4: 10 passed, 0 failed", SevInfo, "check", []Ref{{"task", "T-4"}}},
		{"lease expiry", audit("system", "system", "lease_release", "T-9", "lease expired", nil, m{"status": "assigned"}),
			"system released the lease on T-9: lease expired", SevWarning, "pause", []Ref{{"task", "T-9"}}},
		{"close task", audit("owner", "owner", "close_brief", "T-1", "brief closed", nil, m{"status": "cancelled"}),
			"owner closed the brief: T-1 is now cancelled", SevWarning, "folder", []Ref{{"task", "T-1"}}},
		{"close brief", audit("owner", "owner", "close_brief", "", "", nil, m{"brief_id": "B-1", "cancelled": 3, "blocked": 1}),
			"owner closed brief B-1 (3 cancelled, 1 blocked)", SevWarning, "folder", []Ref{{"brief", "B-1"}}},
		{"split", audit("rex", "leader", "accept_split", "T-5", "", nil, m{"request_id": "HR-2", "created": []any{"T-8", "T-9"}}),
			"rex accepted split HR-2 of T-5 into T-8, T-9", SevInfo, "split", []Ref{{"task", "T-5"}, {"help_request", "HR-2"}, {"task", "T-8"}, {"task", "T-9"}}},
		{"revoke", audit("owner", "owner", "revoke_agent", "", "", m{"agent": "w1", "status": "active"}, m{"status": "revoked"}),
			"owner revoked agent w1", SevWarning, "x", []Ref{{"agent", "w1"}}},
		{"stop spawn failed", audit("system", "system", "stop_spawn", "", "", m{"spawn_id": "S-1"}, m{"spawn_id": "S-1", "status": "failed", "reason": "connect_timeout"}),
			"system stopped spawn S-1 (connect_timeout)", SevError, "stop", []Ref{{"spawn", "S-1"}}},
		{"unknown tool", audit("claude-1", "worker", "frobnicate", "T-3", "", nil, nil),
			"claude-1 used frobnicate on T-3", SevInfo, "activity", []Ref{{"task", "T-3"}}},
		{"unknown tool without task", audit("owner", "owner", "frobnicate", "", "", nil, nil),
			"owner used frobnicate", SevInfo, "activity", []Ref{}},
		{"missing fields", audit("rex", "leader", "propose_plan", "", "", nil, nil),
			"rex proposed plan with tasks", SevInfo, "plan", []Ref{}},
		{"secret in note", audit("owner", "owner", "cancel", "T-1", "token ghp_"+"abcdefghijklmnopqrstuvwxyz0123456789 leaked", nil, m{"status": "cancelled"}),
			"", SevWarning, "x", []Ref{{"task", "T-1"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			it := FromAudit(c.ev)
			if c.text != "" && it.Text != c.text {
				t.Errorf("text = %q, want %q", it.Text, c.text)
			}
			if c.text == "" && strings.Contains(it.Text, "abcdefghijklmnopqrstuvwxyz0123456789") {
				t.Errorf("secret leaked: %q", it.Text)
			}
			if it.Severity != c.severity || it.Icon != c.icon {
				t.Errorf("severity/icon = %s/%s, want %s/%s", it.Severity, it.Icon, c.severity, c.icon)
			}
			if !slices.Equal(it.Targets, c.targets) {
				t.Errorf("targets = %v, want %v", it.Targets, c.targets)
			}
			if it.ID != "a:7" || it.Source != SourceAudit || it.Tool != c.ev.Tool {
				t.Errorf("identity = %+v", it)
			}
		})
	}
}

func TestFromAuditActors(t *testing.T) {
	for role, want := range map[string]Ref{
		"owner":  {"owner", "owner"},
		"system": {"system", "w1"},
		"worker": {"agent", "w1"},
	} {
		if got := FromAudit(audit("w1", role, "claim_task", "T-1", "", nil, nil)).Actor; got != want {
			t.Errorf("role %s actor = %v, want %v", role, got, want)
		}
	}
}

func TestFromMessage(t *testing.T) {
	cases := []struct {
		name     string
		msg      model.Message
		text     string
		severity string
		targets  []Ref
	}{
		{"verb body gets actor", model.Message{ID: 3, ThreadKind: "task", ThreadID: "T-1", AuthorID: "rex", AuthorRole: "leader", Kind: "system", Body: "reassigned to w2"},
			"rex reassigned to w2", SevInfo, []Ref{{"task", "T-1"}}},
		{"sentence body kept", model.Message{ID: 4, ThreadKind: "agent", ThreadID: "w1", AuthorID: "system", AuthorRole: "system", Kind: "system", Body: "Agent paused: loop"},
			"Agent paused: loop", SevWarning, []Ref{{"agent", "w1"}}},
		{"brief thread", model.Message{ID: 5, ThreadKind: "brief", ThreadID: "B-1", AuthorID: "owner", AuthorRole: "owner", Kind: "system", Body: "Plan P-1 approved"},
			"Plan P-1 approved", SevInfo, []Ref{{"brief", "B-1"}}},
		{"failure", model.Message{ID: 6, ThreadKind: "task", ThreadID: "T-2", AuthorID: "prsync", AuthorRole: "system", Kind: "system", Body: "checks failed on the PR"},
			"prsync checks failed on the PR", SevError, []Ref{{"task", "T-2"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.msg.CreatedAt = time.Unix(1700000000, 0).UTC()
			it := FromMessage(&c.msg)
			if it.Text != c.text || it.Severity != c.severity || !slices.Equal(it.Targets, c.targets) {
				t.Errorf("got %q %s %v, want %q %s %v", it.Text, it.Severity, it.Targets, c.text, c.severity, c.targets)
			}
			if it.Source != SourceMessage || it.ID != "m:"+strconv.FormatInt(c.msg.ID, 10) {
				t.Errorf("identity = %+v", it)
			}
		})
	}
}

func TestEveryAuditedToolHasTemplate(t *testing.T) {
	tools := auditedTools(t)
	if len(tools) < 40 {
		t.Fatalf("found only %d audited tools, scanner is broken", len(tools))
	}
	for _, tool := range tools {
		r, ok := rules[tool]
		if !ok {
			t.Errorf("audited tool %q has no template in rules.go", tool)
			continue
		}
		if r.text == "" || r.icon == "" {
			t.Errorf("template for %q needs text and icon", tool)
		}
	}
}

func auditedTools(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	var files []*ast.File
	for _, root := range []string{"..", "../../cmd"} {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			slash := filepath.ToSlash(p)
			if d.IsDir() {
				switch d.Name() {
				case "testdata", "client", "storetest", "loadtest", "metrics", "activity":
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(slash, ".go") || strings.HasSuffix(slash, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				return err
			}
			files = append(files, f)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	toolParam := map[string][]int{}
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			i := 0
			for _, p := range fd.Type.Params.List {
				for _, n := range p.Names {
					if n.Name == "tool" {
						toolParam[fd.Name.Name] = append(toolParam[fd.Name.Name], i)
					}
					i++
				}
				if len(p.Names) == 0 {
					i++
				}
			}
		}
	}
	found := map[string]bool{}
	literal := func(e ast.Expr) {
		if b, ok := e.(*ast.BasicLit); ok && b.Kind == token.STRING {
			if s, err := strconv.Unquote(b.Value); err == nil {
				found[s] = true
			}
		}
	}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				var name string
				switch fn := x.Fun.(type) {
				case *ast.Ident:
					name = fn.Name
				case *ast.SelectorExpr:
					name = fn.Sel.Name
				}
				for _, i := range toolParam[name] {
					if i < len(x.Args) {
						literal(x.Args[i])
					}
				}
			case *ast.AssignStmt:
				if len(x.Lhs) != len(x.Rhs) {
					return true
				}
				for i, l := range x.Lhs {
					if id, ok := l.(*ast.Ident); ok && id.Name == "tool" {
						literal(x.Rhs[i])
					}
				}
			case *ast.CompositeLit:
				var typ string
				switch ty := x.Type.(type) {
				case *ast.Ident:
					typ = ty.Name
				case *ast.SelectorExpr:
					typ = ty.Sel.Name
				}
				if typ != "AuditEvent" {
					return true
				}
				for _, el := range x.Elts {
					if kv, ok := el.(*ast.KeyValueExpr); ok {
						if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Tool" {
							literal(kv.Value)
						}
					}
				}
			}
			return true
		})
	}
	out := make([]string, 0, len(found))
	for k := range found {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
