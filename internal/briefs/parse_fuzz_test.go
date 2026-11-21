package briefs

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func FuzzParse(f *testing.F) {
	for _, t := range Templates() {
		f.Add([]byte(t.Markdown))
	}
	for _, s := range []string{
		"---\ntitle: x\n---\n",
		"---\r\ntitle: \"a\\nb\"\r\nrepos: [a/b, c/d]\r\n---\r\nbody",
		"---\ntitle: x\ncomponents:\n  - name: a\n    paths: [x]\n  - name: b\n    depends_on: [a]\n---\n",
		"---\ntitle: x\ntask_budget:\n  max_minutes: 5\n  max_cost_eur: 1.5\n---\n",
		"---\ntitle: x\ntitle: y\n---\n",
		"---\ntitle: &a x\n---\n",
		"---\ntitle: x\nrepos:\n- a\n- b\n---\n",
		"---\ntitle: 'it''s'\nmax_cost_eur: 1e3\n---\n",
		"---\ntitle: x\n",
		"title: x\n",
		"---\n\ttitle: x\n---\n",
		"",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		start := time.Now()
		d, err := Parse(src)
		if el := time.Since(start); el > 5*time.Second {
			t.Fatalf("Parse took %s on %d bytes", el, len(src))
		}
		if err != nil {
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("error %T is not a *ParseError: %v", err, err)
			}
			if pe.Line < 1 || pe.Col < 1 {
				t.Fatalf("position %d:%d out of range: %v", pe.Line, pe.Col, err)
			}
			return
		}
		if d.Title == "" || d.Title != strings.TrimSpace(d.Title) || !utf8.ValidString(d.Title) {
			t.Fatalf("bad title %q", d.Title)
		}
		if d.MaxParallel <= 0 || d.MaxCostEUR < 0 || d.Repos == nil || d.AllowedProviders == nil || d.Components == nil {
			t.Fatalf("bad document %+v", d)
		}
		if d.TaskBudget != nil && (d.TaskBudget.MaxMinutes < 0 || d.TaskBudget.MaxCostEUR < 0) {
			t.Fatalf("negative task budget %+v", d.TaskBudget)
		}
		crlf, err := Parse([]byte(strings.ReplaceAll(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n", "\r\n")))
		if err != nil || !reflect.DeepEqual(d, crlf) {
			t.Fatalf("CRLF parse differs: %v", err)
		}
		again, err := Parse(src)
		if err != nil || !reflect.DeepEqual(d, again) {
			t.Fatalf("Parse is not deterministic: %v", err)
		}
	})
}

func FuzzRenderedTemplateParses(f *testing.F) {
	f.Add("feature", "Add login", "SigmaUno/subspace", "goal", "- works")
	f.Add("bugfix", `quote " and \ slash`, "a/b", "multi\nline", "")
	f.Add("feature", "tab\there", "a/b", "g", "a")
	f.Add("feature", "  padded  ", "a/b", "{{title}}", "{{repo}}")
	f.Add("feature", "emoji 🙂 # not a comment", "a/b", "g", "a")
	f.Add("security-audit", "\u0085\u2028", "a/b", "g", "a")
	f.Fuzz(func(t *testing.T, id, title, repo, goal, acceptance string) {
		vars := map[string]string{"title": title, "repo": repo, "goal": goal, "acceptance": acceptance}
		for _, tpl := range Templates() {
			if tpl.ID == id {
				for _, v := range tpl.Variables {
					if _, ok := vars[v]; !ok {
						vars[v] = "x"
					}
				}
			}
		}
		md, err := Render(id, vars)
		if err != nil || !utf8.ValidString(md) || strings.IndexByte(md, 0) >= 0 {
			return
		}
		d, err := Parse([]byte(md))
		if err != nil {
			if strings.TrimSpace(title) == "" || strings.TrimSpace(repo) == "" {
				return
			}
			t.Fatalf("rendered %s does not parse: %v\n%s", id, err, md)
		}
		if !strings.HasSuffix(d.Title, strings.TrimSpace(title)) {
			t.Fatalf("title %q came back as %q", title, d.Title)
		}
	})
}
