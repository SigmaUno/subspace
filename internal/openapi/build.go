package openapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	SpecVersion  = "3.1.0"
	bearerScheme = "bearer"
)

type Tool struct {
	Name        string
	Description string
	Roles       []string
	InputSchema map[string]any
}

type Options struct {
	Version    string
	ServerURL  string
	Registered func(method, path string) bool
	Tools      []Tool
}

var pathParam = regexp.MustCompile(`\{([^}]+)\}`)

func Build(o Options) ([]byte, error) {
	sc := newSchemas()
	sc.of(Error{})
	paths := map[string]any{}
	tags := map[string]bool{}
	ids := map[string]string{}
	for _, op := range operations {
		if o.Registered != nil && !o.Registered(op.Method, op.Path) {
			continue
		}
		id := op.ID()
		key := op.Method + " " + op.Path
		if prev, dup := ids[id]; dup {
			return nil, fmt.Errorf("openapi: operationId %s used by %s and %s", id, prev, key)
		}
		ids[id] = key
		item, _ := paths[op.Path].(map[string]any)
		if item == nil {
			item = map[string]any{}
			paths[op.Path] = item
		}
		item[strings.ToLower(op.Method)] = sc.operation(op, id)
		tags[op.Tag()] = true
	}
	info := map[string]any{
		"title":       "Subspace API",
		"version":     o.Version,
		"description": "Owner REST API of Subspace, plus the MCP endpoint. MCP tools are listed in x-mcp-tools.",
	}
	doc := map[string]any{
		"openapi":  SpecVersion,
		"info":     info,
		"security": []any{map[string]any{bearerScheme: []string{}}},
		"tags":     tagList(tags),
		"paths":    paths,
		"components": map[string]any{
			"securitySchemes": map[string]any{
				bearerScheme: map[string]any{
					"type": "http", "scheme": "bearer",
					"description": "Owner token on /api/v1, agent token on /mcp",
				},
				"webhookSignature": map[string]any{
					"type": "apiKey", "in": "header", "name": "X-Hub-Signature-256",
					"description": "HMAC-SHA256 of the body; Forgejo sends X-Forgejo-Signature or X-Gitea-Signature",
				},
			},
			"responses": map[string]any{"Error": map[string]any{
				"description": "Error",
				"content":     map[string]any{mediaJSON: map[string]any{"schema": ref("Error")}},
			}},
			"schemas": sc.defs,
		},
		"x-mcp-tools": toolList(o.Tools),
	}
	if o.ServerURL != "" {
		doc["servers"] = []any{map[string]any{"url": o.ServerURL}}
	}
	return json.Marshal(doc)
}

func ref(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }

func tagList(set map[string]bool) []any {
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]any, len(names))
	for i, n := range names {
		out[i] = map[string]any{"name": n}
	}
	return out
}

func toolList(tools []Tool) []any {
	sorted := append([]Tool(nil), tools...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	out := make([]any, 0, len(sorted))
	for _, t := range sorted {
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		roles := append([]string{}, t.Roles...)
		sort.Strings(roles)
		out = append(out, map[string]any{"name": t.Name, "description": t.Description, "roles": roles, "inputSchema": schema})
	}
	return out
}

func (s *schemas) operation(op Op, id string) map[string]any {
	out := map[string]any{
		"tags":        []string{op.Tag()},
		"summary":     op.Summary,
		"operationId": id,
		"parameters":  parameters(op),
		"responses":   s.responses(op),
	}
	bodies := map[string]any{}
	for media, sample := range op.Bodies {
		bodies[media] = map[string]any{"schema": s.of(sample)}
	}
	if op.Request != nil {
		bodies[mediaJSON] = map[string]any{"schema": s.of(op.Request)}
	}
	if len(bodies) > 0 {
		out["requestBody"] = map[string]any{"content": bodies}
	}
	switch {
	case op.Security != "":
		out["security"] = []any{map[string]any{op.Security: []string{}}}
	case op.Public:
		out["security"] = []any{}
	}
	return out
}

func (s *schemas) responses(op Op) map[string]any {
	success := map[string]any{"description": http.StatusText(op.Status)}
	if op.Response != nil && op.Status != http.StatusNoContent {
		media := op.Media
		if media == "" {
			media = mediaJSON
		}
		success["content"] = map[string]any{media: map[string]any{"schema": s.of(op.Response)}}
	}
	return map[string]any{
		strconv.Itoa(op.Status): success,
		"default":               map[string]any{"$ref": "#/components/responses/Error"},
	}
}

func parameters(op Op) []any {
	out := []any{}
	for _, m := range pathParam.FindAllStringSubmatch(op.Path, -1) {
		typ := "string"
		if m[1] == "n" {
			typ = "integer"
		}
		out = append(out, map[string]any{"name": m[1], "in": "path", "required": true, "schema": map[string]any{"type": typ}})
	}
	for _, p := range []struct{ in, spec string }{{"query", op.Query}, {"header", op.Header}} {
		for _, tok := range strings.Fields(p.spec) {
			name, typ, _ := strings.Cut(tok, ":")
			required := strings.HasSuffix(tok, "!")
			name = strings.TrimSuffix(name, "!")
			typ = strings.TrimSuffix(typ, "!")
			if typ == "" {
				typ = "string"
			}
			param := map[string]any{"name": name, "in": p.in, "schema": map[string]any{"type": typ}}
			if required {
				param["required"] = true
			}
			out = append(out, param)
		}
	}
	return out
}
