package mcp

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/model"
)

func allTools(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	for _, role := range AllRoles {
		for _, tool := range DefaultRegistry().List(role) {
			seen[tool.Name] = true
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	return names
}

func TestEveryErrorCodeHasHintForEveryTool(t *testing.T) {
	tools := allTools(t)
	if len(tools) == 0 {
		t.Fatal("no tools registered")
	}
	for _, code := range model.ErrorCodes {
		if codeHints[code] == "" {
			t.Errorf("code %s has no hint", code)
		}
		for _, tool := range tools {
			if Hint(code, tool) == "" {
				t.Errorf("code %s on %s has no hint", code, tool)
			}
		}
	}
	for code := range codeHints {
		if !slices.Contains(model.ErrorCodes, code) {
			t.Errorf("hint for %s, which is not in model.ErrorCodes", code)
		}
	}
	for code, perTool := range toolHints {
		for tool := range perTool {
			if !slices.Contains(tools, tool) {
				t.Errorf("hint %s/%s names an unknown tool", code, tool)
			}
		}
	}
}

func TestHintsOnlyNameKnownTools(t *testing.T) {
	known := map[string]bool{"in_progress": true, "changes_requested": true}
	for _, n := range allTools(t) {
		known[n] = true
	}
	word := regexp.MustCompile(`[a-z]+(?:_[a-z]+)+`)
	check := func(where, h string) {
		for _, w := range word.FindAllString(h, -1) {
			if !known[w] {
				t.Errorf("%s mentions %q, which is not a tool or status", where, w)
			}
		}
	}
	for code, h := range codeHints {
		check(code, h)
	}
	for code, perTool := range toolHints {
		for tool, h := range perTool {
			check(code+"/"+tool, h)
		}
	}
}

func TestSourceErrorCodesAreListed(t *testing.T) {
	lit := regexp.MustCompile(`model\.Errf\("([a-z_]+)"|\bErrf\("([a-z_]+)"`)
	root := filepath.Join("..")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range lit.FindAllStringSubmatch(string(src), -1) {
			code := m[1] + m[2]
			if !slices.Contains(model.ErrorCodes, code) {
				t.Errorf("%s returns code %q missing from model.ErrorCodes", p, code)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestErrBodyCarriesHint(t *testing.T) {
	e := errBody("not_assigned", "task T-1 is not assigned to you", "claim_task")["error"].(map[string]any)
	if e["hint"] != codeHints["not_assigned"] || e["code"] != "not_assigned" {
		t.Fatalf("%v", e)
	}
	if _, ok := errBody("unheard_of", "x", "claim_task")["error"].(map[string]any)["hint"]; ok {
		t.Fatal("unknown code got a hint")
	}
}
