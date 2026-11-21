package briefs_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/briefs"
	"github.com/SigmaUno/subspace/internal/components"
)

func TestParseAcceptsSubset(t *testing.T) {
	src := "---\n" +
		"# leading comment\n" +
		"title: 'It''s a \"brief\"' # trailing comment\n" +
		"repos:\n" +
		"  - SigmaUno/subspace\n" +
		"  - \"x/y\"\n" +
		"max_cost_eur: 12.5\n" +
		"max_parallel_agents: 4\n" +
		"allowed_providers: [anthropic, \"deep,seek\" ]\n" +
		"cross_review: false\n" +
		"risky_paths: [internal/auth/**]\n" +
		"task_budget:\n" +
		"    max_minutes: 90\n" +
		"    max_cost_eur: 3\n" +
		"components:\n" +
		"- name: api\n" +
		"  description: \"REST: handlers\\n\\\"v1\\\"\"\n" +
		"  paths: [internal/api]\n" +
		"  depends_on:\n" +
		"    - store\n" +
		"-   name: store\n" +
		"    paths: []\n" +
		"---\n\n# Body\n\nText with --- and key: value.\n"
	d, err := briefs.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := &briefs.Doc{
		Title: `It's a "brief"`, Body: "# Body\n\nText with --- and key: value.",
		Repos: []string{"SigmaUno/subspace", "x/y"}, MaxCostEUR: 12.5, MaxParallel: 4,
		AllowedProviders: []string{"anthropic", "deep,seek"}, CrossReview: false, RiskyPaths: []string{"internal/auth/**"},
		TaskBudget: &briefs.TaskBudget{MaxMinutes: 90, MaxCostEUR: 3},
		Components: []components.Spec{
			{Name: "api", Description: "REST: handlers\n\"v1\"", Paths: []string{"internal/api"}, DependsOn: []string{"store"}},
			{Name: "store", Paths: []string{}, DependsOn: []string{}},
		},
	}
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("got  %+v\nwant %+v", d, want)
	}
}

func TestParseDefaultsAndCRLF(t *testing.T) {
	d, err := briefs.Parse([]byte("---\r\ntitle: Minimal\r\n---\r\nbody\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "Minimal" || d.Body != "body" || d.MaxParallel != 10 || !d.CrossReview || d.TaskBudget != nil ||
		len(d.Repos) != 0 || len(d.Components) != 0 {
		t.Fatalf("%+v", d)
	}
}

func TestParseCompactListBeforeNextKey(t *testing.T) {
	d, err := briefs.Parse([]byte("---\nrepos:\n- a\n- b\ntitle: x\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d.Repos, []string{"a", "b"}) || d.Title != "x" {
		t.Fatalf("%+v", d)
	}
}

func TestParseErrors(t *testing.T) {
	deep := "---\ntitle: x\ncomponents:\n"
	for i := 1; i <= 10; i++ {
		deep += strings.Repeat("  ", i) + "-\n"
	}
	deep += strings.Repeat("  ", 11) + "- a\n---\n"

	manyItems := "---\ntitle: x\nrepos:\n" + strings.Repeat("  - r\n", 1001) + "---\n"
	manyFlow := "---\ntitle: x\nrepos: [" + strings.Repeat("a,", 1000) + "a]\n---\n"

	cases := []struct {
		name      string
		src       string
		line, col int
		msg       string
	}{
		{"empty", "", 1, 1, `must start with a "---"`},
		{"no frontmatter", "# just markdown\n", 1, 1, `must start with a "---"`},
		{"unclosed", "---\ntitle: x\nbody\n", 1, 1, "not closed"},
		{"huge", "---\ntitle: x\n---\n" + strings.Repeat("a", briefs.MaxDocumentBytes), 1, 1, "larger than"},
		{"long frontmatter", "---\n" + strings.Repeat("# c\n", 6000) + "---\n", 5002, 1, "longer than"},
		{"invalid utf8", "---\ntitle: a\xffb\n---\n", 2, 9, "not valid UTF-8"},
		{"nul", "---\ntitle: x\n---\nab\x00", 4, 3, "NUL"},
		{"tab", "---\ntitle:\tx\n---\n", 2, 7, "tabs"},
		{"control", "---\ntitle: a\rb\n---\n", 2, 9, "control character"},
		{"missing title", "---\nrepos: []\n---\n", 2, 1, "title is required"},
		{"empty frontmatter", "---\n---\n", 2, 1, "title is required"},
		{"blank title", "---\ntitle: \"  \"\n---\n", 2, 8, "must not be empty"},
		{"duplicate key", "---\ntitle: a\nrepos: []\ntitle: b\n---\n", 4, 1, `duplicate key "title" (first defined on line 2)`},
		{"duplicate nested key", "---\ntitle: a\ntask_budget:\n  max_minutes: 1\n  max_minutes: 2\n---\n", 5, 3, "duplicate key"},
		{"unknown key", "---\ntitle: a\nowner: me\n---\n", 3, 1, `unknown key "owner"`},
		{"invalid key", "---\nTitle: a\n---\n", 2, 1, "invalid key"},
		{"garbage key", "---\n" + strings.Repeat("{[", 100) + ": x\n---\n", 2, 1, "invalid key"},
		{"no colon", "---\ntitle a\n---\n", 2, 1, `expected "key: value"`},
		{"no space after colon", "---\ntitle:a\n---\n", 2, 7, "space after"},
		{"root list", "---\n- a\n---\n", 2, 1, "mapping of keys"},
		{"root indented", "---\n  title: a\n---\n", 2, 3, "unexpected indentation"},
		{"over-indented sibling", "---\ntitle: a\n  repos: []\n---\n", 3, 3, "unexpected indentation"},
		{"missing value", "---\ntitle:\nrepos: []\n---\n", 2, 7, `missing value for "title"`},
		{"missing value at end", "---\ntitle: x\nrepos:\n---\n", 3, 7, "missing value"},
		{"mixed list and map", "---\ntitle: x\nrepos:\n  - a\n  b: c\n---\n", 5, 3, "expected a list item"},
		{"item in map", "---\ntitle: x\ntask_budget:\n  max_minutes: 1\n  - a\n---\n", 5, 3, "unexpected list item"},
		{"dash garbage", "---\ntitle: x\nrepos:\n  - - - a\n---\n", 4, 5, "cannot start with"},
		{"empty item", "---\ntitle: x\nrepos:\n  -\n  - a\n---\n", 4, 4, "missing value for list item"},
		{"unterminated double", "---\ntitle: \"abc\n---\n", 2, 8, "unterminated string"},
		{"unterminated single", "---\ntitle: 'abc\n---\n", 2, 8, "unterminated string"},
		{"bad escape", "---\ntitle: \"a\\qb\"\n---\n", 2, 10, `unsupported escape sequence \q`},
		{"text after quote", "---\ntitle: \"a\"b\n---\n", 2, 11, "unexpected text after value"},
		{"comment without space", "---\ntitle: \"a\"#b\n---\n", 2, 11, "unexpected text after value"},
		{"plain colon", "---\ntitle: Fix: lease\n---\n", 2, 11, `contains ": "`},
		{"plain trailing colon", "---\ntitle: Fix:\n---\n", 2, 11, `ends with ":"`},
		{"flow map", "---\ntitle: {a: b}\n---\n", 2, 8, "flow mappings"},
		{"block scalar", "---\ntitle: |\n  text\n---\n", 2, 8, "block scalars"},
		{"anchor", "---\ntitle: &a x\n---\n", 2, 8, "anchors"},
		{"alias", "---\ntitle: *a\n---\n", 2, 8, "anchors"},
		{"tag", "---\ntitle: !!str x\n---\n", 2, 8, "anchors"},
		{"dash value", "---\ntitle: - x\n---\n", 2, 8, "cannot start with"},
		{"unterminated flow", "---\ntitle: x\nrepos: [a, b\n---\n", 3, 8, "unterminated list"},
		{"nested flow", "---\ntitle: x\nrepos: [a, [b]]\n---\n", 3, 12, "nested collections"},
		{"nested flow map", "---\ntitle: x\nrepos: [a, {b: c}]\n---\n", 3, 12, "nested collections"},
		{"empty flow item", "---\ntitle: x\nrepos: [a, , b]\n---\n", 3, 12, "empty list item"},
		{"trailing comma", "---\ntitle: x\nrepos: [a,]\n---\n", 3, 11, "empty list item"},
		{"flow comment", "---\ntitle: x\nrepos: [a #c]\n---\n", 3, 11, "comments are not allowed"},
		{"flow garbage after", "---\ntitle: x\nrepos: [a] b\n---\n", 3, 11, "unexpected text"},
		{"flow missing comma", "---\ntitle: x\nrepos: [\"a\" \"b\"]\n---\n", 3, 13, `expected "," or "]"`},
		{"too deep", deep, 10, 15, "nesting is deeper"},
		{"too many items", manyItems, 1004, 3, "more than 1000 items"},
		{"too many flow items", manyFlow, 3, 2009, "more than 1000 items"},
		{"title list", "---\ntitle: [a]\n---\n", 2, 8, "title must be a string, not a list"},
		{"repos scalar", "---\ntitle: x\nrepos: a\n---\n", 3, 8, "repos must be a list"},
		{"duplicate repo", "---\ntitle: x\nrepos: [a, b, a]\n---\n", 3, 15, `duplicate repos item "a"`},
		{"nested repo list", "---\ntitle: x\nrepos:\n  -\n    - a\n---\n", 5, 5, "repos item must be a string"},
		{"negative cost", "---\ntitle: x\nmax_cost_eur: -1\n---\n", 3, 15, "non-negative number"},
		{"quoted cost", "---\ntitle: x\nmax_cost_eur: \"5\"\n---\n", 3, 15, "non-negative number"},
		{"nan cost", "---\ntitle: x\nmax_cost_eur: NaN\n---\n", 3, 15, "non-negative number"},
		{"inf cost", "---\ntitle: x\nmax_cost_eur: .inf\n---\n", 3, 15, "non-negative number"},
		{"hex cost", "---\ntitle: x\nmax_cost_eur: 0x10\n---\n", 3, 15, "non-negative number"},
		{"float parallel", "---\ntitle: x\nmax_parallel_agents: 2.5\n---\n", 3, 22, "non-negative integer"},
		{"huge parallel", "---\ntitle: x\nmax_parallel_agents: 99999999999999999999\n---\n", 3, 22, "non-negative integer"},
		{"yes bool", "---\ntitle: x\ncross_review: yes\n---\n", 3, 15, "true or false"},
		{"budget scalar", "---\ntitle: x\ntask_budget: 5\n---\n", 3, 14, "task_budget must be a mapping"},
		{"budget unknown", "---\ntitle: x\ntask_budget:\n  hours: 1\n---\n", 4, 3, `unknown key "hours" in task_budget`},
		{"component scalar", "---\ntitle: x\ncomponents: [api]\n---\n", 3, 14, "component must be a mapping"},
		{"component no name", "---\ntitle: x\ncomponents:\n  - description: d\n---\n", 4, 5, "component name is required"},
		{"component unknown key", "---\ntitle: x\ncomponents:\n  - name: a\n    owner: b\n---\n", 5, 5, `unknown key "owner" in component`},
		{"component duplicate", "---\ntitle: x\ncomponents:\n  - name: a\n  - name: A\n---\n", 3, 1, "duplicate component name"},
		{"component cycle", "---\ntitle: x\ncomponents:\n  - name: a\n    depends_on: [b]\n  - name: b\n    depends_on: [a]\n---\n", 3, 1, "dependency cycle"},
		{"component escaping path", "---\ntitle: x\ncomponents:\n  - name: a\n    paths: [../etc]\n---\n", 3, 1, "escapes the repository"},
		{"empty components", "---\ntitle: x\ncomponents: []\n---\n", 3, 1, "component plan is empty"},
		{"unicode column", "---\ntitle: \"héllo\" x\n---\n", 2, 15, "unexpected text"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := briefs.Parse([]byte(c.src))
			var pe *briefs.ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("want ParseError, got %v", err)
			}
			if pe.Line != c.line || pe.Col != c.col || !strings.Contains(pe.Msg, c.msg) {
				t.Fatalf("got %d:%d %q, want %d:%d containing %q", pe.Line, pe.Col, pe.Msg, c.line, c.col, c.msg)
			}
		})
	}
}
