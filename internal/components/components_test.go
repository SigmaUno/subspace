package components_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/components"
	"github.com/SigmaUno/subspace/internal/model"
)

type fakeSource struct {
	tasks  []components.Task
	agents []*model.Agent
}

func (f *fakeSource) Brief(_ context.Context, id string) (*model.Brief, error) {
	if id != "B-1" {
		return nil, model.NotFound("brief %s not found", id)
	}
	return &model.Brief{ID: id}, nil
}

func (f *fakeSource) Tasks(context.Context, string) ([]components.Task, error) {
	return f.tasks, nil
}

func (f *fakeSource) Agents(context.Context) ([]*model.Agent, error) {
	return f.agents, nil
}

func ptr[T any](v T) *T { return &v }

func TestArchitectureEndpoint(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	minutesAgo := func(m int) *time.Time { return ptr(now.Add(-time.Duration(m) * time.Minute)) }

	src := &fakeSource{
		agents: []*model.Agent{
			{ID: "claude-1", Role: model.RoleWorker, Status: model.AgentActive},
			{ID: "codex-1", Role: model.RoleWorker, Status: model.AgentPaused},
			{ID: "rev-1", Role: model.RoleReviewer, Status: model.AgentActive},
		},
		tasks: []components.Task{
			{ComponentID: "C-1", Task: &model.Task{
				ID: "T-1", Status: model.StatusDone, Assignee: ptr("claude-1"), Reviewer: ptr("rev-1"),
				UpdatedAt: *minutesAgo(60),
			}},
			{ComponentID: "C-1", Task: &model.Task{
				ID: "T-2", Status: model.StatusInProgress, Assignee: ptr("claude-1"), ProgressPct: 50,
				LastHeartbeatAt: minutesAgo(1), LeaseExpiresAt: ptr(now.Add(10 * time.Minute)),
				UpdatedAt: *minutesAgo(1),
			}},
			{ComponentID: "C-1", Task: &model.Task{
				ID: "T-3", Status: model.StatusCancelled, Assignee: ptr("ghost"), UpdatedAt: *minutesAgo(90),
			}},
			{ComponentID: "C-2", Task: &model.Task{
				ID: "T-4", Status: model.StatusInProgress, Assignee: ptr("codex-1"), ProgressPct: 20,
				LastHeartbeatAt: minutesAgo(30), UpdatedAt: *minutesAgo(30),
			}},
			{ComponentID: "C-2", Task: &model.Task{
				ID: "T-5", Status: model.StatusBlocked, Assignee: ptr("codex-1"), UpdatedAt: *minutesAgo(10),
			}},
			{ComponentID: "", Task: &model.Task{ID: "T-6", Status: model.StatusReady, UpdatedAt: now}},
		},
	}

	svc := components.NewService(components.NewMemory(), src, func() time.Time { return now })
	_, err := svc.SetPlan(context.Background(), "B-1", []components.Spec{
		{Name: "core", Description: "domain logic", Paths: []string{"internal/core/", "./internal/model"}},
		{Name: "api", Paths: []string{"internal/api"}, DependsOn: []string{"Core"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.StripPrefix("/api/v1", components.Handler(svc)))
	defer srv.Close()

	get := func(path string) (int, []byte) {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, body
	}

	status, body := get("/api/v1/briefs/B-1/architecture")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	var g components.Graph
	if err := json.Unmarshal(body, &g); err != nil {
		t.Fatal(err)
	}

	if len(g.Nodes) != 2 || g.Nodes[0].Name != "core" || g.Nodes[1].Name != "api" {
		t.Fatalf("nodes: %+v", g.Nodes)
	}
	if got := g.Nodes[0].Paths; len(got) != 2 || got[0] != "internal/core" || got[1] != "internal/model" {
		t.Fatalf("paths not cleaned: %v", got)
	}
	if len(g.Edges) != 1 || g.Edges[0] != (components.Edge{From: "C-2", To: "C-1"}) {
		t.Fatalf("edges: %+v", g.Edges)
	}

	core, api := g.Nodes[0], g.Nodes[1]

	if len(core.Owners) != 2 || core.Owners[0].AgentID != "claude-1" || core.Owners[1].AgentID != "rev-1" {
		t.Fatalf("core owners: %+v", core.Owners)
	}
	if o := core.Owners[1]; o.Role != model.RoleReviewer || len(o.TaskIDs) != 1 || o.TaskIDs[0] != "T-1" {
		t.Fatalf("reviewer owner: %+v", o)
	}
	if o := core.Owners[0]; len(o.TaskIDs) != 2 {
		t.Fatalf("claude-1 owner: %+v", o)
	}
	if core.Status.Total != 3 || core.Status.Counts[model.StatusDone] != 1 ||
		core.Status.Counts[model.StatusInProgress] != 1 || core.Status.Counts[model.StatusCancelled] != 1 {
		t.Fatalf("core counts: %+v", core.Status)
	}
	if core.Status.ProgressAvg != 75 || core.Status.Blocked != 0 || core.Status.Stale != 0 {
		t.Fatalf("core rollup: %+v", core.Status)
	}
	if core.Status.LastActivity == nil || !core.Status.LastActivity.Equal(*minutesAgo(1)) {
		t.Fatalf("core last activity: %v", core.Status.LastActivity)
	}

	if len(api.Owners) != 1 || api.Owners[0].AgentID != "codex-1" || api.Owners[0].AgentStatus != model.AgentPaused {
		t.Fatalf("api owners: %+v", api.Owners)
	}
	if api.Status.Blocked != 1 || api.Status.Stale != 1 || api.Status.ProgressAvg != 10 {
		t.Fatalf("api rollup: %+v", api.Status)
	}
	if api.Status.LastActivity == nil || !api.Status.LastActivity.Equal(*minutesAgo(10)) {
		t.Fatalf("api last activity: %v", api.Status.LastActivity)
	}

	status, body = get("/api/v1/briefs/B-9/architecture")
	if status != http.StatusNotFound || !strings.Contains(string(body), `"not_found"`) {
		t.Fatalf("missing brief: %d %s", status, body)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		specs   []components.Spec
		wantErr string
	}{
		{"valid dag", []components.Spec{
			{Name: "a"}, {Name: "b", DependsOn: []string{"a"}}, {Name: "c", DependsOn: []string{"a", "b"}},
		}, ""},
		{"empty plan", nil, "empty"},
		{"blank name", []components.Spec{{Name: "  "}}, "empty name"},
		{"duplicate name ignoring case", []components.Spec{{Name: "api"}, {Name: " API "}}, "duplicate component name"},
		{"unknown dependency", []components.Spec{{Name: "a", DependsOn: []string{"b"}}}, "unknown component"},
		{"self dependency", []components.Spec{{Name: "a", DependsOn: []string{"a"}}}, "itself"},
		{"repeated dependency", []components.Spec{{Name: "a"}, {Name: "b", DependsOn: []string{"a", "A"}}}, "twice"},
		{"two cycle", []components.Spec{
			{Name: "a", DependsOn: []string{"b"}}, {Name: "b", DependsOn: []string{"a"}},
		}, "a -> b -> a"},
		{"long cycle behind valid prefix", []components.Spec{
			{Name: "x"},
			{Name: "a", DependsOn: []string{"x", "b"}},
			{Name: "b", DependsOn: []string{"c"}},
			{Name: "c", DependsOn: []string{"a"}},
		}, "a -> b -> c -> a"},
		{"absolute path", []components.Spec{{Name: "a", Paths: []string{"/etc"}}}, "relative"},
		{"escaping path", []components.Spec{{Name: "a", Paths: []string{"x/../../y"}}}, "escapes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := components.Validate(tc.specs)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got %v, want error containing %q", err, tc.wantErr)
			}
			if model.AsError(err).Code != "bad_request" {
				t.Fatalf("code %q", model.AsError(err).Code)
			}
		})
	}
}

func TestSetPlanRejectsInvalidAndKeepsPrevious(t *testing.T) {
	src := &fakeSource{}
	store := components.NewMemory()
	svc := components.NewService(store, src, nil)
	ctx := context.Background()

	if _, err := svc.SetPlan(ctx, "B-1", []components.Spec{{Name: "a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetPlan(ctx, "B-1", []components.Spec{{Name: "a", DependsOn: []string{"a"}}}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := svc.SetPlan(ctx, "B-2", []components.Spec{{Name: "a"}}); model.AsError(err).Code != "not_found" {
		t.Fatalf("unknown brief: %v", err)
	}
	got, err := store.List(ctx, "B-1")
	if err != nil || len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("stored plan changed: %+v %v", got, err)
	}
}

func TestPutComponentsThenArchitecture(t *testing.T) {
	svc := components.NewService(components.NewMemory(), &fakeSource{}, nil)
	srv := httptest.NewServer(http.StripPrefix("/api/v1", components.Handler(svc)))
	defer srv.Close()

	do := func(method, path, body string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		out, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, out
	}

	status, body := do(http.MethodPut, "/api/v1/briefs/B-1/components",
		`{"components":[{"name":"core","paths":["internal/core/"]},{"name":"api","depends_on":["core"]}]}`)
	if status != http.StatusOK {
		t.Fatalf("put: %d %s", status, body)
	}
	var put struct {
		Components []components.Component `json:"components"`
	}
	if err := json.Unmarshal(body, &put); err != nil {
		t.Fatal(err)
	}
	if len(put.Components) != 2 || put.Components[1].ID != "C-2" || put.Components[1].DependsOn[0] != "C-1" ||
		put.Components[0].Paths[0] != "internal/core" {
		t.Fatalf("components: %+v", put.Components)
	}

	status, body = do(http.MethodGet, "/api/v1/briefs/B-1/architecture", "")
	if status != http.StatusOK {
		t.Fatalf("get: %d %s", status, body)
	}
	var g components.Graph
	if err := json.Unmarshal(body, &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Nodes) != 2 || len(g.Edges) != 1 || g.Edges[0] != (components.Edge{From: "C-2", To: "C-1"}) {
		t.Fatalf("graph: %+v", g)
	}

	for name, tc := range map[string]struct {
		path, body, code string
		status           int
	}{
		"cycle":         {"/api/v1/briefs/B-1/components", `{"components":[{"name":"a","depends_on":["b"]},{"name":"b","depends_on":["a"]}]}`, "bad_request", http.StatusBadRequest},
		"empty":         {"/api/v1/briefs/B-1/components", `{"components":[]}`, "bad_request", http.StatusBadRequest},
		"malformed":     {"/api/v1/briefs/B-1/components", `{`, "bad_request", http.StatusBadRequest},
		"unknown brief": {"/api/v1/briefs/B-9/components", `{"components":[{"name":"a"}]}`, "not_found", http.StatusNotFound},
	} {
		status, body = do(http.MethodPut, tc.path, tc.body)
		if status != tc.status || !strings.Contains(string(body), `"`+tc.code+`"`) {
			t.Fatalf("%s: %d %s", name, status, body)
		}
	}

	_, body = do(http.MethodGet, "/api/v1/briefs/B-1/architecture", "")
	if err := json.Unmarshal(body, &g); err != nil || len(g.Nodes) != 2 {
		t.Fatalf("plan changed after rejected puts: %s", body)
	}
}
