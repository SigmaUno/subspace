package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/testdb"
)

const mirrorToken = "forgejo-test-token"

type forgejoIssue struct {
	Title  string
	Body   string
	State  string
	Labels []int64
}

type forgejoFake struct {
	mu     sync.Mutex
	issues []*forgejoIssue
	labels map[string]int64
	srv    *httptest.Server
}

func newForgejoFake(t *testing.T) *forgejoFake {
	t.Helper()
	f := &forgejoFake{labels: map[string]int64{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *forgejoFake) labelName(id int64) string {
	for n, i := range f.labels {
		if i == id {
			return n
		}
	}
	return ""
}

func (f *forgejoFake) json(n int) map[string]any {
	i := f.issues[n]
	ls := []map[string]any{}
	for _, id := range i.Labels {
		ls = append(ls, map[string]any{"id": id, "name": f.labelName(id)})
	}
	return map[string]any{"number": n + 1, "title": i.Title, "body": i.Body, "state": i.State, "labels": ls,
		"html_url": f.srv.URL + "/o/r/issues/" + strconv.Itoa(n+1)}
}

func (f *forgejoFake) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "token "+mirrorToken {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/repos/o/r")
	var body map[string]any
	if r.Method != http.MethodGet {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	ids := func() []int64 {
		var out []int64
		for _, x := range body["labels"].([]any) {
			out = append(out, int64(x.(float64)))
		}
		return out
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case path == "/labels" && r.Method == http.MethodGet:
		out := []map[string]any{}
		for n, id := range f.labels {
			out = append(out, map[string]any{"id": id, "name": n})
		}
		_ = json.NewEncoder(w).Encode(out)
	case path == "/labels":
		id := int64(len(f.labels) + 1)
		name := body["name"].(string)
		f.labels[name] = id
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "name": name})
	case path == "/issues" && r.Method == http.MethodGet:
		out := []map[string]any{}
		for n, i := range f.issues {
			if strings.Contains(i.Body, r.URL.Query().Get("q")) && r.URL.Query().Get("page") == "1" {
				out = append(out, f.json(n))
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	case path == "/issues":
		f.issues = append(f.issues, &forgejoIssue{Title: body["title"].(string), Body: body["body"].(string), State: "open", Labels: ids()})
		_ = json.NewEncoder(w).Encode(f.json(len(f.issues) - 1))
	case strings.HasPrefix(path, "/issues/"):
		parts := strings.Split(strings.TrimPrefix(path, "/issues/"), "/")
		n, _ := strconv.Atoi(parts[0])
		if n < 1 || n > len(f.issues) {
			http.NotFound(w, r)
			return
		}
		i := f.issues[n-1]
		if len(parts) == 2 {
			i.Labels = ids()
			_, _ = w.Write([]byte("[]"))
			return
		}
		if v, ok := body["title"].(string); ok {
			i.Title = v
		}
		if v, ok := body["body"].(string); ok {
			i.Body = v
		}
		if v, ok := body["state"].(string); ok {
			i.State = v
		}
		_ = json.NewEncoder(w).Encode(f.json(n - 1))
	default:
		http.NotFound(w, r)
	}
}

func (f *forgejoFake) find(taskID string) (forgejoIssue, []string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, i := range f.issues {
		if strings.Contains(i.Body, "subspace:task:"+taskID+" ") {
			var names []string
			for _, id := range i.Labels {
				names = append(names, f.labelName(id))
			}
			return *i, names, true
		}
	}
	return forgejoIssue{}, nil, false
}

func (f *forgejoFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.issues)
}

func (f *forgejoFake) await(t *testing.T, taskID string, cond func(forgejoIssue, []string) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if i, labels, ok := f.find(taskID); ok && cond(i, labels) {
			return
		}
		if time.Now().After(deadline) {
			i, labels, ok := f.find(taskID)
			t.Fatalf("issue for %s not in expected state (found=%v %+v %v)", taskID, ok, i, labels)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func newMirrorEnv(t *testing.T, dbPath string) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s, err := New(ctx, Config{DBPath: dbPath, OwnerToken: ownerTok, PublicURL: "http://sub.test",
		FlushEvery: 20 * time.Millisecond, SweepEvery: 20 * time.Millisecond, Lease: time.Minute})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	go s.Board.Run(ctx)
	ts := httptest.NewServer(s.Handler())
	e := &env{t: t, ts: ts, srv: s}
	t.Cleanup(func() { ts.Close(); cancel(); _ = s.Close() })
	return e
}

func awaitMirrorLink(t *testing.T, e *env, taskID string) {
	t.Helper()
	st := e.srv.Store.(interface {
		MirrorLink(ctx context.Context, taskID string) (repo, url, digest string, ok bool, err error)
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, _, digest, ok, err := st.MirrorLink(context.Background(), taskID)
		if err != nil {
			t.Fatal(err)
		}
		if ok && digest != "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("mapping for %s not stored", taskID)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestMirrorFollowsTaskLifecycleAcrossRestart(t *testing.T) {
	f := newForgejoFake(t)
	t.Setenv("SUBSPACE_MIRROR_FORGEJO_URL", f.srv.URL)
	t.Setenv("SUBSPACE_MIRROR_FORGEJO_TOKEN", mirrorToken)
	t.Setenv("SUBSPACE_MIRROR_FORGEJO_REPO_MAP", "o/r=o/r")
	db := testdb.Path(t, "m.db")

	e := newMirrorEnv(t, db)
	lead := e.register("lead", "leader", "claude-code")
	bid := e.brief(map[string]any{"title": "Auth rewrite"})
	first := e.approvedPlan(lead, bid, planTask("Add login", nil))[0]

	f.await(t, first, func(i forgejoIssue, labels []string) bool {
		return i.State == "open" && i.Title == "Add login" && strings.Contains(i.Body, "Auth rewrite") &&
			slices.Contains(labels, "status/ready") && !slices.Contains(labels, "status/backlog")
	})

	e.restWant(200, "POST", "/api/v1/tasks/"+first+"/cancel", map[string]any{"reason": "dropped"})
	f.await(t, first, func(i forgejoIssue, labels []string) bool {
		return i.State == "closed" && slices.Contains(labels, "status/cancelled")
	})

	awaitMirrorLink(t, e, first)
	e.ts.Close()
	if err := e.srv.Close(); err != nil {
		t.Fatal(err)
	}

	e2 := newMirrorEnv(t, db)
	second := e2.approvedPlan(lead, bid, planTask("Add logout", nil))[0]
	f.await(t, second, func(i forgejoIssue, _ []string) bool { return i.State == "open" })
	e2.restWant(200, "POST", "/api/v1/tasks/"+first+"/reopen", map[string]any{"status": "ready", "note": "again"})
	f.await(t, first, func(i forgejoIssue, labels []string) bool {
		return i.State == "open" && slices.Contains(labels, "status/ready") && !slices.Contains(labels, "status/cancelled")
	})
	if n := f.count(); n != 2 {
		t.Fatalf("issues = %d, want 2", n)
	}
	awaitMirrorLink(t, e2, second)
}

func TestMirrorRejectsIncompleteConfig(t *testing.T) {
	for name, tc := range map[string][3]string{
		"no token":   {"http://forgejo.invalid", "", "o/r=o/r"},
		"no map":     {"http://forgejo.invalid", "tok", ""},
		"bad entry":  {"http://forgejo.invalid", "tok", "o/r"},
		"bad target": {"http://forgejo.invalid", "tok", "o/r=justrepo"},
		"bad url":    {"ftp://forgejo.invalid", "tok", "o/r=o/r"},
	} {
		if _, err := forgejoMirrors(tc[0], tc[1], tc[2]); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
