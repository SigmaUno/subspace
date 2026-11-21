package openapi_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func parse(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("fixture: %v\n%s", err, raw)
	}
	return m
}

func keys(changes []change, breaking bool) []string {
	var out []string
	for _, c := range changes {
		if c.breaking == breaking {
			out = append(out, c.key)
		}
	}
	return out
}

func fixture(path, method, status, body string, schemas string) string {
	return `{"paths":{"` + path + `":{"` + method + `":{"responses":{"` + status +
		`":{"content":{"application/json":{"schema":` + body + `}}},"default":{"$ref":"#/components/responses/Error"}}}}},` +
		`"components":{"schemas":` + schemas + `}}`
}

func TestClassifyDocs(t *testing.T) {
	task := `{"Task":{"type":"object","required":["id","status"],"properties":{"id":{"type":"string"},"status":{"type":"string"},"note":{"type":["string","null"]}}}}`
	ref := `{"$ref":"#/components/schemas/Task"}`
	cases := []struct {
		name         string
		before       string
		after        string
		wantBreaking []string
		wantAdditive []string
	}{
		{
			name:   "identical",
			before: fixture("/t", "get", "200", ref, task),
			after:  fixture("/t", "get", "200", ref, task),
		},
		{
			name:         "removed route",
			before:       fixture("/t", "get", "200", ref, task),
			after:        `{"paths":{}}`,
			wantBreaking: []string{"removed operation GET /t"},
		},
		{
			name:         "removed method keeps the path",
			before:       `{"paths":{"/t":{"get":{"responses":{"200":{"description":"ok"}}},"delete":{"responses":{"204":{"description":"ok"}}}}}}`,
			after:        `{"paths":{"/t":{"get":{"responses":{"200":{"description":"ok"}}}}}}`,
			wantBreaking: []string{"removed operation DELETE /t"},
		},
		{
			name:         "removed response field",
			before:       fixture("/t", "get", "200", ref, task),
			after:        fixture("/t", "get", "200", ref, `{"Task":{"type":"object","required":["id"],"properties":{"id":{"type":"string"},"note":{"type":["string","null"]}}}}`),
			wantBreaking: []string{"removed response field Task.status"},
		},
		{
			name:         "retyped response field",
			before:       fixture("/t", "get", "200", ref, task),
			after:        fixture("/t", "get", "200", ref, strings.Replace(task, `"id":{"type":"string"}`, `"id":{"type":"integer"}`, 1)),
			wantBreaking: []string{"retyped response field Task.id: string -> integer"},
		},
		{
			name:         "response field becomes nullable",
			before:       fixture("/t", "get", "200", ref, task),
			after:        fixture("/t", "get", "200", ref, strings.Replace(task, `"status":{"type":"string"}`, `"status":{"type":["string","null"]}`, 1)),
			wantBreaking: []string{"retyped response field Task.status: string -> null|string"},
		},
		{
			name:   "response field loses nullability is safe",
			before: fixture("/t", "get", "200", ref, task),
			after:  fixture("/t", "get", "200", ref, strings.Replace(task, `"note":{"type":["string","null"]}`, `"note":{"type":"string"}`, 1)),
		},
		{
			name:         "changed status code",
			before:       fixture("/t", "post", "200", ref, task),
			after:        fixture("/t", "post", "201", ref, task),
			wantBreaking: []string{"changed status code of POST /t: 200 -> 201"},
		},
		{
			name:         "removed response status",
			before:       fixture("/t", "post", "200", ref, task),
			after:        `{"paths":{"/t":{"post":{"responses":{"default":{"description":"x"}}}}}}`,
			wantBreaking: []string{"removed response status 200 of POST /t"},
		},
		{
			name:         "removed field of a nested array item",
			before:       fixture("/t", "get", "200", `{"type":"object","properties":{"items":{"type":"array","items":{"type":"object","properties":{"a":{"type":"string"}}}}}}`, `{}`),
			after:        fixture("/t", "get", "200", `{"type":"object","properties":{"items":{"type":"array","items":{"type":"object","properties":{}}}}}`, `{}`),
			wantBreaking: []string{"removed response field GET /t 200.items[].a"},
		},
		{
			name:         "shared component is reported once",
			before:       `{"paths":{"/a":{"get":{"responses":{"200":{"content":{"application/json":{"schema":` + ref + `}}}}}},"/b":{"get":{"responses":{"200":{"content":{"application/json":{"schema":` + ref + `}}}}}}},"components":{"schemas":` + task + `}}`,
			after:        `{"paths":{"/a":{"get":{"responses":{"200":{"content":{"application/json":{"schema":` + ref + `}}}}}},"/b":{"get":{"responses":{"200":{"content":{"application/json":{"schema":` + ref + `}}}}}}},"components":{"schemas":{"Task":{"type":"object","properties":{"id":{"type":"string"},"note":{"type":["string","null"]}}}}}}`,
			wantBreaking: []string{"removed response field Task.status"},
		},
		{
			name:         "new required request field",
			before:       requestFixture(`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}`),
			after:        requestFixture(`{"type":"object","properties":{"title":{"type":"string"},"repo":{"type":"string"}},"required":["repo","title"]}`),
			wantBreaking: []string{"new required request field POST /t request.repo"},
		},
		{
			name:         "existing request field becomes required",
			before:       requestFixture(`{"type":"object","properties":{"title":{"type":"string"},"repo":{"type":"string"}},"required":["title"]}`),
			after:        requestFixture(`{"type":"object","properties":{"title":{"type":"string"},"repo":{"type":"string"}},"required":["repo","title"]}`),
			wantBreaking: []string{"new required request field POST /t request.repo"},
		},
		{
			name:         "new optional request field is additive",
			before:       requestFixture(`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}`),
			after:        requestFixture(`{"type":"object","properties":{"title":{"type":"string"},"repo":{"type":"string"}},"required":["title"]}`),
			wantAdditive: []string{"added request field POST /t request.repo"},
		},
		{
			name:         "request enum narrowed",
			before:       requestFixture(`{"type":"object","properties":{"role":{"type":"string","enum":["worker","tester","reviewer"]}}}`),
			after:        requestFixture(`{"type":"object","properties":{"role":{"type":"string","enum":["worker","tester"]}}}`),
			wantBreaking: []string{"narrowed enum POST /t request.role: removed reviewer"},
		},
		{
			name:         "request enum widened is not breaking",
			before:       requestFixture(`{"type":"object","properties":{"role":{"type":"string","enum":["worker"]}}}`),
			after:        requestFixture(`{"type":"object","properties":{"role":{"type":"string","enum":["worker","tester"]}}}`),
			wantAdditive: nil,
		},
		{
			name:         "request field retyped",
			before:       requestFixture(`{"type":"object","properties":{"n":{"type":"integer"}}}`),
			after:        requestFixture(`{"type":"object","properties":{"n":{"type":"string"}}}`),
			wantBreaking: []string{"retyped request field POST /t request.n: integer -> string"},
		},
		{
			name:         "new required parameter",
			before:       `{"paths":{"/t":{"get":{"parameters":[{"name":"a","in":"query"}],"responses":{}}}}}`,
			after:        `{"paths":{"/t":{"get":{"parameters":[{"name":"a","in":"query"},{"name":"b","in":"query","required":true},{"name":"c","in":"query"}],"responses":{}}}}}`,
			wantBreaking: []string{"new required parameter query:b of GET /t"},
			wantAdditive: []string{"added parameter query:c of GET /t"},
		},
		{
			name:         "additive routes fields and statuses",
			before:       fixture("/t", "get", "200", ref, task),
			after:        `{"paths":{"/t":{"get":{"responses":{"200":{"content":{"application/json":{"schema":` + ref + `}}},"404":{"description":"x"}}}},"/u":{"post":{"responses":{"201":{"description":"x"}}}}},"components":{"schemas":{"Task":{"type":"object","required":["id","status"],"properties":{"id":{"type":"string"},"status":{"type":"string"},"note":{"type":["string","null"]},"extra":{"type":"string"}}}}}}`,
			wantAdditive: []string{"added operation POST /u", "added response field Task.extra", "added response status 404 of GET /t"},
		},
		{
			name:         "recursive schemas terminate",
			before:       fixture("/t", "get", "200", `{"$ref":"#/components/schemas/Node"}`, `{"Node":{"type":"object","properties":{"next":{"oneOf":[{"$ref":"#/components/schemas/Node"},{"type":"null"}]},"v":{"type":"string"}}}}`),
			after:        fixture("/t", "get", "200", `{"$ref":"#/components/schemas/Node"}`, `{"Node":{"type":"object","properties":{"next":{"oneOf":[{"$ref":"#/components/schemas/Node"},{"type":"null"}]}}}}`),
			wantBreaking: []string{"removed response field Node.v"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyDocs(parse(t, c.before), parse(t, c.after))
			if b := keys(got, true); !slices.Equal(b, c.wantBreaking) {
				t.Errorf("breaking = %q, want %q", b, c.wantBreaking)
			}
			if a := keys(got, false); !slices.Equal(a, c.wantAdditive) {
				t.Errorf("additive = %q, want %q", a, c.wantAdditive)
			}
		})
	}
}

func requestFixture(schema string) string {
	return `{"paths":{"/t":{"post":{"requestBody":{"content":{"application/json":{"schema":` + schema + `}}},"responses":{"200":{"description":"ok"}}}}}}`
}

func TestClassifyTools(t *testing.T) {
	before := `{"leader":{"assign":{"type":"object","required":["task_id"],"properties":{"task_id":{"type":"string"},"as":{"type":"string","enum":["worker","reviewer"]}}},"board":{"type":"object"}},
		"worker":{"submit":{"type":"object","properties":{"pr_url":{"type":"string"}}}}}`
	after := `{"leader":{"assign":{"type":"object","required":["agent_id","task_id"],"properties":{"task_id":{"type":"integer"},"agent_id":{"type":"string"},"as":{"type":"string","enum":["worker"]}}},"capacity":{"type":"object"}},
		"worker":{},"reviewer":{"review":{"type":"object"}}}`
	got := classifyTools(parse(t, before), parse(t, after))
	wantBreaking := []string{
		"narrowed enum tool leader/assign.as: removed reviewer",
		"new required request field tool leader/assign.agent_id",
		"removed tool leader/board",
		"removed tool worker/submit",
		"retyped request field tool leader/assign.task_id: string -> integer",
	}
	wantAdditive := []string{"added tool leader/capacity", "added tool reviewer/review"}
	if b := keys(got, true); !slices.Equal(b, wantBreaking) {
		t.Errorf("breaking = %q, want %q", b, wantBreaking)
	}
	if a := keys(got, false); !slices.Equal(a, wantAdditive) {
		t.Errorf("additive = %q, want %q", a, wantAdditive)
	}
}

func TestAllowlist(t *testing.T) {
	allow, err := parseAllowlist("# header\n\nremoved operation GET /old :: replaced by /v2/old\nremoved tool worker/x :: folded into submit\n")
	if err != nil {
		t.Fatal(err)
	}
	changes := []change{
		{key: "removed operation GET /old", breaking: true},
		{key: "removed operation GET /other", breaking: true},
		{key: "removed tool worker/x", breaking: true},
		{key: "added operation GET /new"},
	}
	if bad := unallowed(changes, allow); !slices.Equal(bad, []string{"removed operation GET /other"}) {
		t.Fatalf("unallowed = %q", bad)
	}
	for _, bad := range []string{"removed operation GET /old", "removed operation GET /old :: ", " :: reason"} {
		if _, err := parseAllowlist(bad); err == nil {
			t.Errorf("accepted %q without a reason", bad)
		}
	}
}
