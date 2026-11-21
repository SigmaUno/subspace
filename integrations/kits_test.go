package integrations

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"text/template"
)

type sample struct {
	URL     string
	HookBin string
}

var values = sample{URL: "https://subspace.example.com", HookBin: "/usr/local/bin/subspace-hook"}

var inlineToken = regexp.MustCompile(`ss[pu]_|experimental_bearer_token|http_headers|Bearer\s+[A-Za-z0-9_-]{16,}`)

func render(t *testing.T, path string) string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := template.New(filepath.Base(path)).Option("missingkey=error").Parse(string(src))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, values); err != nil {
		t.Fatalf("render %s: %v", path, err)
	}
	if m := inlineToken.FindString(out.String()); m != "" {
		t.Fatalf("%s embeds credentials (%q):\n%s", path, m, out.String())
	}
	return out.String()
}

func TestCodexConfigFragment(t *testing.T) {
	doc, err := parseTOML(render(t, "codex/config.toml.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	srv := lookup(t, doc, "mcp_servers", "subspace").(map[string]any)
	if srv["url"] != values.URL+"/mcp" {
		t.Fatalf("url %v", srv["url"])
	}
	if srv["bearer_token_env_var"] != "SUBSPACE_TOKEN" {
		t.Fatalf("bearer_token_env_var %v", srv["bearer_token_env_var"])
	}
	want := []any{values.HookBin, "notify"}
	if fmt.Sprint(doc["notify"]) != fmt.Sprint(want) {
		t.Fatalf("notify %v", doc["notify"])
	}
}

func TestOpenCodeConfig(t *testing.T) {
	var doc struct {
		MCP map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
			Enabled bool              `json:"enabled"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(render(t, "opencode/opencode.json.tmpl")), &doc); err != nil {
		t.Fatal(err)
	}
	srv, ok := doc.MCP["subspace"]
	if !ok {
		t.Fatalf("no subspace server: %+v", doc)
	}
	if srv.Type != "remote" || srv.URL != values.URL+"/mcp" || !srv.Enabled {
		t.Fatalf("server %+v", srv)
	}
	if srv.Headers["Authorization"] != "Bearer {env:SUBSPACE_TOKEN}" {
		t.Fatalf("headers %v", srv.Headers)
	}
}

func TestPluginAndScripts(t *testing.T) {
	plugin, err := os.ReadFile("opencode/plugins/subspace.ts")
	if err != nil {
		t.Fatal(err)
	}
	src := string(plugin)
	if m := inlineToken.FindString(src); m != "" {
		t.Fatalf("plugin embeds credentials: %q", m)
	}
	for _, want := range []string{"subspace-hook", `"heartbeat"`, `"log"`, `"instructions"`, "tool.execute.after", "system.transform"} {
		if !strings.Contains(src, want) {
			t.Fatalf("plugin lacks %s", want)
		}
	}
	if strings.Contains(src, "from \"@") {
		t.Fatal("plugin must not import packages")
	}

	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	for _, script := range []string{"codex/install.sh", "opencode/install.sh"} {
		if out, err := exec.Command(sh, "-n", script).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", script, err, out)
		}
	}
}

func TestInstallScriptsRenderKits(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	dest := filepath.Join(dir, "subspace.config.toml")
	if out, err := exec.Command(sh, "codex/install.sh", values.URL+"/", values.HookBin, dest).CombinedOutput(); err != nil {
		t.Fatalf("codex install: %v\n%s", err, out)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != render(t, "codex/config.toml.tmpl") {
		t.Fatalf("codex install output differs:\n%s", got)
	}

	project := filepath.Join(dir, "project")
	if out, err := exec.Command(sh, "opencode/install.sh", values.URL, project).CombinedOutput(); err != nil {
		t.Fatalf("opencode install: %v\n%s", err, out)
	}
	got, _ = os.ReadFile(filepath.Join(project, "opencode.json"))
	if string(got) != render(t, "opencode/opencode.json.tmpl") {
		t.Fatalf("opencode install output differs:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(project, ".opencode", "plugins", "subspace.ts")); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(sh, "opencode/install.sh", values.URL, project).Run(); err == nil {
		t.Fatal("install must not overwrite an existing opencode.json")
	}
}

func lookup(t *testing.T, doc map[string]any, path ...string) any {
	t.Helper()
	var cur any = doc
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok || m[p] == nil {
			t.Fatalf("missing %s", strings.Join(path, "."))
		}
		cur = m[p]
	}
	return cur
}

func parseTOML(src string) (map[string]any, error) {
	root := map[string]any{}
	cur := root
	for n, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fail := func(msg string) error { return fmt.Errorf("line %d: %s: %q", n+1, msg, line) }
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") || strings.HasPrefix(line, "[[") {
				return nil, fail("bad table header")
			}
			cur = root
			for _, key := range strings.Split(line[1:len(line)-1], ".") {
				next, _ := cur[key].(map[string]any)
				if next == nil {
					next = map[string]any{}
					cur[key] = next
				}
				cur = next
			}
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fail("expected key = value")
		}
		if _, dup := cur[key]; dup {
			return nil, fail("duplicate key")
		}
		v, err := parseValue(strings.TrimSpace(val))
		if err != nil {
			return nil, fail(err.Error())
		}
		cur[key] = v
	}
	return root, nil
}

func parseValue(s string) (any, error) {
	switch {
	case strings.HasPrefix(s, `"`):
		return strconv.Unquote(s)
	case strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]"):
		items := []any{}
		for _, part := range splitTop(s[1 : len(s)-1]) {
			v, err := parseValue(part)
			if err != nil {
				return nil, err
			}
			items = append(items, v)
		}
		return items, nil
	case s == "true" || s == "false":
		return s == "true", nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	return nil, fmt.Errorf("unsupported value %s", s)
}

func splitTop(s string) []string {
	var parts []string
	var b strings.Builder
	quoted := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && quoted && i+1 < len(s):
			b.WriteByte(c)
			i++
			b.WriteByte(s[i])
			continue
		case c == '"':
			quoted = !quoted
		case c == ',' && !quoted:
			parts = append(parts, strings.TrimSpace(b.String()))
			b.Reset()
			continue
		}
		b.WriteByte(c)
	}
	if rest := strings.TrimSpace(b.String()); rest != "" {
		parts = append(parts, rest)
	}
	return parts
}
