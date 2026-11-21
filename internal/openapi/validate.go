package openapi

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	httpMethods   = map[string]bool{"get": true, "put": true, "post": true, "delete": true, "options": true, "head": true, "patch": true, "trace": true}
	paramLocation = map[string]bool{"query": true, "header": true, "path": true, "cookie": true}
	statusKey     = regexp.MustCompile(`^([1-5][0-9]{2}|[1-5]XX|default)$`)
	componentName = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
)

type validator struct {
	root  map[string]any
	probs []string
	ids   map[string]string
}

func Validate(raw []byte) []string {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return []string{"document is not a JSON object: " + err.Error()}
	}
	v := &validator{root: root, ids: map[string]string{}}
	v.document()
	v.refs("#", root)
	sort.Strings(v.probs)
	return v.probs
}

func (v *validator) fail(format string, args ...any) {
	v.probs = append(v.probs, fmt.Sprintf(format, args...))
}

func (v *validator) object(where string, x any) (map[string]any, bool) {
	m, ok := x.(map[string]any)
	if !ok {
		v.fail("%s must be an object", where)
	}
	return m, ok
}

func (v *validator) text(where string, m map[string]any, key string) string {
	s, ok := m[key].(string)
	if !ok || s == "" {
		v.fail("%s.%s is required", where, key)
	}
	return s
}

func (v *validator) document() {
	if ver, _ := v.root["openapi"].(string); !strings.HasPrefix(ver, "3.1.") && ver != "3.1" {
		v.fail("openapi must be a 3.1 version, got %q", ver)
	}
	if info, ok := v.object("info", v.root["info"]); ok {
		v.text("info", info, "title")
		v.text("info", info, "version")
	}
	schemes := v.components()
	v.requirements("security", v.root["security"], schemes)
	paths, ok := v.object("paths", v.root["paths"])
	if !ok {
		return
	}
	if len(paths) == 0 {
		v.fail("paths must not be empty")
	}
	for p, item := range paths {
		v.pathItem(p, item, schemes)
	}
	if tools, present := v.root["x-mcp-tools"]; present {
		v.tools(tools)
	}
}

func (v *validator) components() map[string]bool {
	schemes := map[string]bool{}
	comps, ok := v.root["components"].(map[string]any)
	if !ok {
		return schemes
	}
	for kind, group := range comps {
		entries, ok := v.object("components."+kind, group)
		if !ok {
			continue
		}
		for name := range entries {
			if !componentName.MatchString(name) {
				v.fail("components.%s has invalid name %q", kind, name)
			}
		}
	}
	defs, _ := comps["securitySchemes"].(map[string]any)
	for name, def := range defs {
		schemes[name] = true
		s, ok := v.object("securityScheme "+name, def)
		if !ok {
			continue
		}
		switch s["type"] {
		case "http":
			v.text("securityScheme "+name, s, "scheme")
		case "apiKey":
			v.text("securityScheme "+name, s, "name")
			if in, _ := s["in"].(string); !paramLocation[in] || in == "path" {
				v.fail("securityScheme %s has invalid in %q", name, in)
			}
		case "oauth2", "openIdConnect", "mutualTLS":
		default:
			v.fail("securityScheme %s has invalid type %v", name, s["type"])
		}
	}
	return schemes
}

func (v *validator) requirements(where string, x any, schemes map[string]bool) {
	if x == nil {
		return
	}
	list, ok := x.([]any)
	if !ok {
		v.fail("%s must be an array", where)
		return
	}
	for _, r := range list {
		req, ok := v.object(where+" requirement", r)
		if !ok {
			continue
		}
		for name := range req {
			if !schemes[name] {
				v.fail("%s references undeclared security scheme %q", where, name)
			}
		}
	}
}

func (v *validator) pathItem(p string, x any, schemes map[string]bool) {
	if !strings.HasPrefix(p, "/") {
		v.fail("path %q must start with /", p)
	}
	item, ok := v.object("path "+p, x)
	if !ok {
		return
	}
	for method, opx := range item {
		if !httpMethods[method] {
			if method != "parameters" && method != "summary" && method != "description" && method != "servers" && !strings.HasPrefix(method, "x-") {
				v.fail("path %s has unknown field %q", p, method)
			}
			continue
		}
		v.operation(strings.ToUpper(method)+" "+p, p, opx, schemes)
	}
}

func (v *validator) operation(where, p string, x any, schemes map[string]bool) {
	op, ok := v.object(where, x)
	if !ok {
		return
	}
	if id := v.text(where, op, "operationId"); id != "" {
		if prev, dup := v.ids[id]; dup {
			v.fail("operationId %s is used by %s and %s", id, prev, where)
		}
		v.ids[id] = where
	}
	v.parameters(where, p, op["parameters"])
	if body, present := op["requestBody"]; present {
		v.content(where+" requestBody", body)
	}
	v.responses(where, op["responses"])
	v.requirements(where+" security", op["security"], schemes)
}

func (v *validator) parameters(where, p string, x any) {
	declared := map[string]bool{}
	list, _ := x.([]any)
	seen := map[string]bool{}
	for _, px := range list {
		param, ok := v.object(where+" parameter", px)
		if !ok {
			continue
		}
		if _, isRef := param["$ref"]; isRef {
			continue
		}
		name := v.text(where+" parameter", param, "name")
		in, _ := param["in"].(string)
		if !paramLocation[in] {
			v.fail("%s parameter %s has invalid in %q", where, name, in)
		}
		if key := in + ":" + name; seen[key] {
			v.fail("%s declares parameter %s twice", where, key)
		} else {
			seen[key] = true
		}
		if in == "path" {
			declared[name] = true
			if req, _ := param["required"].(bool); !req {
				v.fail("%s path parameter %s must be required", where, name)
			}
		}
		if _, hasSchema := param["schema"]; !hasSchema {
			if _, hasContent := param["content"]; !hasContent {
				v.fail("%s parameter %s needs schema or content", where, name)
			}
		}
	}
	for _, m := range pathParam.FindAllStringSubmatch(p, -1) {
		if !declared[m[1]] {
			v.fail("%s misses path parameter %s", where, m[1])
		}
		delete(declared, m[1])
	}
	for name := range declared {
		v.fail("%s declares path parameter %s that is not in the path", where, name)
	}
}

func (v *validator) content(where string, x any) {
	m, ok := v.object(where, x)
	if !ok {
		return
	}
	if _, isRef := m["$ref"]; isRef {
		return
	}
	content, ok := v.object(where+".content", m["content"])
	if !ok {
		return
	}
	if len(content) == 0 {
		v.fail("%s.content must not be empty", where)
	}
	for media, entry := range content {
		if !strings.Contains(media, "/") {
			v.fail("%s has invalid media type %q", where, media)
		}
		if e, ok := v.object(where+" "+media, entry); ok {
			if _, has := e["schema"]; !has {
				v.fail("%s %s needs a schema", where, media)
			}
		}
	}
}

func (v *validator) responses(where string, x any) {
	rs, ok := v.object(where+" responses", x)
	if !ok {
		return
	}
	if len(rs) == 0 {
		v.fail("%s needs at least one response", where)
	}
	for code, r := range rs {
		if !statusKey.MatchString(code) {
			v.fail("%s has invalid response code %q", where, code)
		}
		resp, ok := v.object(where+" response "+code, r)
		if !ok {
			continue
		}
		if _, isRef := resp["$ref"]; isRef {
			continue
		}
		v.text(where+" response "+code, resp, "description")
		if c, has := resp["content"]; has {
			v.content(where+" response "+code, map[string]any{"content": c})
		}
	}
}

func (v *validator) tools(x any) {
	list, ok := x.([]any)
	if !ok {
		v.fail("x-mcp-tools must be an array")
		return
	}
	seen := map[string]bool{}
	for i, tx := range list {
		where := "x-mcp-tools[" + strconv.Itoa(i) + "]"
		t, ok := v.object(where, tx)
		if !ok {
			continue
		}
		name := v.text(where, t, "name")
		if seen[name] {
			v.fail("%s duplicates tool %s", where, name)
		}
		seen[name] = true
		if roles, _ := t["roles"].([]any); len(roles) == 0 {
			v.fail("%s needs roles", where)
		}
		v.object(where+".inputSchema", t["inputSchema"])
	}
}

func (v *validator) refs(where string, x any) {
	switch t := x.(type) {
	case map[string]any:
		for k, child := range t {
			if k == "$ref" {
				target, _ := child.(string)
				if !v.resolves(target) {
					v.fail("%s has unresolvable $ref %q", where, target)
				}
				continue
			}
			v.refs(where+"/"+strings.NewReplacer("~", "~0", "/", "~1").Replace(k), child)
		}
	case []any:
		for i, child := range t {
			v.refs(where+"/"+strconv.Itoa(i), child)
		}
	}
}

func (v *validator) resolves(target string) bool {
	rest, ok := strings.CutPrefix(target, "#")
	if !ok {
		return false
	}
	var cur any = v.root
	if rest == "" {
		return true
	}
	if !strings.HasPrefix(rest, "/") {
		return false
	}
	for _, tok := range strings.Split(rest[1:], "/") {
		tok = strings.NewReplacer("~1", "/", "~0", "~").Replace(tok)
		switch c := cur.(type) {
		case map[string]any:
			next, ok := c[tok]
			if !ok {
				return false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(c) {
				return false
			}
			cur = c[i]
		default:
			return false
		}
	}
	return true
}
