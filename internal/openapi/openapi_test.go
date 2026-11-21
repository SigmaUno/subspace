package openapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type inner struct {
	Created time.Time `json:"created"`
}

type shape struct {
	*inner
	Name    string            `json:"name"`
	Skip    string            `json:"-"`
	Tags    []string          `json:"tags"`
	Extra   map[string]int    `json:"extra,omitempty"`
	Parent  *shape            `json:"parent"`
	Any     Free              `json:"any,omitempty"`
	Labels  map[string]string `json:"labels"`
	Counter *int              `json:"counter"`
}

func build(t *testing.T, o Options) map[string]any {
	t.Helper()
	raw, err := Build(o)
	if err != nil {
		t.Fatal(err)
	}
	if probs := Validate(raw); len(probs) > 0 {
		t.Fatalf("invalid document:\n%s", strings.Join(probs, "\n"))
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func sub(t *testing.T, m map[string]any, keys ...string) map[string]any {
	t.Helper()
	for _, k := range keys {
		next, ok := m[k].(map[string]any)
		if !ok {
			t.Fatalf("missing %s in %v", k, m)
		}
		m = next
	}
	return m
}

func TestBuildContainsEveryOperation(t *testing.T) {
	tools := []Tool{
		{Name: "whoami", Description: "who", Roles: []string{"worker", "leader"}, InputSchema: map[string]any{"type": "object"}},
		{Name: "board", Roles: []string{"leader"}},
	}
	doc := build(t, Options{Version: "test", ServerURL: "https://s.example.com", Tools: tools})
	paths := sub(t, doc, "paths")
	for _, op := range Routes() {
		item := sub(t, paths, op.Path)
		if _, ok := item[strings.ToLower(op.Method)]; !ok {
			t.Errorf("%s %s missing from document", op.Method, op.Path)
		}
	}
	listed, _ := doc["x-mcp-tools"].([]any)
	if len(listed) != 2 || listed[0].(map[string]any)["name"] != "board" {
		t.Fatalf("x-mcp-tools = %v", listed)
	}
	if got := listed[1].(map[string]any)["roles"].([]any); got[0] != "leader" || got[1] != "worker" {
		t.Fatalf("roles not sorted: %v", got)
	}
	sec := sub(t, paths, "/healthz", "get")
	if s, ok := sec["security"].([]any); !ok || len(s) != 0 {
		t.Fatalf("healthz must opt out of security: %v", sec["security"])
	}
	if s := sub(t, paths, "/hooks/github", "post")["security"].([]any); len(s) != 1 {
		t.Fatalf("webhook security = %v", s)
	}
}

func TestBuildHonoursRegisteredFilter(t *testing.T) {
	doc := build(t, Options{Version: "test", Registered: func(method, path string) bool {
		return path != "/mcp" && !strings.HasPrefix(path, "/api/v1/mergequeue")
	}})
	paths := sub(t, doc, "paths")
	for p := range paths {
		if p == "/mcp" || strings.HasPrefix(p, "/api/v1/mergequeue") {
			t.Errorf("unregistered path %s in document", p)
		}
	}
	if _, ok := paths["/api/v1/state"]; !ok {
		t.Fatal("registered path dropped")
	}
}

func TestOperationIDsAreUnique(t *testing.T) {
	seen := map[string]string{}
	keys := map[string]bool{}
	for _, op := range Routes() {
		k := op.Method + " " + op.Path
		if keys[k] {
			t.Errorf("%s registered twice", k)
		}
		keys[k] = true
		if prev, dup := seen[op.ID()]; dup {
			t.Errorf("operationId %s used by %s and %s", op.ID(), prev, k)
		}
		seen[op.ID()] = k
	}
}

func TestSchemaReflection(t *testing.T) {
	s := newSchemas()
	ref := s.of(shape{})
	if ref["$ref"] != "#/components/schemas/shape" {
		t.Fatalf("ref = %v", ref)
	}
	def := sub(t, map[string]any{"d": s.defs}, "d", "shape")
	props := sub(t, def, "properties")
	for _, want := range []string{"created", "name", "tags", "extra", "parent", "any", "labels", "counter"} {
		if _, ok := props[want]; !ok {
			t.Errorf("property %s missing", want)
		}
	}
	for _, bad := range []string{"Skip", "inner"} {
		if _, ok := props[bad]; ok {
			t.Errorf("property %s must not be exported", bad)
		}
	}
	required, _ := def["required"].([]string)
	if strings.Join(required, ",") != "counter,created,labels,name,parent,tags" {
		t.Fatalf("required = %v", required)
	}
	if got := props["created"].(map[string]any)["format"]; got != "date-time" {
		t.Fatalf("time format = %v", got)
	}
	if one, _ := props["parent"].(map[string]any)["oneOf"].([]any); len(one) != 2 {
		t.Fatalf("nullable ref = %v", props["parent"])
	}
	if got := props["counter"].(map[string]any)["type"].([]string); got[0] != "integer" || got[1] != "null" {
		t.Fatalf("nullable integer = %v", got)
	}
}

func TestSameNamedTypesGetDistinctComponents(t *testing.T) {
	doc := build(t, Options{Version: "test"})
	schemas := sub(t, doc, "components", "schemas")
	for _, name := range []string{"Message", "ChatMessage", "InstructionVersion", "InstructionMeta", "PRStatus"} {
		if _, ok := schemas[name]; !ok {
			t.Errorf("component %s missing", name)
		}
	}
}

func TestValidateRejectsBrokenDocuments(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(doc map[string]any)
		want   string
	}{
		{"old version", func(d map[string]any) { d["openapi"] = "3.0.3" }, "3.1"},
		{"missing title", func(d map[string]any) { delete(sub(t, d, "info"), "title") }, "info.title"},
		{"dangling ref", func(d map[string]any) {
			sub(t, d, "components", "responses", "Error")["content"] = map[string]any{
				"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Nope"}},
			}
		}, "unresolvable"},
		{"duplicate operationId", func(d map[string]any) {
			sub(t, d, "paths", "/api/v1/state", "get")["operationId"] = sub(t, d, "paths", "/healthz", "get")["operationId"]
		}, "operationId"},
		{"missing path parameter", func(d map[string]any) {
			sub(t, d, "paths", "/api/v1/briefs/{id}", "get")["parameters"] = []any{}
		}, "misses path parameter id"},
		{"undeclared security scheme", func(d map[string]any) {
			sub(t, d, "paths", "/api/v1/state", "get")["security"] = []any{map[string]any{"ghost": []any{}}}
		}, "undeclared security scheme"},
		{"empty responses", func(d map[string]any) {
			sub(t, d, "paths", "/api/v1/state", "get")["responses"] = map[string]any{}
		}, "at least one response"},
		{"bad response code", func(d map[string]any) {
			sub(t, d, "paths", "/api/v1/state", "get", "responses")["20"] = map[string]any{"description": "x"}
		}, "invalid response code"},
		{"tool without roles", func(d map[string]any) {
			d["x-mcp-tools"] = []any{map[string]any{"name": "x", "roles": []any{}, "inputSchema": map[string]any{}}}
		}, "needs roles"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, err := Build(Options{Version: "test"})
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			c.mutate(doc)
			broken, _ := json.Marshal(doc)
			probs := strings.Join(Validate(broken), "\n")
			if !strings.Contains(probs, c.want) {
				t.Fatalf("want a problem containing %q, got:\n%s", c.want, probs)
			}
		})
	}
}
