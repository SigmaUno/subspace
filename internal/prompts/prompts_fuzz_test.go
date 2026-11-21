package prompts

import (
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

func FuzzParse(f *testing.F) {
	entries, err := embedded.ReadDir("library")
	if err != nil {
		f.Fatal(err)
	}
	for _, e := range entries {
		data, err := embedded.ReadFile("library/" + e.Name())
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	for _, s := range []string{
		"---\nid: a\ntitle: A\n---\nbody",
		"---\r\nid: a\r\ntitle: A\r\nvariables: [x, y]\r\n---\r\n{{x}} {{ y }}",
		"---\nid: a\ntitle: A\nkind: task\ntask_title: T\nacceptance_criteria:\n  - \"quoted \\\" item\"\n  - plain\n---\n",
		"---\nid: a\ntitle: A\nkind: task\ntask_title: T\nbudget_minutes: 1.5\n---\n",
		"---\nid: a\ntitle: A\nkind: task\ntask_title: T\nacceptance_criteria:\n\t- tab\n---\n",
		"---\nid: a\ntitle: A\nkind: task\nkind: task\ntask_title: T\n---\n",
		"---\nid: a\ntitle: A\n- orphan\n---\n",
		"---\nid: a\ntitle: A\nvariables: [x]\n---\n{{y}}",
		"---\nid: a\ntitle: A",
		"id: a\n",
		"",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		start := time.Now()
		tpl, err := parse(data)
		if el := time.Since(start); el > 5*time.Second {
			t.Fatalf("parse took %s on %d bytes", el, len(data))
		}
		if err != nil {
			return
		}
		if !idPattern.MatchString(tpl.ID) || tpl.Title == "" {
			t.Fatalf("invalid template accepted: %+v", tpl)
		}
		if tpl.Kind != "" && tpl.Kind != KindTask {
			t.Fatalf("kind %q", tpl.Kind)
		}
		if len(tpl.AcceptanceCriteria) > maxCriteria {
			t.Fatalf("%d criteria", len(tpl.AcceptanceCriteria))
		}
		for _, c := range tpl.AcceptanceCriteria {
			if c == "" || utf8.RuneCountInString(c) > maxCriterionLen || strings.ContainsFunc(c, unicode.IsControl) {
				t.Fatalf("bad criterion %q", c)
			}
		}
		if tpl.Kind == KindTask && (strings.TrimSpace(tpl.TaskTitle) == "" || tpl.Role != "" && tpl.Role != "worker" && tpl.Role != "tester") {
			t.Fatalf("bad task template %+v", tpl)
		}
		if tpl.Budget != nil && (tpl.Budget.MaxMinutes < 0 || tpl.Budget.MaxCostEUR < 0) {
			t.Fatalf("negative budget %+v", tpl.Budget)
		}
		crlf, err := parse([]byte(strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n", "\r\n")))
		if err != nil || !reflect.DeepEqual(tpl, crlf) {
			t.Fatalf("CRLF parse differs: %v", err)
		}
		vars := map[string]string{}
		for _, v := range tpl.Variables {
			vars[v] = "<" + v + ">"
		}
		if out := substitute(tpl.Body, vars); placeholder.MatchString(out) {
			t.Fatalf("placeholder left in %q", out)
		}
	})
}
