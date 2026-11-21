package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/openapi"
	"github.com/SigmaUno/subspace/internal/spawner"
)

const openapiPath = "/api/v1/openapi.json"

var (
	openapiPatternCall = regexp.MustCompile(`(?:\.Handle(?:Func)?\(|\bMount\([^,]+,\s*|\bm\()\s*"([^"]+)"`)
	openapiHookLiteral = regexp.MustCompile(`"(/hooks/[a-z]+)"`)
	openapiParam       = regexp.MustCompile(`\{[^}]*\}`)
	openapiRoots       = []string{"/api/v1", "/mcp", "/healthz", "/a/", "/hooks/"}
)

func (e *env) openapiDoc() (map[string]any, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.ts.URL+openapiPath, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+ownerTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
		e.t.Fatalf("GET %s: %d %s %s", openapiPath, resp.StatusCode, resp.Header.Get("Content-Type"), raw)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		e.t.Fatal(err)
	}
	return doc, raw
}

func openapiOps(t *testing.T, doc map[string]any) map[string]bool {
	t.Helper()
	paths, _ := doc["paths"].(map[string]any)
	out := map[string]bool{}
	for p, item := range paths {
		for method := range item.(map[string]any) {
			out[strings.ToUpper(method)+" "+p] = true
		}
	}
	return out
}

func openapiNormalize(p string) string { return openapiParam.ReplaceAllString(p, "{}") }

func openapiRegisteredPatterns(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("mount_*.go")
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, "server.go", "../api/api.go")
	var out []string
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var found []string
		for _, m := range openapiPatternCall.FindAllStringSubmatch(string(src), -1) {
			found = append(found, m[1])
		}
		for _, m := range openapiHookLiteral.FindAllStringSubmatch(string(src), -1) {
			found = append(found, m[1])
		}
		for _, p := range found {
			_, path, hasMethod := strings.Cut(p, " ")
			if !hasMethod {
				path = p
			}
			if path == "/api/v1/" || !slices.ContainsFunc(openapiRoots, func(r string) bool { return strings.HasPrefix(path, r) }) {
				continue
			}
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	if len(out) < 60 {
		t.Fatalf("scanned only %d registered patterns in internal/server/mount_*.go; update openapiPatternCall in internal/server/openapi_test.go if the mount helpers changed", len(out))
	}
	sort.Strings(out)
	return out
}

func openapiHasOperation(pattern string, ops []openapi.Op) bool {
	method, path, hasMethod := strings.Cut(pattern, " ")
	if !hasMethod {
		method, path = "", pattern
	}
	for _, op := range ops {
		switch {
		case strings.HasSuffix(path, "/"):
			if strings.HasPrefix(op.Path, path) {
				return true
			}
		case method == "":
			if openapiNormalize(op.Path) == openapiNormalize(path) || strings.HasPrefix(op.Path, path+"/") {
				return true
			}
		default:
			if op.Method == method && openapiNormalize(op.Path) == openapiNormalize(path) {
				return true
			}
		}
	}
	return false
}

func TestOpenAPIServedAndValid(t *testing.T) {
	e := contractEnv(t)
	doc, raw := e.openapiDoc()
	if probs := openapi.Validate(raw); len(probs) > 0 {
		t.Fatalf("served document is invalid; fix internal/openapi/operations.go or internal/openapi/build.go:\n%s", strings.Join(probs, "\n"))
	}
	if doc["openapi"] != "3.1.0" {
		t.Fatalf("openapi = %v, want 3.1.0: edit SpecVersion in internal/openapi/build.go", doc["openapi"])
	}
	if servers, _ := doc["servers"].([]any); len(servers) != 1 || servers[0].(map[string]any)["url"] != "http://sub.test" {
		t.Fatalf("servers = %v: edit internal/openapi/build.go and internal/server/mount_openapi.go", doc["servers"])
	}
	schemes := doc["components"].(map[string]any)["securitySchemes"].(map[string]any)
	if b, _ := schemes["bearer"].(map[string]any); b["type"] != "http" || b["scheme"] != "bearer" {
		t.Fatalf("bearer scheme = %v: edit internal/openapi/build.go", schemes["bearer"])
	}

	again, rawAgain := e.openapiDoc()
	if string(raw) != string(rawAgain) || !reflect.DeepEqual(doc, again) {
		t.Fatal("document changes between requests: make internal/openapi/build.go deterministic")
	}
}

func TestOpenAPIRequiresOwnerToken(t *testing.T) {
	e := contractEnv(t)
	agent := e.register("w1", "worker", "claude-code")
	for name, tok := range map[string]string{"none": "", "garbage": "nope", "agent": agent} {
		req, _ := http.NewRequest(http.MethodGet, e.ts.URL+openapiPath, nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		want := http.StatusUnauthorized
		if name == "agent" {
			want = http.StatusForbidden
		}
		if resp.StatusCode != want {
			t.Errorf("%s token: status %d, want %d: fix auth of the openapi mount in internal/server/mount_openapi.go", name, resp.StatusCode, want)
		}
	}
}

func TestOpenAPIListsMCPToolsPerRole(t *testing.T) {
	e := contractEnv(t)
	doc, _ := e.openapiDoc()
	type tool struct {
		roles  []string
		schema any
	}
	want := map[string]*tool{}
	for _, role := range []string{"leader", "worker", "tester", "reviewer"} {
		tok := e.register("oa-"+role, role, "other")
		res, _ := e.rpc(tok, "tools/list", map[string]any{})["result"].(map[string]any)
		list, _ := res["tools"].([]any)
		if len(list) == 0 {
			t.Fatalf("no tools for %s: check internal/mcp/registry.go", role)
		}
		for _, x := range list {
			m := x.(map[string]any)
			name := m["name"].(string)
			if want[name] == nil {
				want[name] = &tool{schema: m["inputSchema"]}
			}
			want[name].roles = append(want[name].roles, role)
		}
	}
	listed, _ := doc["x-mcp-tools"].([]any)
	if len(listed) != len(want) {
		t.Fatalf("x-mcp-tools lists %d tools, tools/list exposes %d: fix mcpTools in internal/server/mount_openapi.go", len(listed), len(want))
	}
	for _, x := range listed {
		m := x.(map[string]any)
		name := m["name"].(string)
		w := want[name]
		if w == nil {
			t.Errorf("tool %s is in x-mcp-tools but not exposed over MCP: fix mcpTools in internal/server/mount_openapi.go", name)
			continue
		}
		var roles []string
		for _, r := range m["roles"].([]any) {
			roles = append(roles, r.(string))
		}
		sort.Strings(w.roles)
		if !slices.Equal(roles, w.roles) {
			t.Errorf("tool %s roles = %v, tools/list says %v: fix mcpTools in internal/server/mount_openapi.go", name, roles, w.roles)
		}
		if !reflect.DeepEqual(m["inputSchema"], w.schema) {
			t.Errorf("tool %s inputSchema differs from tools/list: fix mcpTools in internal/server/mount_openapi.go", name)
		}
	}
}

func TestOpenAPICoversEveryRegisteredRoute(t *testing.T) {
	ops := openapi.Routes()
	for _, p := range openapiRegisteredPatterns(t) {
		if !openapiHasOperation(p, ops) {
			t.Errorf("registered pattern %q has no operation: add it to internal/openapi/operations.go (and name new schema types in internal/openapi/names.go)", p)
		}
	}
	for _, r := range contractRoutes(t) {
		if !openapiHasOperation(r.method+" "+r.path, ops) {
			t.Errorf("documented route %s %s has no operation: add it to internal/openapi/operations.go, or remove the row from docs/api.md", r.method, r.path)
		}
	}
}

func TestOpenAPIDocumentedRoutesAreServedOperations(t *testing.T) {
	e := contractEnv(t)
	doc, _ := e.openapiDoc()
	served := openapiOps(t, doc)
	for _, r := range contractRoutes(t) {
		if !served[r.method+" "+r.path] {
			t.Errorf("documented route %s %s is missing from the served document: add an operation in internal/openapi/operations.go and mount the route in internal/server/mount_*.go, or remove the row from docs/api.md", r.method, r.path)
		}
	}
}

func TestOpenAPIOperationsExistOnServer(t *testing.T) {
	e := contractEnv(t)
	doc, _ := e.openapiDoc()
	var keys []string
	for k := range openapiOps(t, doc) {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	checked := 0
	for _, k := range keys {
		method, path, _ := strings.Cut(k, " ")
		if !strings.HasPrefix(path, "/api/v1/") || path == "/api/v1/events" {
			continue
		}
		checked++
		t.Run(k, func(t *testing.T) {
			e.t = t
			code, body := e.contractDo(method, contractDummyPath(path))
			if contractRouteMissing(code, body) {
				t.Fatalf("%s is in the document but not served (%d %s): remove the operation from internal/openapi/operations.go or mount the route in internal/server/mount_*.go", k, code, body)
			}
			if code >= 500 {
				t.Fatalf("%s answered %d %s: fix the handler registered in internal/server/mount_*.go", k, code, body)
			}
		})
	}
	if checked < 100 {
		t.Fatalf("checked only %d operations: internal/openapi/operations.go lost entries", checked)
	}
}

func TestOpenAPIOnlyListsMountedRoutes(t *testing.T) {
	plain, _ := contractEnv(t).openapiDoc()
	ops := openapiOps(t, plain)
	for _, k := range []string{"GET /api/v1/autopilot", "POST /hooks/github", "POST /hooks/forgejo"} {
		if ops[k] {
			t.Errorf("%s is listed although it is not mounted: fix muxRegistered in internal/server/mount_openapi.go", k)
		}
	}
	if !ops["GET /healthz"] || !ops["POST /mcp"] || !ops["GET /api/v1/metrics"] {
		t.Fatalf("always-mounted routes missing from the document: add them to internal/openapi/operations.go: %v", ops)
	}

	t.Setenv("SUBSPACE_AUTOPILOT", "on")
	t.Setenv("SUBSPACE_WEBHOOK_SECRET_GITHUB", strings.Repeat("s", 32))
	e := newFlowEnv(t, func(c *Config) { c.Spawner = spawner.Noop{} })
	enabled, raw := e.openapiDoc()
	if probs := openapi.Validate(raw); len(probs) > 0 {
		t.Fatalf("invalid document; fix internal/openapi/operations.go:\n%s", strings.Join(probs, "\n"))
	}
	ops = openapiOps(t, enabled)
	for _, k := range []string{"GET /api/v1/autopilot", "POST /api/v1/autopilot/briefs/{id}", "POST /hooks/github"} {
		if !ops[k] {
			t.Errorf("%s is mounted but not listed: add it to internal/openapi/operations.go", k)
		}
	}
	if ops["POST /hooks/forgejo"] {
		t.Error("forgejo webhook listed without a secret: fix muxRegistered in internal/server/mount_openapi.go")
	}
	code, body := e.contractDo("GET", "/api/v1/autopilot")
	if contractRouteMissing(code, body) {
		t.Fatalf("autopilot listed but missing (%d %s): fix internal/server/mount_autopilot.go or internal/openapi/operations.go", code, body)
	}
}

func TestOpenAPISchemasMatchEncodedObjects(t *testing.T) {
	e := contractEnv(t)
	lead := e.register("rex", "leader", "other")
	wk := e.register("w1", "worker", "claude-code")
	bid := e.brief(map[string]any{})
	plan := e.must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{planTask("a", nil)}})["plan"].(map[string]any)
	e.restWant(200, "POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{})
	tid := plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
	e.must(lead, "assign", map[string]any{"task_id": tid, "agent_id": "w1"})
	e.must(wk, "claim_task", map[string]any{"task_id": tid})
	e.must(wk, "add_note", map[string]any{"task_id": tid, "body": "note"})
	e.must(wk, "ask_owner", map[string]any{"task_id": tid, "question": "which?", "options": []string{"a", "b"}})

	first := func(out map[string]any, key string) map[string]any {
		list, _ := out[key].([]any)
		if len(list) == 0 {
			t.Fatalf("no %s in %v", key, out)
		}
		return list[0].(map[string]any)
	}
	doc, _ := e.openapiDoc()
	defs := doc["components"].(map[string]any)["schemas"].(map[string]any)
	samples := map[string]map[string]any{
		"Brief":     e.restWant(200, "GET", "/api/v1/briefs/"+bid, nil),
		"Task":      e.restWant(200, "GET", "/api/v1/tasks/"+tid, nil),
		"Plan":      e.restWant(200, "GET", "/api/v1/plans/"+plan["id"].(string), nil),
		"Agent":     e.restWant(200, "GET", "/api/v1/agents/w1", nil),
		"Message":   first(e.restWant(200, "GET", "/api/v1/threads/task/"+tid+"/messages", nil), "messages"),
		"Question":  first(e.restWant(200, "GET", "/api/v1/questions", nil), "questions"),
		"Charter":   e.restWant(200, "GET", "/api/v1/charter", nil),
		"StateInfo": e.restWant(200, "GET", "/api/v1/state", nil),
	}
	for name, sample := range samples {
		t.Run(name, func(t *testing.T) {
			def, ok := defs[name].(map[string]any)
			if !ok {
				t.Fatalf("component %s missing from the document: name the type in internal/openapi/names.go", name)
			}
			props := def["properties"].(map[string]any)
			for k := range sample {
				if _, ok := props[k]; !ok {
					t.Errorf("%s: encoded field %s is not in the schema: add the json field to the Go type behind it (or to internal/openapi/types.go for wrappers)", name, k)
				}
			}
			required, _ := def["required"].([]any)
			for _, r := range required {
				if _, ok := sample[r.(string)]; !ok {
					t.Errorf("%s: required field %s is not encoded: mark it omitempty in the Go type behind it (or in internal/openapi/types.go for wrappers)", name, r)
				}
			}
		})
	}
}
