package mirror_test

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/forgejotest"
	"github.com/SigmaUno/subspace/internal/forgejotest/serverrig"
	"github.com/SigmaUno/subspace/internal/mirror"
)

type issue struct {
	Number int64  `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func listIssues(t *testing.T, inst *forgejotest.Instance, repo *forgejotest.Repo) []issue {
	t.Helper()
	var all []issue
	for page := 1; ; page++ {
		var batch []issue
		q := url.Values{"state": {"all"}, "type": {"issues"}, "limit": {"50"}, "page": {strconv.Itoa(page)}}
		inst.Do(t, "", http.MethodGet, repo.Path("/issues?"+q.Encode()), nil, &batch)
		all = append(all, batch...)
		if len(batch) == 0 {
			return all
		}
	}
}

func labelNames(i issue) string {
	var names []string
	for _, l := range i.Labels {
		names = append(names, l.Name)
	}
	slices.Sort(names)
	return strings.Join(names, ",")
}

func TestLiveIssueMirror(t *testing.T) {
	inst := forgejotest.Start(t)
	repo := inst.NewRepo(t)
	m, err := mirror.NewForgejo(mirror.ForgejoConfig{BaseURL: inst.URL, Token: inst.Token, Owner: repo.Owner, Repo: repo.Name})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	view := func(id, status string) mirror.TaskView {
		return mirror.TaskView{ID: id, Title: "Title " + id, Status: status, Assignee: "w1", BriefTitle: "Brief | pipe", Acceptance: []string{"works"}}
	}

	u1, err := m.Upsert(ctx, view("T-1", "ready"))
	if err != nil {
		t.Fatal(err)
	}
	u2, err := m.Upsert(ctx, view("T-1", "in_progress"))
	if err != nil {
		t.Fatal(err)
	}
	if u1 != u2 || !strings.HasPrefix(u1, inst.URL+"/"+repo.FullName()+"/issues/") {
		t.Fatalf("issue urls %q, %q", u1, u2)
	}
	issues := listIssues(t, inst, repo)
	if len(issues) != 1 {
		t.Fatalf("%d issues after two upserts, want 1", len(issues))
	}
	got := issues[0]
	if got.State != "open" || got.Title != "Title T-1" || !strings.HasPrefix(got.Body, mirror.Marker("T-1")+"\n") ||
		!strings.Contains(got.Body, "`in_progress`") || labelNames(got) != "status/in_progress,subspace" {
		t.Fatalf("issue = %+v", got)
	}

	if _, err := m.Upsert(ctx, view("T-2", "backlog")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Upsert(ctx, view("T-1", "done")); err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]issue{}
	for _, i := range listIssues(t, inst, repo) {
		byTitle[i.Title] = i
	}
	if len(byTitle) != 2 || byTitle["Title T-1"].State != "closed" || byTitle["Title T-2"].State != "open" ||
		labelNames(byTitle["Title T-1"]) != "status/done,subspace" {
		t.Fatalf("issues = %+v", byTitle)
	}

	if err := m.Close(ctx, "T-2"); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(ctx, "T-2"); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(ctx, "T-404"); err != nil {
		t.Fatal(err)
	}
	for _, i := range listIssues(t, inst, repo) {
		if i.State != "closed" {
			t.Fatalf("issue %q is %s", i.Title, i.State)
		}
	}
}

func TestLiveIssueMirrorFindsTasksBeyondOnePage(t *testing.T) {
	inst := forgejotest.Start(t)
	repo := inst.NewRepo(t)
	m, err := mirror.NewForgejo(mirror.ForgejoConfig{BaseURL: inst.URL, Token: inst.Token, Owner: repo.Owner, Repo: repo.Name})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const n = 60
	deadline := time.Now().Add(2 * time.Minute)
	for i := 1; i <= n; i++ {
		id := "T-" + strconv.Itoa(i)
		if _, err := m.Upsert(ctx, mirror.TaskView{ID: id, Title: "Task " + id, Status: "ready"}); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("too slow at %d", i)
		}
	}
	for i := 1; i <= n; i++ {
		id := "T-" + strconv.Itoa(i)
		if _, err := m.Upsert(ctx, mirror.TaskView{ID: id, Title: "Task " + id, Status: "approved"}); err != nil {
			t.Fatal(err)
		}
	}
	restarted, err := mirror.NewForgejo(mirror.ForgejoConfig{BaseURL: inst.URL, Token: inst.Token, Owner: repo.Owner, Repo: repo.Name})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		id := "T-" + strconv.Itoa(i)
		if _, err := restarted.Upsert(ctx, mirror.TaskView{ID: id, Title: "Task " + id, Status: "done"}); err != nil {
			t.Fatal(err)
		}
	}
	issues := listIssues(t, inst, repo)
	if len(issues) != n {
		t.Fatalf("%d issues, want %d (duplicates created)", len(issues), n)
	}
	for _, i := range issues {
		if labelNames(i) != "status/done,subspace" || i.State != "closed" {
			t.Fatalf("issue %q labels %q", i.Title, labelNames(i))
		}
	}
}

func TestLiveIssueMirrorReusesLabelsBeyondOnePage(t *testing.T) {
	inst := forgejotest.Start(t)
	repo := inst.NewRepo(t)
	for i := range 40 {
		inst.Do(t, "", http.MethodPost, repo.Path("/labels"), map[string]string{"name": "filler-" + strconv.Itoa(i), "color": "#cccccc"}, nil)
	}
	for _, name := range []string{"subspace", "status/ready"} {
		inst.Do(t, "", http.MethodPost, repo.Path("/labels"), map[string]string{"name": name, "color": "#cccccc"}, nil)
	}
	m, err := mirror.NewForgejo(mirror.ForgejoConfig{BaseURL: inst.URL, Token: inst.Token, Owner: repo.Owner, Repo: repo.Name})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Upsert(context.Background(), mirror.TaskView{ID: "T-1", Title: "One", Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	var labels []struct {
		Name string `json:"name"`
	}
	for page := 1; ; page++ {
		var batch []struct {
			Name string `json:"name"`
		}
		inst.Do(t, "", http.MethodGet, repo.Path("/labels?limit=50&page="+strconv.Itoa(page)), nil, &batch)
		if len(batch) == 0 {
			break
		}
		labels = append(labels, batch...)
	}
	count := map[string]int{}
	for _, l := range labels {
		count[l.Name]++
	}
	if len(labels) != 42 || count["subspace"] != 1 || count["status/ready"] != 1 {
		t.Fatalf("labels = %d, subspace %d, status/ready %d", len(labels), count["subspace"], count["status/ready"])
	}
}

func waitIssue(t *testing.T, inst *forgejotest.Instance, repo *forgejotest.Repo, title, labels, state string) issue {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		issues := listIssues(t, inst, repo)
		if len(issues) > 1 {
			t.Fatalf("%d issues for one task", len(issues))
		}
		if len(issues) == 1 && issues[0].Title == title && labelNames(issues[0]) == labels && issues[0].State == state {
			return issues[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("issues = %+v, want %q labelled %q and %s", issues, title, labels, state)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestLiveIssueMirrorThroughTheServer(t *testing.T) {
	inst := forgejotest.Start(t)
	repo := inst.NewRepo(t)
	repo.Commit(t, "sub/t-1-a", "main", "a.txt", "a\n")
	pr := repo.OpenPR(t, "sub/t-1-a", "main", "Add a")
	rig := serverrig.New(t, inst, repo, map[string]string{
		"SUBSPACE_MIRROR_FORGEJO_URL":      inst.URL,
		"SUBSPACE_MIRROR_FORGEJO_TOKEN":    inst.Token,
		"SUBSPACE_MIRROR_FORGEJO_REPO_MAP": repo.FullName() + "=" + repo.FullName(),
	})

	id := rig.Task("Mirror me", "it is mirrored")
	got := waitIssue(t, inst, repo, "Mirror me", "status/ready,subspace", "open")
	if !strings.HasPrefix(got.Body, mirror.Marker(id)+"\n") || !strings.Contains(got.Body, "- [ ] it is mirrored") {
		t.Fatalf("body:\n%s", got.Body)
	}
	rig.Submit(id, pr)
	waitIssue(t, inst, repo, "Mirror me", "status/submitted,subspace", "open")
	rig.Review(id, "approve", "ok")
	waitIssue(t, inst, repo, "Mirror me", "status/approved,subspace", "open")
	if code, out := rig.E.REST("POST", "/api/v1/tasks/"+id+"/done", map[string]any{"note": "merged"}); code != 200 {
		t.Fatalf("mark done: %d %v", code, out)
	}
	waitIssue(t, inst, repo, "Mirror me", "status/done,subspace", "closed")
}
