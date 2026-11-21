package mirror

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type fakeIssue struct {
	Number  int64
	Title   string
	Body    string
	State   string
	Labels  []int64
	Created bool
}

type fakeForgejo struct {
	mu        sync.Mutex
	t         *testing.T
	issues    []*fakeIssue
	labels    map[string]int64
	nextLabel int64
	srv       *httptest.Server
	badAuth   int
}

func newFakeForgejo(t *testing.T) *fakeForgejo {
	f := &fakeForgejo{t: t, labels: map[string]int64{}, nextLabel: 100}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeForgejo) labelName(id int64) string {
	for n, i := range f.labels {
		if i == id {
			return n
		}
	}
	return ""
}

func (f *fakeForgejo) issueJSON(i *fakeIssue) map[string]any {
	ls := []map[string]any{}
	for _, id := range i.Labels {
		ls = append(ls, map[string]any{"id": id, "name": f.labelName(id)})
	}
	return map[string]any{
		"number": i.Number, "title": i.Title, "body": i.Body, "state": i.State, "labels": ls,
		"html_url": f.srv.URL + "/o/r/issues/" + strconv.FormatInt(i.Number, 10),
	}
}

func (f *fakeForgejo) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "token secret" {
		f.badAuth++
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	base := "/api/v1/repos/o/r"
	path := strings.TrimPrefix(r.URL.Path, base)
	var body map[string]any
	if r.Body != nil && r.Method != http.MethodGet {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	write := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	ids := func(v any) []int64 {
		var out []int64
		for _, x := range v.([]any) {
			out = append(out, int64(x.(float64)))
		}
		return out
	}
	switch {
	case path == "/labels" && r.Method == http.MethodGet:
		out := []map[string]any{}
		for n, id := range f.labels {
			out = append(out, map[string]any{"id": id, "name": n})
		}
		write(out)
	case path == "/labels" && r.Method == http.MethodPost:
		f.nextLabel++
		name := body["name"].(string)
		f.labels[name] = f.nextLabel
		w.WriteHeader(http.StatusCreated)
		write(map[string]any{"id": f.nextLabel, "name": name})
	case path == "/issues" && r.Method == http.MethodGet:
		q := r.URL.Query().Get("q")
		if r.URL.Query().Get("state") != "all" || r.URL.Query().Get("type") != "issues" {
			f.t.Errorf("bad search query %q", r.URL.RawQuery)
		}
		out := []map[string]any{}
		if r.URL.Query().Get("page") == "1" {
			for _, i := range f.issues {
				if strings.Contains(i.Body, q) {
					out = append(out, f.issueJSON(i))
				}
			}
		}
		write(out)
	case path == "/issues" && r.Method == http.MethodPost:
		i := &fakeIssue{Number: int64(len(f.issues) + 1), Title: body["title"].(string), Body: body["body"].(string),
			State: "open", Labels: ids(body["labels"])}
		f.issues = append(f.issues, i)
		w.WriteHeader(http.StatusCreated)
		write(f.issueJSON(i))
	case strings.HasPrefix(path, "/issues/"):
		rest := strings.Split(strings.TrimPrefix(path, "/issues/"), "/")
		n, _ := strconv.Atoi(rest[0])
		if n < 1 || n > len(f.issues) {
			http.NotFound(w, r)
			return
		}
		i := f.issues[n-1]
		switch {
		case len(rest) == 2 && rest[1] == "labels" && r.Method == http.MethodPut:
			i.Labels = ids(body["labels"])
			write([]any{})
		case len(rest) == 1 && r.Method == http.MethodGet:
			write(f.issueJSON(i))
		case len(rest) == 1 && r.Method == http.MethodPatch:
			if v, ok := body["title"].(string); ok {
				i.Title = v
			}
			if v, ok := body["body"].(string); ok {
				i.Body = v
			}
			if v, ok := body["state"].(string); ok {
				i.State = v
			}
			write(f.issueJSON(i))
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeForgejo) mirror(t *testing.T) *ForgejoMirror {
	m, err := NewForgejo(ForgejoConfig{BaseURL: f.srv.URL + "/", Token: "secret", Owner: "o", Repo: "r"})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (f *fakeForgejo) labelNames(i *fakeIssue) []string {
	var out []string
	for _, id := range i.Labels {
		out = append(out, f.labelName(id))
	}
	return out
}

func sample() TaskView {
	return TaskView{ID: "T-3", Title: "Add login", Status: "in_progress", Assignee: "claude-1",
		BriefTitle: "Auth | v2", Acceptance: []string{"login works", "tests pass"}}
}

func TestUpsertCreatesThenUpdatesIdempotently(t *testing.T) {
	f := newFakeForgejo(t)
	m := f.mirror(t)
	ctx := context.Background()

	u1, err := m.Upsert(ctx, sample())
	if err != nil {
		t.Fatal(err)
	}
	if u1 != f.srv.URL+"/o/r/issues/1" {
		t.Fatalf("url = %q", u1)
	}
	tv := sample()
	tv.Status = "submitted"
	tv.PRURL = "https://git.example/o/r/pulls/7"
	u2, err := m.Upsert(ctx, tv)
	if err != nil {
		t.Fatal(err)
	}
	if u2 != u1 || len(f.issues) != 1 {
		t.Fatalf("expected single issue, url %q vs %q, n=%d", u2, u1, len(f.issues))
	}
	i := f.issues[0]
	if i.State != "open" || i.Title != "Add login" {
		t.Fatalf("issue = %+v", i)
	}
	names := strings.Join(f.labelNames(i), ",")
	if !strings.Contains(names, "subspace") || !strings.Contains(names, "status/submitted") || strings.Contains(names, "in_progress") {
		t.Fatalf("labels = %s", names)
	}
	for _, want := range []string{Marker("T-3"), "Managed by Subspace, read-only", "pulls/7", "- [ ] login works", `Auth \| v2`, "`claude-1`"} {
		if !strings.Contains(i.Body, want) {
			t.Errorf("body missing %q:\n%s", want, i.Body)
		}
	}
}

func TestUpsertDoesNotConfuseSimilarIDs(t *testing.T) {
	f := newFakeForgejo(t)
	m := f.mirror(t)
	ctx := context.Background()
	a := TaskView{ID: "T-1", Title: "one", Status: "ready"}
	b := TaskView{ID: "T-10", Title: "ten", Status: "ready"}
	if _, err := m.Upsert(ctx, b); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Upsert(ctx, a); err != nil {
		t.Fatal(err)
	}
	if len(f.issues) != 2 {
		t.Fatalf("issues = %d, want 2", len(f.issues))
	}
}

func TestUpsertKeepsForeignLabelsAndReusesLabels(t *testing.T) {
	f := newFakeForgejo(t)
	m := f.mirror(t)
	ctx := context.Background()
	if _, err := m.Upsert(ctx, sample()); err != nil {
		t.Fatal(err)
	}
	f.labels["priority"] = 999
	f.issues[0].Labels = append(f.issues[0].Labels, 999)
	nLabels := len(f.labels)

	tv := sample()
	tv.Status = "in_review"
	if _, err := m.Upsert(ctx, tv); err != nil {
		t.Fatal(err)
	}
	names := f.labelNames(f.issues[0])
	if !strings.Contains(strings.Join(names, ","), "priority") {
		t.Fatalf("foreign label dropped: %v", names)
	}
	if len(f.labels) != nLabels+1 { // only status/in_review is new
		t.Fatalf("labels = %v", f.labels)
	}
}

func TestUpsertTerminalStatusClosesIssue(t *testing.T) {
	f := newFakeForgejo(t)
	m := f.mirror(t)
	ctx := context.Background()
	tv := sample()
	tv.Status = "done"
	if _, err := m.Upsert(ctx, tv); err != nil {
		t.Fatal(err)
	}
	if f.issues[0].State != "closed" {
		t.Fatalf("state = %s", f.issues[0].State)
	}
	tv.Status = "ready"
	if _, err := m.Upsert(ctx, tv); err != nil {
		t.Fatal(err)
	}
	if f.issues[0].State != "open" {
		t.Fatalf("reopen failed: %s", f.issues[0].State)
	}
}

func TestClose(t *testing.T) {
	f := newFakeForgejo(t)
	m := f.mirror(t)
	ctx := context.Background()
	if err := m.Close(ctx, "T-404"); err != nil {
		t.Fatalf("closing unknown task: %v", err)
	}
	if _, err := m.Upsert(ctx, sample()); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(ctx, "T-3"); err != nil {
		t.Fatal(err)
	}
	if f.issues[0].State != "closed" {
		t.Fatalf("state = %s", f.issues[0].State)
	}
	if err := m.Close(ctx, "T-3"); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(ctx, ""); err == nil {
		t.Fatal("want error for empty id")
	}
}

func TestConcurrentUpsertsCreateOneIssue(t *testing.T) {
	f := newFakeForgejo(t)
	m := f.mirror(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Upsert(context.Background(), sample()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(f.issues) != 1 {
		t.Fatalf("issues = %d", len(f.issues))
	}
}

func TestErrorsDoNotLeakToken(t *testing.T) {
	f := newFakeForgejo(t)
	m, err := NewForgejo(ForgejoConfig{BaseURL: f.srv.URL, Token: "wrong-token", Owner: "o", Repo: "r"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Upsert(context.Background(), sample())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "wrong-token") {
		t.Fatal("token leaked in error")
	}
}

func TestServerErrorAndBadInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	m, err := NewForgejo(ForgejoConfig{BaseURL: srv.URL, Token: "t", Owner: "o", Repo: "r"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Upsert(context.Background(), sample()); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v", err)
	}
	if _, err := m.Upsert(context.Background(), TaskView{}); err == nil {
		t.Fatal("want error for empty task")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Close(ctx, "T-1"); err == nil {
		t.Fatal("want ctx error")
	}
}

func TestNewForgejoValidation(t *testing.T) {
	for _, c := range []ForgejoConfig{
		{BaseURL: "", Token: "t", Owner: "o", Repo: "r"},
		{BaseURL: "ftp://x", Token: "t", Owner: "o", Repo: "r"},
		{BaseURL: "http://x", Token: "", Owner: "o", Repo: "r"},
		{BaseURL: "http://x", Token: "t", Owner: "", Repo: "r"},
	} {
		if _, err := NewForgejo(c); err == nil {
			t.Errorf("want error for %+v", c)
		}
	}
}

func TestNoopMirror(t *testing.T) {
	var m IssueMirror = NoopMirror{}
	u, err := m.Upsert(context.Background(), sample())
	if u != "" || err != nil {
		t.Fatalf("%q %v", u, err)
	}
	if err := m.Close(context.Background(), "T-3"); err != nil {
		t.Fatal(err)
	}
}

func TestRenderBodyMinimal(t *testing.T) {
	b := RenderBody(TaskView{ID: "T-1", Title: "x", Status: "ready"})
	if !strings.HasPrefix(b, "<!-- subspace:task:T-1 -->\n") {
		t.Fatalf("marker not first: %q", b)
	}
	if strings.Contains(b, "Acceptance") || strings.Contains(b, "Assignee") {
		t.Fatalf("unexpected sections: %s", b)
	}
}
