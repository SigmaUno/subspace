package prsync_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/prsync"
	"github.com/SigmaUno/subspace/internal/ratelimit"
	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

const (
	ghToken = "gh-secret-token-0123456789"
	fjToken = "fj-secret-token-0123456789"
)

type fakePR struct {
	State     string
	Merged    bool
	Draft     bool
	Mergeable *bool
	Head      string
	Commits   []string
	Reviews   []map[string]any
	Status    string
	Runs      []map[string]any
	Ref       string
	Files     []map[string]any
	Authors   map[string]string
}

type fakeHost struct {
	*httptest.Server
	kind, prefix, token string

	mu          sync.Mutex
	prs         map[string]*fakePR
	requests    int
	notModified int
	throttle    int
	fail        int
	badAuth     int
}

func newFakeHost(t *testing.T, kind string) *fakeHost {
	f := &fakeHost{kind: kind, prs: map[string]*fakePR{}, token: ghToken}
	if kind == prsync.KindForgejo {
		f.prefix, f.token = "/api/v1", fjToken
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeHost) set(repo string, n int, mut func(*fakePR)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := repo + "#" + strconv.Itoa(n)
	pr := f.prs[key]
	if pr == nil {
		pr = &fakePR{State: "open", Head: "a000000000", Commits: []string{"a000000000"}, Status: "pending"}
		f.prs[key] = pr
	}
	mut(pr)
}

func (f *fakeHost) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests, f.notModified
}

func (f *fakeHost) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	want := "Bearer " + f.token
	if f.kind == prsync.KindForgejo {
		want = "token " + f.token
	}
	if r.Header.Get("Authorization") != want {
		f.badAuth++
	}
	if f.throttle > 0 {
		f.throttle--
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	if f.fail > 0 {
		f.fail--
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	parts := strings.Split(strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, f.prefix), "/repos/"), "/")
	if len(parts) < 4 {
		http.NotFound(w, r)
		return
	}
	repo := parts[0] + "/" + parts[1]
	var body any
	switch parts[2] {
	case "pulls":
		pr := f.prs[repo+"#"+parts[3]]
		if pr == nil {
			http.NotFound(w, r)
			return
		}
		switch {
		case len(parts) == 4:
			n, _ := strconv.Atoi(parts[3])
			state := pr.State
			if pr.Merged {
				state = "closed"
			}
			body = map[string]any{"number": n, "state": state, "merged": pr.Merged, "draft": pr.Draft,
				"mergeable": pr.Mergeable, "title": "t", "head": map[string]any{"sha": pr.Head, "ref": pr.Ref}}
		case parts[4] == "reviews":
			body = pr.Reviews
		case parts[4] == "commits":
			cs := []map[string]any{}
			for _, c := range pr.Commits {
				commit := map[string]any{"sha": c}
				if login := pr.Authors[c]; login != "" {
					commit["author"] = map[string]any{"login": login}
				}
				cs = append(cs, commit)
			}
			body = paginate(r, cs)
		case parts[4] == "files":
			body = paginate(r, append([]map[string]any{}, pr.Files...))
		}
	case "commits":
		var pr *fakePR
		for k, p := range f.prs {
			if strings.HasPrefix(k, repo+"#") && p.Head == parts[3] {
				pr = p
			}
		}
		if pr == nil {
			http.NotFound(w, r)
			return
		}
		switch parts[4] {
		case "status":
			total := 1
			if pr.Status == "" {
				total = 0
			}
			body = map[string]any{"state": pr.Status, "total_count": total}
		case "check-runs":
			body = map[string]any{"total_count": len(pr.Runs), "check_runs": pr.Runs}
		}
	}
	raw, _ := json.Marshal(body)
	sum := sha256.Sum256(raw)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	if r.Header.Get("If-None-Match") == etag {
		f.notModified++
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func paginate(r *http.Request, items []map[string]any) []map[string]any {
	q := r.URL.Query()
	size, _ := strconv.Atoi(q.Get("per_page"))
	if size == 0 {
		size, _ = strconv.Atoi(q.Get("limit"))
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if size <= 0 || page <= 0 {
		return items
	}
	lo := min((page-1)*size, len(items))
	return items[lo:min(lo+size, len(items))]
}

type harness struct {
	t      *testing.T
	e      *servertest.Env
	clock  *ratelimit.FakeClock
	p      *prsync.Poller
	logs   *bytes.Buffer
	gh, fj *fakeHost
	lead   string
	wk     string
	rev    string
	bid    string
}

func hostConfig(gh, fj *fakeHost, ghRate, global string) []byte {
	return fmt.Appendf(nil, `{
  "interval": "30s",
  "rate": %s,
  "hosts": {
    "gh": {"kind": "github", "api_url": %q, "token_env": "PRSYNC_TEST_GH_TOKEN", "rate": %s, "authors": ["bot-ci"]},
    "fj": {"kind": "forgejo", "url": %q, "token": %q}
  },
  "repos": {"o/gh": "gh"}
}`, global, gh.URL, ghRate, fj.URL, fjToken)
}

func newHarness(t *testing.T, gh, fj *fakeHost, dbPath string, ghRate string) *harness {
	t.Helper()
	t.Setenv("PRSYNC_TEST_GH_TOKEN", ghToken)
	e := servertest.Start(t, server.Config{PublicURL: "http://sub.test", DBPath: dbPath,
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})
	cfg, err := prsync.ParseConfig(hostConfig(gh, fj, ghRate, `{"per_minute": 6000, "burst": 1000}`), "")
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, e: e, gh: gh, fj: fj, logs: &bytes.Buffer{},
		clock: ratelimit.NewFakeClock(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))}
	st, ok := e.Srv.Store.(prsync.Store)
	if !ok {
		t.Fatal("store does not implement prsync.Store")
	}
	h.p, err = prsync.New(prsync.Options{
		Config: cfg, Board: e.Srv.Board, Store: st, Hub: e.Srv.Hub, Clock: h.clock,
		Rand: rand.New(rand.NewPCG(1, 2)), Log: slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) setup(n int) []string {
	h.t.Helper()
	e := h.e
	h.lead = e.Register("rex", "leader", "other")
	h.wk = e.Register("w1", "worker", "claude-code")
	h.rev = e.Register("r1", "reviewer", "codex")
	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B", "repos": []string{"o/gh", "o/fj"}, "max_parallel_agents": 50})
	h.bid = br["id"].(string)
	var tasks []any
	for i := range n {
		tasks = append(tasks, map[string]any{"title": fmt.Sprintf("Task %d", i), "spec": "s", "repo": "o/gh"})
	}
	plan := e.Must(h.lead, "propose_plan", map[string]any{"brief_id": h.bid, "tasks": tasks})["plan"].(map[string]any)
	if code, out := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		h.t.Fatalf("approve plan: %d %v", code, out)
	}
	var ids []string
	for _, x := range plan["tasks"].([]any) {
		ids = append(ids, x.(map[string]any)["id"].(string))
	}
	return ids
}

func (h *harness) submit(id, prURL string) {
	h.t.Helper()
	h.e.Must(h.lead, "assign", map[string]any{"task_id": id, "agent_id": "w1"})
	h.e.Must(h.wk, "claim_task", map[string]any{"task_id": id})
	h.e.Must(h.wk, "submit", map[string]any{"task_id": id, "pr_url": prURL, "summary": "done"})
}

func (h *harness) sync() {
	h.t.Helper()
	if err := h.p.SyncOnce(context.Background()); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) status(id string) map[string]any {
	h.t.Helper()
	code, out := h.e.REST("GET", "/api/v1/tasks/"+id+"/pr", nil)
	if code != 200 {
		h.t.Fatalf("GET pr for %s: %d %v", id, code, out)
	}
	return out
}

func (h *harness) syncMessages(id string) []string {
	h.t.Helper()
	_, out := h.e.REST("GET", "/api/v1/threads/task/"+id+"/messages", nil)
	var bodies []string
	for _, x := range out["messages"].([]any) {
		m := x.(map[string]any)
		if m["author_id"] == "prsync" {
			bodies = append(bodies, m["body"].(string))
		}
	}
	return bodies
}

func ghURL(n int) string { return "https://github.com/o/gh/pull/" + strconv.Itoa(n) }

func ptr[T any](v T) *T { return &v }

func TestPRStatusLifecycleOverHTTP(t *testing.T) {
	gh, fj := newFakeHost(t, prsync.KindGitHub), newFakeHost(t, prsync.KindForgejo)
	h := newHarness(t, gh, fj, "", `{"per_minute": 6000, "burst": 1000}`)
	ids := h.setup(2)
	gh.set("o/gh", 1, func(p *fakePR) {})
	fj.set("o/fj", 2, func(p *fakePR) { p.Mergeable = ptr(true) })
	if code, out := h.e.REST("PATCH", "/api/v1/tasks/"+ids[1], map[string]any{"repo": "o/fj"}); code != 200 {
		t.Fatalf("patch task repo: %d %v", code, out)
	}
	h.submit(ids[0], ghURL(1))
	h.submit(ids[1], fj.URL+"/o/fj/pulls/2")

	sub := h.e.Srv.Hub.Subscribe([]string{"task:" + ids[0]})
	defer sub.Close()
	prEventCount := func() int {
		n := 0
		for {
			select {
			case ev := <-sub.C():
				if ev.Type == prsync.EventPR {
					n++
				}
			default:
				return n
			}
		}
	}

	h.sync()
	s := h.status(ids[0])
	if s["state"] != "open" || s["checks"] != "pending" || s["host"] != "gh" || s["repo"] != "o/gh" || s["number"] != 1.0 || s["head_sha"] != "a000000000" || s["mergeable"] != nil {
		t.Fatalf("github status: %v", s)
	}
	if s2 := h.status(ids[1]); s2["host"] != "fj" || s2["mergeable"] != true || s2["checks"] != "pending" {
		t.Fatalf("forgejo status: %v", s2)
	}
	if n := prEventCount(); n != 1 {
		t.Fatalf("task.pr events after first sync: %d", n)
	}
	_, list := h.e.REST("GET", "/api/v1/prs?brief_id="+h.bid, nil)
	if got := len(list["prs"].([]any)); got != 2 {
		t.Fatalf("GET /prs: %v", list)
	}

	gh.set("o/gh", 1, func(p *fakePR) {
		p.Status = "success"
		p.Runs = []map[string]any{{"status": "completed", "conclusion": "failure"}}
	})
	h.sync()
	if s := h.status(ids[0]); s["checks"] != "failure" {
		t.Fatalf("checks: %v", s)
	}
	if msgs := h.syncMessages(ids[0]); len(msgs) != 1 || !strings.Contains(msgs[0], "Checks failed") {
		t.Fatalf("messages after failing checks: %v", msgs)
	}

	gh.set("o/gh", 1, func(p *fakePR) {
		p.Head, p.Commits = "b111111111", []string{"b111111111"}
		p.Runs = []map[string]any{{"status": "completed", "conclusion": "success"}}
		p.Mergeable = ptr(true)
		p.Reviews = []map[string]any{
			{"user": map[string]any{"login": "alice"}, "state": "CHANGES_REQUESTED"},
			{"user": map[string]any{"login": "alice"}, "state": "APPROVED"},
			{"user": map[string]any{"login": "bob"}, "state": "COMMENTED"},
		}
	})
	h.sync()
	msgs := h.syncMessages(ids[0])
	if len(msgs) != 2 || !strings.Contains(msgs[1], "force-pushed") || !strings.Contains(msgs[1], "a000000") || !strings.Contains(msgs[1], "b111111") {
		t.Fatalf("messages after force push: %v", msgs)
	}
	s = h.status(ids[0])
	if s["checks"] != "success" || s["review_decision"] != "approved" || s["merge_ready"] != false {
		t.Fatalf("status before reviewer approval: %v", s)
	}

	h.e.Must(h.rev, "review", map[string]any{"task_id": ids[0], "verdict": "approve", "comments": "ok"})
	h.sync()
	if s := h.status(ids[0]); s["merge_ready"] != true {
		t.Fatalf("merge_ready: %v", s)
	}
	_, dec := h.e.REST("GET", "/api/v1/decisions", nil)
	found := false
	for _, x := range dec["decisions"].([]any) {
		d := x.(map[string]any)
		if d["id"] == "merge-ready:"+ids[0] {
			found = d["kind"] == "merge-ready" && d["links"].(map[string]any)["pr"] == ghURL(1)
		}
	}
	if !found {
		t.Fatalf("merge-ready decision missing: %v", dec)
	}

	if code, out := h.e.REST("PATCH", "/api/v1/briefs/"+h.bid, map[string]any{"auto_done_on_merge": true}); code != 200 {
		t.Fatalf("patch brief: %d %v", code, out)
	}
	gh.set("o/gh", 1, func(p *fakePR) { p.Merged = true })
	fj.set("o/fj", 2, func(p *fakePR) { p.State = "closed" })
	h.sync()
	if _, task := h.e.REST("GET", "/api/v1/tasks/"+ids[0], nil); task["status"] != "done" {
		t.Fatalf("task not done after merge: %v", task)
	}
	if s := h.status(ids[0]); s["state"] != "merged" {
		t.Fatalf("merged status: %v", s)
	}
	if msgs := h.syncMessages(ids[1]); len(msgs) != 1 || !strings.Contains(msgs[0], "closed without merging") {
		t.Fatalf("closed message: %v", msgs)
	}
	if s := h.status(ids[1]); s["state"] != "closed" {
		t.Fatalf("closed status: %v", s)
	}
	_, dec = h.e.REST("GET", "/api/v1/decisions", nil)
	for _, x := range dec["decisions"].([]any) {
		if x.(map[string]any)["kind"] == "merge-ready" {
			t.Fatalf("merge-ready decision survives merge: %v", x)
		}
	}
	gh.mu.Lock()
	fj.mu.Lock()
	badAuth := gh.badAuth + fj.badAuth
	fj.mu.Unlock()
	gh.mu.Unlock()
	if badAuth != 0 {
		t.Fatalf("%d requests without the configured token", badAuth)
	}
	if strings.Contains(h.logs.String(), ghToken) || strings.Contains(h.logs.String(), fjToken) {
		t.Fatal("token logged")
	}
	if n := prEventCount(); n != 4 {
		t.Fatalf("task.pr events: %d", n)
	}
}

func TestETagConditionalRequests(t *testing.T) {
	gh, fj := newFakeHost(t, prsync.KindGitHub), newFakeHost(t, prsync.KindForgejo)
	h := newHarness(t, gh, fj, "", `{"per_minute": 6000, "burst": 1000}`)
	ids := h.setup(1)
	gh.set("o/gh", 1, func(p *fakePR) {})
	h.submit(ids[0], ghURL(1))

	h.sync()
	first, nm := gh.counts()
	if first != 6 || nm != 0 {
		t.Fatalf("first sync: %d requests, %d not modified", first, nm)
	}
	before := h.status(ids[0])

	h.clock.Advance(30 * time.Second)
	h.sync()
	total, nm := gh.counts()
	if total-first != 4 || nm != 4 {
		t.Fatalf("second sync: %d requests, %d not modified", total-first, nm)
	}
	after := h.status(ids[0])
	if after["changed_at"] != before["changed_at"] || after["checked_at"] == before["checked_at"] {
		t.Fatalf("unchanged PR: before %v after %v", before, after)
	}

	gh.set("o/gh", 1, func(p *fakePR) { p.Draft = true })
	h.clock.Advance(30 * time.Second)
	h.sync()
	if _, nm2 := gh.counts(); nm2 != 7 {
		t.Fatalf("not modified after PR change: %d", nm2)
	}
	if s := h.status(ids[0]); s["draft"] != true || s["changed_at"] == before["changed_at"] {
		t.Fatalf("changed PR: %v", s)
	}
}

func TestHostRateLimitSharesTokensFairly(t *testing.T) {
	gh, fj := newFakeHost(t, prsync.KindGitHub), newFakeHost(t, prsync.KindForgejo)
	h := newHarness(t, gh, fj, "", `{"per_minute": 6, "burst": 6}`)
	ids := h.setup(3)
	for i, id := range ids {
		gh.set("o/gh", i+1, func(p *fakePR) {})
		h.submit(id, ghURL(i+1))
	}
	check := func(wantRequests, wantStatuses int) {
		t.Helper()
		_, out := h.e.REST("GET", "/api/v1/prs", nil)
		if n, _ := gh.counts(); n != wantRequests || len(out["prs"].([]any)) != wantStatuses {
			t.Fatalf("%d requests, %d statuses; want %d, %d", n, len(out["prs"].([]any)), wantRequests, wantStatuses)
		}
	}
	h.sync()
	check(6, 1)
	h.sync()
	check(6, 1)
	h.clock.Advance(time.Minute)
	h.sync()
	check(12, 2)
	h.clock.Advance(time.Minute)
	h.sync()
	check(18, 3)
}

func TestThrottleAndJitteredBackoff(t *testing.T) {
	gh, fj := newFakeHost(t, prsync.KindGitHub), newFakeHost(t, prsync.KindForgejo)
	h := newHarness(t, gh, fj, "", `{"per_minute": 6000, "burst": 1000}`)
	id := h.setup(1)[0]
	gh.set("o/gh", 1, func(p *fakePR) {})
	h.submit(id, ghURL(1))
	step := func(advance time.Duration, mut func(), want int) {
		t.Helper()
		h.clock.Advance(advance)
		gh.mu.Lock()
		mut()
		gh.mu.Unlock()
		h.sync()
		if n, _ := gh.counts(); n != want {
			t.Fatalf("after %s: %d requests, want %d", advance, n, want)
		}
	}
	none := func() {}

	step(0, none, 6)
	step(30*time.Second, func() { gh.fail = 1 }, 7)
	if e, _ := h.status(id)["error"].(string); !strings.Contains(e, "502") {
		t.Fatalf("error not recorded: %q", e)
	}
	step(14*time.Second, none, 7)
	step(16*time.Second, none, 11)
	if s := h.status(id); s["error"] != nil || s["state"] != "open" {
		t.Fatalf("not recovered: %v", s)
	}

	step(30*time.Second, func() { gh.throttle = 1 }, 12)
	step(60*time.Second, none, 12)
	step(61*time.Second, none, 16)
	if s := h.status(id); s["error"] != nil {
		t.Fatalf("throttling recorded as a task error: %v", s)
	}
	if strings.Contains(h.logs.String(), ghToken) || !strings.Contains(h.logs.String(), "rate limited") {
		t.Fatalf("logs: %s", h.logs.String())
	}
}

func TestRestartKeepsStatusAndDoesNotRepeatTransitions(t *testing.T) {
	gh, fj := newFakeHost(t, prsync.KindGitHub), newFakeHost(t, prsync.KindForgejo)
	db := filepath.Join(t.TempDir(), "t.db")
	gh.set("o/gh", 1, func(p *fakePR) { p.Status = "failure" })
	var id string
	var before map[string]any
	t.Run("first", func(t *testing.T) {
		h := newHarness(t, gh, fj, db, `{"per_minute": 6000, "burst": 1000}`)
		id = h.setup(1)[0]
		h.submit(id, ghURL(1))
		h.sync()
		before = h.status(id)
		if msgs := h.syncMessages(id); len(msgs) != 1 {
			t.Fatalf("messages: %v", msgs)
		}
	})
	h := newHarness(t, gh, fj, db, `{"per_minute": 6000, "burst": 1000}`)
	if s := h.status(id); s["head_sha"] != before["head_sha"] || s["checks"] != "failure" {
		t.Fatalf("status after restart: %v", s)
	}
	h.sync()
	if msgs := h.syncMessages(id); len(msgs) != 1 {
		t.Fatalf("transition repeated after restart: %v", msgs)
	}
	if code, out := h.e.REST("GET", "/api/v1/tasks/T-999/pr", nil); code != 404 {
		t.Fatalf("unknown task: %d %v", code, out)
	}
}

func TestMountPollsWithEnvConfig(t *testing.T) {
	gh, fj := newFakeHost(t, prsync.KindGitHub), newFakeHost(t, prsync.KindForgejo)
	path := filepath.Join(t.TempDir(), "prsync.json")
	if err := os.WriteFile(path, hostConfig(gh, fj, "null", "null"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PRSYNC_TEST_GH_TOKEN", ghToken)
	t.Setenv("SUBSPACE_PRSYNC_CONFIG", path)
	t.Setenv("SUBSPACE_PRSYNC_INTERVAL", "20ms")
	e := servertest.New(t)
	h := &harness{t: t, e: e}
	ids := h.setup(1)
	gh.set("o/gh", 1, func(p *fakePR) { p.Status = "success" })
	h.submit(ids[0], ghURL(1))
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, out := e.REST("GET", "/api/v1/tasks/"+ids[0]+"/pr", nil)
		if code == 200 && out["checks"] == "success" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("poller never stored a status: %d %v", code, out)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
