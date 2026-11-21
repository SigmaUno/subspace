package openapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/openapi"
	"github.com/SigmaUno/subspace/internal/servertest"
)

var update = flag.Bool("update", false, "regenerate the golden files")

const (
	restGolden  = "testdata/openapi.golden.json"
	toolsGolden = "testdata/mcp-tools.golden.json"
	allowFile   = "testdata/breaking.allow"
)

func canonical(t *testing.T, v any) []byte {
	t.Helper()
	var raw bytes.Buffer
	enc := json.NewEncoder(&raw)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	var generic any
	if err := json.Unmarshal(raw.Bytes(), &generic); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	enc = json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(generic); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func currentDocument(t *testing.T) map[string]any {
	t.Helper()
	raw, err := openapi.Build(openapi.Options{Version: "golden"})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func currentTools(t *testing.T) map[string]any {
	t.Helper()
	env := servertest.New(t)
	out := map[string]any{}
	for _, role := range mcp.AllRoles {
		tok := env.Register("golden-"+role, role, "other")
		res, _ := env.RPC(tok, "tools/list", map[string]any{})["result"].(map[string]any)
		tools := map[string]any{}
		for _, x := range list(res["tools"]) {
			m := obj(x)
			tools[m["name"].(string)] = m["inputSchema"]
		}
		if len(tools) == 0 {
			t.Fatalf("no tools for role %s", role)
		}
		out[role] = tools
	}
	return out
}

func readAllowlist(t *testing.T) map[string]string {
	t.Helper()
	text, err := os.ReadFile(allowFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	allow, err := parseAllowlist(string(text))
	if err != nil {
		t.Fatalf("%s: %v", allowFile, err)
	}
	return allow
}

func checkGolden(t *testing.T, path string, cur map[string]any, classify func(old, cur map[string]any) []change) {
	t.Helper()
	want := canonical(t, cur)
	have, err := os.ReadFile(path)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		t.Fatal(err)
	}
	var changes []change
	if !missing {
		var old map[string]any
		if err := json.Unmarshal(have, &old); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		changes = classify(old, cur)
		if bad := unallowed(changes, readAllowlist(t)); len(bad) > 0 {
			t.Fatalf("breaking API changes against %s (list them with a reason in %s to accept):\n  %s",
				path, allowFile, strings.Join(bad, "\n  "))
		}
	}
	if bytes.Equal(have, want) {
		return
	}
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if missing {
		t.Fatalf("%s does not exist; run go test ./internal/openapi -run TestOpenAPIGolden -update", path)
	}
	var lines []string
	for _, c := range changes {
		kind := "additive"
		if c.breaking {
			kind = "breaking, allowed"
		}
		lines = append(lines, kind+": "+c.key)
	}
	if len(lines) == 0 {
		lines = append(lines, "no structural change (descriptions or metadata differ)")
	}
	t.Fatalf("%s is out of date; run go test ./internal/openapi -run TestOpenAPIGolden -update\n  %s",
		path, strings.Join(lines, "\n  "))
}

func TestOpenAPIGolden(t *testing.T) {
	t.Run("rest", func(t *testing.T) {
		checkGolden(t, restGolden, currentDocument(t), classifyDocs)
	})
	t.Run("mcp", func(t *testing.T) {
		checkGolden(t, toolsGolden, currentTools(t), classifyTools)
	})
}
