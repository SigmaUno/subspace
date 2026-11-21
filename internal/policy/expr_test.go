package policy

import (
	"strings"
	"testing"
	"time"
)

var exprFacts = mapFacts{
	"task.title":      Str("[Security] Rotate #infra keys"),
	"task.repo":       Str("SigmaUno/subspace"),
	"task.paths":      Strs([]string{"internal/api/**", "deploy/k8s/app.yaml"}),
	"task.labels":     Strs([]string{"security", "infra"}),
	"task.priority":   Num(3),
	"task.has_tester": Bool(true),
	"brief.repos":     Strs([]string{"SigmaUno/subspace", "SigmaUno/web"}),
	"actor.provider":  Str("anthropic"),
	"cost.task_used":  Num(2.5),
}

func TestExprTable(t *testing.T) {
	cases := []struct {
		src  string
		want bool
		err  string
	}{
		{src: ``, want: true},
		{src: `true`, want: true},
		{src: `not true`, want: false},
		{src: `!(1 < 2)`, want: false},
		{src: `task.priority >= 3 && task.priority < 4`, want: true},
		{src: `task.priority == -3`, want: false},
		{src: `cost.task_used > 2.25 and cost.task_used <= 2.5`, want: true},
		{src: `"security" in task.labels`, want: true},
		{src: `"ops" not in task.labels`, want: true},
		{src: `task.priority in [1, 2, 3]`, want: true},
		{src: `actor.provider in ["openai", "deepseek"]`, want: false},
		{src: `"Uno" in task.repo`, want: true},
		{src: `"x" in []`, want: false},
		{src: `any_in(task.labels, ["infra", "web"])`, want: true},
		{src: `glob(task.paths, "deploy/**")`, want: true},
		{src: `glob(task.repo, "SigmaUno/*")`, want: true},
		{src: `glob(task.repo, "*/web")`, want: false},
		{src: `overlaps(task.paths, "internal/api/handlers")`, want: true},
		{src: `overlaps(task.paths, "web/**")`, want: false},
		{src: `has_prefix(task.repo, "SigmaUno/")`, want: true},
		{src: `has_suffix(brief.repos, "/web")`, want: true},
		{src: `regex(task.title, "^\\[(?i:security)\\]")`, want: true},
		{src: `contains(lower(task.title), "rotate")`, want: true},
		{src: `len(task.labels) == 2 && len(task.repo) > 5`, want: true},
		{src: `task.has_tester == true || task.unknown_thing`, err: "unknown fact"},
		{src: `task.priority == "3"`, err: "cannot compare"},
		{src: `task.labels == task.labels`, err: "cannot compare lists"},
		{src: `task.priority`, err: "must be a bool"},
		{src: `1 in task.labels`, err: "not supported"},
		{src: `glob(task.repo, task.repo)`, err: "string literal pattern"},
		{src: `glob(task.repo, "a**b")`, err: "whole path segment"},
		{src: `overlaps(task.paths, "/etc")`, err: "relative"},
		{src: `regex(task.title, "(")`, err: "invalid regex"},
		{src: `regex(task.title, "` + strings.Repeat("a", MaxRegex+1) + `")`, err: "longer than"},
		{src: `exec("rm")`, err: "unknown function"},
		{src: `[1, "a"]`, err: "mixes"},
		{src: `true &&`, err: "unexpected end"},
		{src: `(true`, err: "expected )"},
		{src: `"unterminated`, err: "unterminated"},
		{src: `a = b`, err: "unexpected character"},
		{src: strings.Repeat("(", MaxDepth+1) + "true" + strings.Repeat(")", MaxDepth+1), err: "nests deeper"},
		{src: strings.Repeat("true&&", MaxNodes) + "true", err: "more than"},
		{src: strings.Repeat(" ", MaxSource+1), err: "longer than"},
		{src: `task.paths..x == ""`, err: "invalid name"},
	}
	for _, c := range cases {
		e, err := Compile(c.src, lookupFact)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%q: error %v, want %q", c.src, err, c.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.src, err)
			continue
		}
		got, _, err := e.Eval(exprFacts)
		if err != nil || got != c.want {
			t.Errorf("%q = %v (%v), want %v", c.src, got, err, c.want)
		}
	}
}

func TestExprStepLimit(t *testing.T) {
	long := make([]string, MaxListLength)
	for i := range long {
		long[i] = strings.Repeat("a/", 100) + "b"
	}
	facts := mapFacts{"task.paths": Strs(long), "brief.repos": Strs(long)}
	src := strings.Repeat(`any_in(task.paths, brief.repos) && `, 20) + `glob(task.paths, "**/c")`
	e, err := Compile(src, lookupFact)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, steps, err := e.Eval(facts)
	if err != ErrSteps || steps <= MaxSteps {
		t.Fatalf("err %v after %d steps, want the step limit", err, steps)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("evaluation took %s", d)
	}
}

func FuzzExpr(f *testing.F) {
	for _, s := range []string{
		`"security" in task.labels && subject.provider != "anthropic"`,
		`time.weekday in ["sat", "sun"] && target.tester_tasks == 0`,
		`overlaps(task.paths, "deploy/**") or glob(task.repo, "*/infra-*")`,
		`regex(task.title, "^(fix|feat)(\\(.+\\))?: ") and not task.has_tester`,
		`len(task.labels) > 2 || cost.component_used >= 10.5 && count.in_progress_provider < -1`,
		`any_in(brief.repos, ["a/b"]) && contains(lower(task.title), "x") && has_prefix(task.paths, "web/")`,
		`((((true))))`,
	} {
		f.Add(s, "deploy/**", "deploy/k8s/x.yaml")
	}
	f.Fuzz(func(t *testing.T, src, pattern, s string) {
		if len(pattern) <= maxString && len(s) <= maxFactString {
			_ = Glob(pattern, s)
		}
		e, err := Compile(src, lookupFact)
		if err != nil {
			return
		}
		facts := mapFacts{
			"task.title": Str(s), "task.repo": Str(pattern), "task.paths": Strs([]string{s, pattern}),
			"task.labels": Strs([]string{s}), "brief.repos": Strs([]string{pattern}), "time.weekday": Str("sat"),
		}
		start := time.Now()
		_, steps, err := e.Eval(facts)
		if err == nil && steps > MaxSteps {
			t.Fatalf("%d steps without error", steps)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("evaluation of %q took %s", src, d)
		}
		e.Constant()
	})
}
