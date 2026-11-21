package prompts_test

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/prompts"
)

func writeTemplate(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "t.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBuiltinTaskTemplates(t *testing.T) {
	lib := prompts.New("")
	list, err := lib.TaskTemplates()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, tt := range list {
		ids = append(ids, tt.ID)
	}
	want := []string{"add-feature-with-tests", "fix-bug-with-regression-test", "perf-hillclimb-task", "refactor-keep-behavior", "security-fix", "write-integration-tests"}
	if !slices.Equal(ids, want) {
		t.Fatalf("task templates %v want %v", ids, want)
	}
	for _, tt := range list {
		vars := map[string]string{}
		for _, v := range tt.Variables {
			vars[v] = "x-" + v
		}
		got, err := lib.RenderTask(tt.ID, vars)
		if err != nil {
			t.Fatalf("%s: %v", tt.ID, err)
		}
		if strings.Contains(got.Title+got.Spec+strings.Join(got.AcceptanceCriteria, ""), "{{") {
			t.Errorf("%s: unrendered placeholder: %+v", tt.ID, got)
		}
		if len(got.AcceptanceCriteria) == 0 || got.Budget.MaxMinutes == 0 || got.Budget.MaxCostEUR == 0 {
			t.Errorf("%s: missing defaults: %+v", tt.ID, got)
		}
	}
	got, err := lib.RenderTask("write-integration-tests", map[string]string{"subject": "plans", "scope": "approve and reject"})
	if err != nil || got.Role != model.RoleTester || got.Title != "Write integration tests for plans" {
		t.Fatalf("tester template: %+v %v", got, err)
	}
}

func TestRenderTask(t *testing.T) {
	dir := t.TempDir()
	writeTemplate(t, dir, "---\nid: demo\ntitle: Demo\nvariables: [name, detail]\nkind: task\ntask_title: Do {{name}}\nbudget_minutes: 30\nacceptance_criteria:\n  - \"{{name}} works, really\"\n  - Detail {{ detail }}\n---\nSpec for {{name}}: {{detail}}\n")
	lib := prompts.New(dir)
	long := strings.Repeat("a", 4001)
	tests := []struct {
		name    string
		id      string
		vars    map[string]string
		title   string
		spec    string
		crit    []string
		wantErr string
		code    string
	}{
		{name: "renders", id: "demo", vars: map[string]string{"name": "X", "detail": "d"},
			title: "Do X", spec: "Spec for X: d", crit: []string{"X works, really", "Detail d"}},
		{name: "title collapses line breaks", id: "demo", vars: map[string]string{"name": "A\nB\t C", "detail": "d"},
			title: "Do A B C", spec: "Spec for A\nB\t C: d", crit: []string{"A B C works, really", "Detail d"}},
		{name: "values are not expanded again", id: "demo", vars: map[string]string{"name": "{{detail}}", "detail": "d"},
			title: "Do {{detail}}", spec: "Spec for {{detail}}: d", crit: []string{"{{detail}} works, really", "Detail d"}},
		{name: "missing", id: "demo", vars: map[string]string{"name": "X"}, wantErr: "missing variables: detail (declared: name, detail)", code: "bad_request"},
		{name: "all missing", id: "demo", wantErr: "missing variables: name, detail", code: "bad_request"},
		{name: "blank counts as missing", id: "demo", vars: map[string]string{"name": " \n", "detail": "d"}, wantErr: "missing variables: name", code: "bad_request"},
		{name: "unknown variable", id: "demo", vars: map[string]string{"name": "X", "detail": "d", "typo": "1", "zzz": "2"}, wantErr: "unknown variables: typo, zzz", code: "bad_request"},
		{name: "too long value", id: "demo", vars: map[string]string{"name": long, "detail": "d"}, wantErr: "variable name exceeds", code: "bad_request"},
		{name: "control character", id: "demo", vars: map[string]string{"name": "a\x00b", "detail": "d"}, wantErr: "control character", code: "bad_request"},
		{name: "title too long", id: "demo", vars: map[string]string{"name": strings.Repeat("t", 201), "detail": "d"}, wantErr: "rendered title", code: "bad_request"},
		{name: "unknown template lists ids", id: "nope", wantErr: "available: add-feature-with-tests, demo, fix-bug", code: "not_found"},
		{name: "prompt is not a task template", id: "make-10x-faster", wantErr: "not found", code: "not_found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := lib.RenderTask(tc.id, tc.vars)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %v, want containing %q", err, tc.wantErr)
				}
				if c := model.AsError(err).Code; c != tc.code {
					t.Fatalf("code %q want %q", c, tc.code)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Title != tc.title || got.Spec != tc.spec || !slices.Equal(got.AcceptanceCriteria, tc.crit) {
				t.Fatalf("got %+v", got)
			}
			if got.Role != model.RoleWorker || got.Budget != (model.Budget{MaxMinutes: 30}) {
				t.Fatalf("defaults: role %q budget %+v", got.Role, got.Budget)
			}
		})
	}
}

func TestFrontMatterAdversarial(t *testing.T) {
	const task = "---\nid: demo\ntitle: Demo\nvariables: [a]\nkind: task\ntask_title: T {{a}}\n"
	tests := []struct {
		name    string
		file    string
		wantErr string
	}{
		{"task without task_title", "---\nid: demo\ntitle: Demo\nkind: task\n---\nbody\n", "needs task_title"},
		{"task keys on a prompt", "---\nid: demo\ntitle: Demo\nrole: worker\n---\nbody\n", "require kind: task"},
		{"explicit prompt kind with task key", "---\nid: demo\ntitle: Demo\nkind: prompt\ntask_title: x\n---\nbody\n", "require kind: task"},
		{"invalid kind", "---\nid: demo\ntitle: Demo\nkind: epic\n---\nbody\n", "invalid kind"},
		{"duplicate kind", "---\nid: demo\ntitle: Demo\nkind: task\nkind: prompt\n---\nbody\n", "duplicate frontmatter key: kind"},
		{"duplicate task_title", task + "task_title: other\n---\nbody\n", "duplicate frontmatter key: task_title"},
		{"duplicate acceptance_criteria", task + "acceptance_criteria:\n  - x\nacceptance_criteria:\n  - y\n---\nbody\n", "duplicate frontmatter key: acceptance_criteria"},
		{"invalid role", task + "role: reviewer\n---\nbody\n", "role must be"},
		{"negative minutes", task + "budget_minutes: -5\n---\nbody\n", "invalid budget_minutes"},
		{"fractional minutes", task + "budget_minutes: 1.5\n---\nbody\n", "invalid budget_minutes"},
		{"huge minutes", task + "budget_minutes: 99999999999999999999999\n---\nbody\n", "invalid budget_minutes"},
		{"NaN cost", task + "budget_cost_eur: NaN\n---\nbody\n", "invalid budget_cost_eur"},
		{"Inf cost", task + "budget_cost_eur: Inf\n---\nbody\n", "invalid budget_cost_eur"},
		{"hex cost", task + "budget_cost_eur: 0x10\n---\nbody\n", "invalid budget_cost_eur"},
		{"exponent cost", task + "budget_cost_eur: 1e999\n---\nbody\n", "invalid budget_cost_eur"},
		{"overflow cost", task + "budget_cost_eur: 1" + strings.Repeat("0", 400) + "\n---\nbody\n", "invalid budget_cost_eur"},
		{"empty budget value", task + "budget_cost_eur:\n---\nbody\n", "invalid budget_cost_eur"},
		{"inline acceptance", task + "acceptance_criteria: [a, b]\n---\nbody\n", "must be a list"},
		{"empty acceptance list", task + "acceptance_criteria:\n---\nbody\n", "acceptance_criteria is empty"},
		{"list item without a list", task + "- stray\n---\nbody\n", "outside a list"},
		{"list item after another key", task + "acceptance_criteria:\n  - ok\nrole: worker\n  - late\n---\nbody\n", "outside a list"},
		{"list item under title", "---\nid: demo\ntitle: Demo\n- x\n---\nbody\n", "outside a list"},
		{"empty list item", task + "acceptance_criteria:\n  -\n---\nbody\n", "empty list item"},
		{"blank quoted item", task + "acceptance_criteria:\n  - \"  \"\n---\nbody\n", "empty list item"},
		{"tab indentation", task + "acceptance_criteria:\n\t- x\n---\nbody\n", "tab indentation"},
		{"broken quote", task + "acceptance_criteria:\n  - \"open\n---\nbody\n", "invalid quoted list item"},
		{"control char via escape", task + "acceptance_criteria:\n  - \"a\\u0000b\"\n---\nbody\n", "control character"},
		{"too many items", task + "acceptance_criteria:\n" + strings.Repeat("  - x\n", 21) + "---\nbody\n", "more than 20"},
		{"item too long", task + "acceptance_criteria:\n  - " + strings.Repeat("x", 501) + "\n---\nbody\n", "longer than 500"},
		{"undeclared variable in criteria", task + "acceptance_criteria:\n  - uses {{ghost}}\n---\nbody\n", "undeclared variable ghost"},
		{"undeclared variable in task_title", "---\nid: demo\ntitle: Demo\nkind: task\ntask_title: T {{ghost}}\n---\nbody\n", "undeclared variable ghost"},
		{"control char in task_title", "---\nid: demo\ntitle: Demo\nkind: task\ntask_title: T\x01\n---\nbody\n", "control character in task_title"},
		{"unknown key still rejected", task + "priority: 5\n---\nbody\n", "unknown frontmatter key: priority"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeTemplate(t, dir, tc.file)
			_, err := prompts.New(dir).TaskTemplates()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestFrontMatterBackwardsCompatible(t *testing.T) {
	dir := t.TempDir()
	writeTemplate(t, dir, "---\r\nid: legacy\r\ntitle: Legacy: with colon\r\ntags: [a, b]\r\nvariables: [x]\r\n---\r\nDo {{x}}\r\n")
	lib := prompts.New(dir)
	got, err := lib.Get("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Legacy: with colon" || got.Kind != "" || got.Budget != nil || got.AcceptanceCriteria != nil || got.Body != "Do {{x}}" {
		t.Fatalf("legacy template: %+v", got)
	}
	tts, err := lib.TaskTemplates()
	if err != nil || slices.ContainsFunc(tts, func(t prompts.Template) bool { return t.ID == "legacy" }) {
		t.Fatalf("legacy listed as task template: %v %v", tts, err)
	}
	explicit := "---\nid: legacy\ntitle: L\nkind: prompt\n---\nbody\n"
	writeTemplate(t, dir, explicit)
	if got, err := lib.Get("legacy"); err != nil || got.Kind != "" {
		t.Fatalf("explicit prompt kind: %+v %v", got, err)
	}
}

func TestTaskTemplatesHandler(t *testing.T) {
	var out struct{ Templates []prompts.Template }
	if code := get(t, prompts.New("").TaskHandler(), "/task-templates", &out); code != http.StatusOK || len(out.Templates) != 6 {
		t.Fatalf("task templates: %d %d", code, len(out.Templates))
	}
	for _, tt := range out.Templates {
		if tt.Kind != prompts.KindTask || tt.TaskTitle == "" || tt.Budget == nil || len(tt.AcceptanceCriteria) == 0 {
			t.Errorf("incomplete template %+v", tt)
		}
	}
}
