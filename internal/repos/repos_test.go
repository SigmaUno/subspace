package repos_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
	"github.com/SigmaUno/subspace/internal/spawner"
)

const reposPath = "/api/v1/repos"

func start(t *testing.T) *servertest.Env {
	t.Helper()
	return servertest.Start(t, server.Config{PublicURL: "http://sub.test", Spawner: spawner.Noop{},
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})
}

func rest(t *testing.T, e *servertest.Env, want int, method, path string, body any) map[string]any {
	t.Helper()
	code, out := e.REST(method, path, body)
	if code != want {
		t.Fatalf("%s %s: %d, want %d: %v", method, path, code, want, out)
	}
	return out
}

func errorOf(out map[string]any) (code, message string) {
	e, _ := out["error"].(map[string]any)
	code, _ = e["code"].(string)
	message, _ = e["message"].(string)
	return code, message
}

func names(t *testing.T, e *servertest.Env) []string {
	t.Helper()
	out := []string{}
	for _, r := range rest(t, e, 200, "GET", reposPath, nil)["repos"].([]any) {
		out = append(out, r.(map[string]any)["name"].(string))
	}
	return out
}

func repo(name string, extra map[string]any) map[string]any {
	m := map[string]any{"name": name, "host": "forgejo", "base_url": "https://forge.example.com"}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func importDoc(t *testing.T, e *servertest.Env, query, doc string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest("POST", e.TS.URL+"/api/v1/briefs/import"+query, strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+servertest.OwnerToken)
	req.Header.Set("Content-Type", "text/markdown")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestRegistryCRUD(t *testing.T) {
	e := start(t)
	if got := names(t, e); len(got) != 0 {
		t.Fatalf("a fresh registry holds %v", got)
	}
	created := rest(t, e, 201, "POST", reposPath, repo("SigmaUno/subspace", map[string]any{
		"host": "github", "base_url": "", "protected_branches": []string{"main", "release/*"},
		"merge_test_command": []string{"make", "test"}, "merge_work_dir": "/data/mq/", "token_env": "GITHUB_TOKEN",
	}))
	if created["base_url"] != "https://github.com" || created["default_branch"] != "main" || created["enabled"] != true ||
		created["merge_work_dir"] != "/data/mq" || created["token_env"] != "GITHUB_TOKEN" {
		t.Fatalf("created = %v", created)
	}
	rest(t, e, 201, "POST", reposPath, repo("acme/api", map[string]any{"enabled": false}))
	if got := fmt.Sprint(names(t, e)); got != "[SigmaUno/subspace acme/api]" {
		t.Fatalf("names = %s", got)
	}
	if out := rest(t, e, 409, "POST", reposPath, repo("sigmauno/SUBSPACE", nil)); out["error"].(map[string]any)["code"] != "conflict" {
		t.Fatalf("duplicate: %v", out)
	}

	got := rest(t, e, 200, "GET", reposPath+"/sigmauno/subspace", nil)
	if got["name"] != "SigmaUno/subspace" || fmt.Sprint(got["protected_branches"]) != "[main release/*]" {
		t.Fatalf("get = %v", got)
	}
	updated := rest(t, e, 200, "PUT", reposPath+"/SigmaUno/subspace", map[string]any{"host": "github", "default_branch": "trunk"})
	if updated["default_branch"] != "trunk" || fmt.Sprint(updated["protected_branches"]) != "[]" || updated["created_at"] != created["created_at"] ||
		updated["updated_at"] == nil {
		t.Fatalf("put must replace every field: %v", updated)
	}
	if out := rest(t, e, 400, "PUT", reposPath+"/SigmaUno/subspace", map[string]any{"name": "SigmaUno/other", "host": "github"}); out["error"] == nil {
		t.Fatalf("rename: %v", out)
	}
	rest(t, e, 404, "PUT", reposPath+"/ghost/repo", map[string]any{"host": "github"})
	rest(t, e, 404, "GET", reposPath+"/ghost/repo", nil)
	rest(t, e, 404, "GET", reposPath+"/ghost/repo/health", nil)

	rest(t, e, 204, "DELETE", reposPath+"/acme/api", nil)
	rest(t, e, 404, "DELETE", reposPath+"/acme/api", nil)
	if got := fmt.Sprint(names(t, e)); got != "[SigmaUno/subspace]" {
		t.Fatalf("names after delete = %s", got)
	}

	for _, tool := range []string{"create_repo", "update_repo", "delete_repo"} {
		events := rest(t, e, 200, "GET", "/api/v1/audit?tool="+tool, nil)["events"].([]any)
		if len(events) == 0 || !strings.HasPrefix(events[0].(map[string]any)["note"].(string), "token=root; repo ") {
			t.Fatalf("audit %s = %v", tool, events)
		}
	}
	feed := rest(t, e, 200, "GET", "/api/v1/activity", nil)
	if !strings.Contains(fmt.Sprint(feed), "registered repository SigmaUno/subspace") {
		t.Fatalf("activity = %v", feed)
	}

	viewer := rest(t, e, 201, "POST", "/api/v1/tokens", map[string]any{"name": "v", "scope": "viewer"})["secret"].(string)
	if code, _ := e.RESTAs(viewer, "GET", reposPath, nil); code != 200 {
		t.Fatalf("viewer read: %d", code)
	}
	if code, _ := e.RESTAs(viewer, "POST", reposPath, repo("acme/web", nil)); code != 403 {
		t.Fatalf("viewer write: %d", code)
	}
	if code, _ := e.RESTAs("", "GET", reposPath, nil); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
}

func TestRegistryValidation(t *testing.T) {
	e := start(t)
	const secret = "ghp_16C7e42F292c6912E7710c838347Ae178B4a"
	for name, body := range map[string]map[string]any{
		"no owner":            repo("subspace", nil),
		"extra segment":       repo("a/b/c", nil),
		"dot owner":           repo("../b", nil),
		"bad characters":      repo("a/b c", nil),
		"unknown host":        repo("a/b", map[string]any{"host": "gitlab"}),
		"forgejo no url":      repo("a/b", map[string]any{"base_url": ""}),
		"url scheme":          repo("a/b", map[string]any{"base_url": "ftp://forge.example.com"}),
		"url credentials":     repo("a/b", map[string]any{"base_url": "https://u:p@forge.example.com"}),
		"url query":           repo("a/b", map[string]any{"base_url": "https://forge.example.com?x=1"}),
		"bad default branch":  repo("a/b", map[string]any{"default_branch": "a..b"}),
		"token as token_env":  repo("a/b", map[string]any{"token_env": secret}),
		"lowercase token_env": repo("a/b", map[string]any{"token_env": "forge_token"}),
		"token_env expr":      repo("a/b", map[string]any{"token_env": "$TOKEN"}),
		"matches task branch": repo("a/b", map[string]any{"protected_branches": []string{"sub/*"}}),
		"matches all":         repo("a/b", map[string]any{"protected_branches": []string{"**"}}),
		"bad pattern":         repo("a/b", map[string]any{"protected_branches": []string{"a b"}}),
		"relative work dir":   repo("a/b", map[string]any{"merge_work_dir": "mq"}),
		"empty command arg":   repo("a/b", map[string]any{"merge_test_command": []string{"make", ""}}),
		"unknown field":       repo("a/b", map[string]any{"token": secret}),
	} {
		code, out := e.REST("POST", reposPath, body)
		if code != 400 {
			t.Errorf("%s: %d %v", name, code, out)
		}
		if strings.Contains(fmt.Sprint(out), secret) {
			t.Errorf("%s: the error echoes the secret: %v", name, out)
		}
	}
	if got := names(t, e); len(got) != 0 {
		t.Fatalf("invalid repositories were stored: %v", got)
	}
}

type host struct {
	srv     *httptest.Server
	mu      sync.Mutex
	auth    []string
	repo    map[string]any
	scopes  string
	status  int
	branchs map[string]bool
}

func newHost(t *testing.T, prefix string) *host {
	t.Helper()
	h := &host{branchs: map[string]bool{"main": true}, status: 200, repo: map[string]any{"default_branch": "main", "permissions": map[string]any{"push": true}}}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.auth = append(h.auth, r.Header.Get("Authorization"))
		if r.Method != http.MethodGet {
			t.Errorf("health check sent %s %s", r.Method, r.URL.Path)
		}
		path := strings.TrimPrefix(r.URL.Path, prefix)
		switch {
		case h.status != 200:
			w.WriteHeader(h.status)
		case path == "/repos/o/r":
			if h.scopes != "" {
				w.Header().Set("X-OAuth-Scopes", h.scopes)
			}
			_ = json.NewEncoder(w).Encode(h.repo)
		case strings.HasPrefix(path, "/repos/o/r/branches/") && h.branchs[strings.TrimPrefix(path, "/repos/o/r/branches/")]:
			_, _ = w.Write([]byte(`{"name":"x"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *host) set(f func(*host)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f(h)
}

func TestHealthThroughFakeHosts(t *testing.T) {
	const token = "tok-super-secret-value-123"
	t.Setenv("FAKE_HOST_TOKEN", token)
	e := start(t)
	for _, tc := range []struct{ host, prefix string }{{"forgejo", "/api/v1"}, {"github", "/api/v3"}} {
		t.Run(tc.host, func(t *testing.T) {
			h := newHost(t, tc.prefix)
			rest(t, e, 201, "POST", reposPath, repo("o/r", map[string]any{"host": tc.host, "base_url": h.srv.URL, "token_env": "FAKE_HOST_TOKEN"}))
			t.Cleanup(func() { rest(t, e, 204, "DELETE", reposPath+"/o/r", nil) })

			health := func() map[string]any { return rest(t, e, 200, "GET", reposPath+"/o/r/health", nil) }
			got := health()
			if got["ok"] != true || got["reachable"] != true || got["token_configured"] != true || got["default_branch_exists"] != true ||
				got["host_default_branch"] != "main" || got["token_hint"] != "token has write access" || got["error"] != "" {
				t.Fatalf("healthy = %v", got)
			}
			want := "token " + token
			if tc.host == "github" {
				want = "Bearer " + token
			}
			h.mu.Lock()
			if len(h.auth) != 2 || h.auth[0] != want {
				t.Fatalf("authorization headers = %q", h.auth)
			}
			h.mu.Unlock()

			h.set(func(h *host) {
				h.repo = map[string]any{"default_branch": "trunk", "permissions": map[string]any{"push": false}}
				h.scopes = "public_repo"
				h.branchs = map[string]bool{}
			})
			got = health()
			if got["ok"] != false || got["default_branch_exists"] != false || got["host_default_branch"] != "trunk" ||
				!strings.Contains(got["token_hint"].(string), "read-only") || !strings.Contains(got["token_hint"].(string), "public_repo") {
				t.Fatalf("read-only token, missing branch = %v", got)
			}

			for status, hint := range map[int]string{401: "rejected the token", 404: "cannot see it"} {
				h.set(func(h *host) { h.status = status })
				got = health()
				if got["ok"] != false || got["reachable"] != true || got["error"] == "" {
					t.Fatalf("status %d = %v", status, got)
				}
				if status == 401 && !strings.Contains(got["token_hint"].(string), hint) || status == 404 && !strings.Contains(got["error"].(string), hint) {
					t.Fatalf("status %d = %v", status, got)
				}
			}
		})
	}

	t.Run("unreachable", func(t *testing.T) {
		h := newHost(t, "/api/v1")
		url := h.srv.URL
		h.srv.Close()
		rest(t, e, 201, "POST", reposPath, repo("o/r", map[string]any{"base_url": url, "token_env": "FAKE_HOST_TOKEN"}))
		got := rest(t, e, 200, "GET", reposPath+"/o/r/health", nil)
		if got["ok"] != false || got["reachable"] != false || !strings.Contains(got["error"].(string), "not reachable") {
			t.Fatalf("unreachable = %v", got)
		}
		if raw, _ := json.Marshal(got); strings.Contains(string(raw), token) {
			t.Fatalf("health leaks the token: %s", raw)
		}
	})

	t.Run("token variable unset", func(t *testing.T) {
		h := newHost(t, "/api/v1")
		rest(t, e, 200, "PUT", reposPath+"/o/r", repo("o/r", map[string]any{"base_url": h.srv.URL, "token_env": "FAKE_UNSET_TOKEN"}))
		got := rest(t, e, 200, "GET", reposPath+"/o/r/health", nil)
		if got["token_configured"] != false || !strings.Contains(got["token_hint"].(string), "FAKE_UNSET_TOKEN is not set") || got["ok"] != true {
			t.Fatalf("unset token = %v", got)
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.auth[0] != "" {
			t.Fatalf("an unset token variable still sent credentials: %q", h.auth)
		}
	})
}

func TestTaskReposAreCheckedAgainstTheRegistry(t *testing.T) {
	e := start(t)
	lead := e.Register("rex", "leader", "other")
	task := func(repo string) map[string]any {
		return map[string]any{"title": "Add login", "spec": "build it", "repo": repo}
	}
	brief := func(repos ...string) map[string]any {
		return map[string]any{"title": "Demo", "body": "b", "repos": repos, "max_cost_eur": 10, "max_parallel_agents": 5}
	}

	rest(t, e, 201, "POST", "/api/v1/briefs", brief("anything/goes"))
	e.Must(lead, "propose_plan", map[string]any{"brief_id": "B-1", "tasks": []any{task("anything/goes")}})

	rest(t, e, 201, "POST", reposPath, repo("o/r", nil))
	rest(t, e, 201, "POST", reposPath, repo("o/off", map[string]any{"enabled": false}))

	out := rest(t, e, 400, "POST", "/api/v1/briefs", brief("o/r", "o/missing"))
	if code, msg := errorOf(out); code != "bad_request" || !strings.Contains(msg, `"o/missing"`) || !strings.Contains(msg, "known repos: o/off, o/r") {
		t.Fatalf("brief with an unknown repo: %v", out)
	}
	rest(t, e, 400, "POST", "/api/v1/briefs", brief("o/off"))
	rest(t, e, 201, "POST", "/api/v1/briefs", brief("O/R"))

	open := rest(t, e, 201, "POST", "/api/v1/briefs", brief())["id"].(string)
	e.MustFail(lead, "propose_plan", map[string]any{"brief_id": open, "tasks": []any{task("o/r"), task("o/missing")}}, "bad_request")
	e.MustFail(lead, "propose_plan", map[string]any{"brief_id": open, "tasks": []any{task("o/off")}}, "bad_request")
	plans := rest(t, e, 200, "GET", "/api/v1/plans?brief_id="+open, nil)["plans"].([]any)
	if len(plans) != 0 {
		t.Fatalf("a rejected plan was stored: %v", plans)
	}
	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": open, "tasks": []any{task("o/r")}})["plan"].(map[string]any)
	planID := plan["id"].(string)
	taskID := plan["tasks"].([]any)[0].(map[string]any)["id"].(string)

	rest(t, e, 400, "POST", "/api/v1/plans/"+planID+"/tasks", task("o/missing"))
	rest(t, e, 201, "POST", "/api/v1/plans/"+planID+"/tasks", task("o/r"))
	rest(t, e, 400, "PATCH", "/api/v1/tasks/"+taskID, map[string]any{"repo": "o/missing"})
	rest(t, e, 200, "PATCH", "/api/v1/tasks/"+taskID, map[string]any{"repo": "o/r", "title": "Other title"})

	rest(t, e, 200, "POST", "/api/v1/plans/"+planID+"/approve", map[string]any{})
	e.MustFail(lead, "create_task", map[string]any{"brief_id": open, "title": "More", "spec": "s", "repo": "o/missing"}, "bad_request")
	e.Must(lead, "create_task", map[string]any{"brief_id": open, "title": "More", "spec": "s", "repo": "o/r"})

	doc := func(repo string) string { return "---\ntitle: Imported\nrepos: [" + repo + "]\n---\nbody\n" }
	if code, out := importDoc(t, e, "", doc("o/missing")); code != 400 || !strings.Contains(fmt.Sprint(out), "known repos") {
		t.Fatalf("import of an unknown repo: %d %v", code, out)
	}
	if code, out := importDoc(t, e, "", doc("o/r")); code != 201 {
		t.Fatalf("import: %d %v", code, out)
	}

	rest(t, e, 204, "DELETE", reposPath+"/o/r", nil)
	rest(t, e, 204, "DELETE", reposPath+"/o/off", nil)
	e.Must(lead, "create_task", map[string]any{"brief_id": open, "title": "Free again", "spec": "s", "repo": "o/missing"})
}

func TestProtectedBranchPatternsBlockTaskBranches(t *testing.T) {
	e := start(t)
	lead := e.Register("rex", "leader", "other")
	rest(t, e, 201, "POST", reposPath, repo("o/guarded", map[string]any{"protected_branches": []string{"main", "sub/t-2-*"}}))
	brief := rest(t, e, 201, "POST", "/api/v1/briefs", map[string]any{"title": "Demo", "body": "b", "max_parallel_agents": 5})["id"].(string)
	task := func(title string) map[string]any {
		return map[string]any{"title": title, "spec": "s", "repo": "o/guarded"}
	}

	e.MustFail(lead, "propose_plan", map[string]any{"brief_id": brief, "tasks": []any{task("One"), task("Two")}}, "bad_request")
	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": brief, "tasks": []any{task("One")}})["plan"].(map[string]any)
	id := plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
	if branch := plan["tasks"].([]any)[0].(map[string]any)["branch"].(string); strings.HasPrefix(branch, "sub/t-2-") {
		t.Fatalf("branch %s should have been refused", branch)
	}
	rest(t, e, 200, "PATCH", "/api/v1/tasks/"+id, map[string]any{"title": "Renamed"})
}
