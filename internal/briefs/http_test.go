package briefs_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/briefs"
	"github.com/SigmaUno/subspace/internal/servertest"
)

func post(t *testing.T, e *servertest.Env, path, contentType string, body []byte) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.TS.URL+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+servertest.OwnerToken)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: %d %s", path, resp.StatusCode, raw)
	}
	return resp.StatusCode, out
}

const importDoc = `---
title: "Import: with components"
repos: [SigmaUno/subspace]
max_cost_eur: 20
max_parallel_agents: 3
allowed_providers: [anthropic, openai]
cross_review: false
task_budget:
  max_minutes: 45
  max_cost_eur: 1.5
components:
  - name: api
    paths: [internal/api]
    depends_on: [store]
  - name: store
    paths: [internal/store]
---
Build it.
`

func TestImportCreatesBriefAndComponentPlan(t *testing.T) {
	e := servertest.New(t)
	code, out := post(t, e, "/api/v1/briefs/import", "text/markdown", []byte(importDoc))
	if code != http.StatusCreated {
		t.Fatalf("import: %d %v", code, out)
	}
	br := out["brief"].(map[string]any)
	id := br["id"].(string)
	if br["title"] != "Import: with components" || br["cross_review"] != false ||
		br["budget"].(map[string]any)["max_parallel_agents"] != 3.0 || br["budget"].(map[string]any)["max_cost_eur"] != 20.0 {
		t.Fatalf("brief: %v", br)
	}
	if body := br["body"].(string); !strings.HasPrefix(body, "Build it.\n\n## Task budget defaults") ||
		!strings.Contains(body, "- max_minutes: 45\n- max_cost_eur: 1.5") {
		t.Fatalf("body: %q", body)
	}
	if comps := out["components"].([]any); len(comps) != 2 || comps[0].(map[string]any)["id"] != "C-1" {
		t.Fatalf("components: %v", comps)
	}

	code, got := e.REST("GET", "/api/v1/briefs/"+id, nil)
	if code != 200 || got["title"] != br["title"] {
		t.Fatalf("get brief: %d %v", code, got)
	}
	code, arch := e.REST("GET", "/api/v1/briefs/"+id+"/architecture", nil)
	if code != 200 {
		t.Fatalf("architecture: %d %v", code, arch)
	}
	edges := arch["edges"].([]any)
	if len(arch["nodes"].([]any)) != 2 || len(edges) != 1 ||
		!reflect.DeepEqual(edges[0], map[string]any{"from": "C-1", "to": "C-2"}) {
		t.Fatalf("architecture: %v", arch)
	}
}

func TestImportDryRunAndJSONBody(t *testing.T) {
	e := servertest.New(t)
	body, _ := json.Marshal(map[string]string{"markdown": importDoc})
	code, out := post(t, e, "/api/v1/briefs/import?dry_run=true", "application/json", body)
	if code != http.StatusOK {
		t.Fatalf("dry run: %d %v", code, out)
	}
	p := out["preview"].(map[string]any)
	if p["title"] != "Import: with components" || p["body"] != "Build it." || len(p["components"].([]any)) != 2 ||
		!reflect.DeepEqual(p["task_budget"], map[string]any{"max_minutes": 45.0, "max_cost_eur": 1.5}) {
		t.Fatalf("preview: %v", p)
	}
	if _, list := e.REST("GET", "/api/v1/briefs", nil); len(list["briefs"].([]any)) != 0 {
		t.Fatalf("dry run created a brief: %v", list)
	}

	code, out = post(t, e, "/api/v1/briefs/import", "application/json", []byte(`{"markdown":"x","extra":1}`))
	if code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d %v", code, out)
	}
	code, out = post(t, e, "/api/v1/briefs/import?dry_run=maybe", "text/markdown", []byte(importDoc))
	if code != http.StatusBadRequest {
		t.Fatalf("bad dry_run: %d %v", code, out)
	}
}

func TestImportRejectsWithPositionAndCreatesNothing(t *testing.T) {
	e := servertest.New(t)
	doc := strings.Replace(importDoc, "  - name: store\n", "  - name: store\n    depends_on: [api]\n", 1)
	for _, c := range []struct {
		src       string
		line, col float64
	}{
		{"---\ntitle: a\ntitle: b\n---\n", 3, 1},
		{doc, 11, 1},
		{"---\ntitle: x\nrepos: [a, [b]]\n---\n", 3, 12},
	} {
		code, out := post(t, e, "/api/v1/briefs/import", "text/markdown", []byte(c.src))
		er := out["error"].(map[string]any)
		if code != http.StatusBadRequest || er["code"] != "bad_request" || er["line"] != c.line || er["column"] != c.col {
			t.Fatalf("%q: %d %v", c.src, code, out)
		}
	}
	huge := append([]byte("---\ntitle: x\n---\n"), bytes.Repeat([]byte("a"), briefs.MaxDocumentBytes)...)
	if code, out := post(t, e, "/api/v1/briefs/import", "text/markdown", huge); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("huge: %d %v", code, out)
	}
	if _, list := e.REST("GET", "/api/v1/briefs", nil); len(list["briefs"].([]any)) != 0 {
		t.Fatalf("rejected imports created briefs: %v", list)
	}
	req, _ := http.NewRequest(http.MethodPost, e.TS.URL+"/api/v1/briefs/import", strings.NewReader(importDoc))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
}

func TestExampleBriefsMatchJSON(t *testing.T) {
	e := servertest.New(t)
	files, _ := filepath.Glob("../../examples/briefs/*.md")
	n := 0
	for _, md := range files {
		if filepath.Base(md) == "README.md" {
			continue
		}
		n++
		src, err := os.ReadFile(md)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(strings.TrimSuffix(md, ".md") + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var want map[string]any
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatal(err)
		}
		code, out := post(t, e, "/api/v1/briefs/import", "text/markdown", src)
		if code != http.StatusCreated {
			t.Fatalf("%s: %d %v", md, code, out)
		}
		br := out["brief"].(map[string]any)
		budget := br["budget"].(map[string]any)
		if br["title"] != want["title"] || !strings.HasPrefix(br["body"].(string), strings.TrimSpace(want["body"].(string))) ||
			!reflect.DeepEqual(br["repos"], want["repos"]) || !reflect.DeepEqual(br["allowed_providers"], want["allowed_providers"]) ||
			budget["max_cost_eur"] != want["max_cost_eur"] || budget["max_parallel_agents"] != want["max_parallel_agents"] {
			t.Errorf("%s does not match its JSON sibling:\n got %v\nwant %v", md, br, want)
		}
	}
	if n < 5 {
		t.Fatalf("found %d markdown examples", n)
	}
}

func TestTemplatesRenderToImportableBriefs(t *testing.T) {
	e := servertest.New(t)
	code, out := e.REST("GET", "/api/v1/brief-templates", nil)
	if code != 200 {
		t.Fatalf("list: %d %v", code, out)
	}
	var ids []string
	for _, x := range out["templates"].([]any) {
		ids = append(ids, x.(map[string]any)["id"].(string))
	}
	want := []string{"bugfix", "docs-free-chore", "feature", "performance", "refactor", "security-audit"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("templates: %v", ids)
	}

	hostile := `He said "stop" \ # not: a comment, [x] {y} - z`
	for _, tpl := range out["templates"].([]any) {
		tm := tpl.(map[string]any)
		id := tm["id"].(string)
		q := url.Values{}
		for _, v := range tm["variables"].([]any) {
			q.Set(v.(string), hostile)
		}
		q.Set("repo", "SigmaUno/subspace")
		code, r := e.REST("GET", "/api/v1/brief-templates/"+id+"/render?"+q.Encode(), nil)
		if code != 200 {
			t.Fatalf("%s render: %d %v", id, code, r)
		}
		md := r["markdown"].(string)
		if strings.Contains(md, "{{") {
			t.Fatalf("%s: unrendered placeholder:\n%s", id, md)
		}
		doc, err := briefs.Parse([]byte(md))
		if err != nil {
			t.Fatalf("%s: rendered template does not parse: %v\n%s", id, err, md)
		}
		if !strings.HasSuffix(doc.Title, hostile) || !reflect.DeepEqual(doc.Repos, []string{"SigmaUno/subspace"}) || doc.TaskBudget == nil {
			t.Fatalf("%s: %+v", id, doc)
		}
		if code, created := post(t, e, "/api/v1/briefs/import", "text/markdown", []byte(md)); code != http.StatusCreated {
			t.Fatalf("%s import: %d %v", id, code, created)
		}
	}

	code, r := e.REST("GET", "/api/v1/brief-templates/feature/render?title=x", nil)
	if code != 400 || !strings.Contains(r["error"].(map[string]any)["message"].(string), "missing variables: repo, goal, acceptance") {
		t.Fatalf("missing vars: %d %v", code, r)
	}
	code, r = e.REST("GET", "/api/v1/brief-templates/feature/render?"+url.Values{
		"title": {"a\nmax_cost_eur: 9999"}, "repo": {"r"}, "goal": {"g"}, "acceptance": {"a"}}.Encode(), nil)
	if code != 400 {
		t.Fatalf("multi-line frontmatter variable: %d %v", code, r)
	}
	if code, r = e.REST("GET", "/api/v1/brief-templates/nope/render", nil); code != 404 {
		t.Fatalf("unknown template: %d %v", code, r)
	}
}
