package server_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/servertest"
)

type gitFixture struct {
	t      *testing.T
	remote string
	seed   string
}

func newGitFixture(t *testing.T) *gitFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(k, "test")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "test@example.invalid")
	}
	root := t.TempDir()
	g := &gitFixture{t: t, remote: filepath.Join(root, "remote.git"), seed: filepath.Join(root, "seed")}
	g.git(root, "init", "-q", "--bare", "-b", "main", g.remote)
	g.git(root, "clone", "-q", g.remote, g.seed)
	g.git(g.seed, "symbolic-ref", "HEAD", "refs/heads/main")
	g.commit("README", "hello\n")
	g.git(g.seed, "push", "-q", "origin", "main")
	return g
}

func (g *gitFixture) git(dir string, args ...string) string {
	g.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		g.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (g *gitFixture) commit(file, content string) {
	g.t.Helper()
	if err := os.WriteFile(filepath.Join(g.seed, file), []byte(content), 0o600); err != nil {
		g.t.Fatal(err)
	}
	g.git(g.seed, "add", file)
	g.git(g.seed, "commit", "-q", "-m", "change "+file)
}

func (g *gitFixture) branch(name, file string) {
	g.t.Helper()
	g.git(g.seed, "checkout", "-q", "-b", name, "main")
	g.commit(file, name+"\n")
	g.git(g.seed, "push", "-q", "origin", name)
	g.git(g.seed, "checkout", "-q", "main")
}

func (g *gitFixture) onMain(file string) bool {
	return exec.Command("git", "-C", g.remote, "cat-file", "-e", "refs/heads/main:"+file).Run() == nil
}

func TestMergeTrainOverHTTP(t *testing.T) {
	g := newGitFixture(t)
	dir := t.TempDir()
	cfg, _ := json.Marshal(map[string]any{
		"workdir":  filepath.Join(dir, "work"),
		"interval": "20ms",
		"repos": map[string]any{"o/r": map[string]any{"remote": g.remote, "train": 8,
			"verify": []string{"sh", "-c", "test ! -e culprit.txt || { echo culprit.txt breaks the build; exit 1; }"}}},
	})
	path := filepath.Join(dir, "mq.json")
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUBSPACE_MQ_CONFIG", path)
	e := servertest.New(t)
	lead := e.Register("rex", "leader", "other")
	wk := e.Register("claude-1", "worker", "claude-code")
	rev := e.Register("codex-r", "reviewer", "codex")

	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "MQ", "repos": []string{"o/r"}, "max_parallel_agents": 5})
	titles := []string{"Alpha", "Beta", "Culprit", "Delta"}
	var items []any
	for _, title := range titles {
		items = append(items, map[string]any{"title": title, "spec": "s", "repo": "o/r"})
	}
	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": br["id"], "tasks": items})["plan"].(map[string]any)
	if code, out := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	ids := map[string]string{}
	for i, x := range plan["tasks"].([]any) {
		tk := x.(map[string]any)
		id, branch := tk["id"].(string), tk["branch"].(string)
		ids[titles[i]] = id
		file := strings.ToLower(titles[i]) + ".txt"
		if titles[i] == "Culprit" {
			file = "culprit.txt"
		}
		g.branch(branch, file)
		e.Must(lead, "assign", map[string]any{"task_id": id, "agent_id": "claude-1"})
		e.Must(wk, "claim_task", map[string]any{"task_id": id})
		e.Must(wk, "submit", map[string]any{"task_id": id, "pr_url": "https://git.example/o/r/pulls/" + strconv.Itoa(i+1)})
		e.Must(rev, "review", map[string]any{"task_id": id, "verdict": "approve", "comments": "lgtm"})
	}
	for _, title := range titles {
		if code, out := e.REST("POST", "/api/v1/mergequeue/candidates", map[string]any{"task_id": ids[title]}); code != 201 {
			t.Fatalf("enqueue %s: %d %v", title, code, out)
		}
	}

	culprit := ids["Culprit"]
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, out := e.REST("GET", "/api/v1/mergequeue/candidates?repo=o/r", nil)
		states := map[string]string{}
		for _, x := range out["candidates"].([]any) {
			m := x.(map[string]any)
			states[m["task_id"].(string)] = m["state"].(string)
		}
		settled := states[culprit] == "ejected"
		for _, title := range []string{"Alpha", "Beta", "Delta"} {
			settled = settled && states[ids[title]] == "merged"
		}
		if settled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue did not settle: %v", states)
		}
		time.Sleep(20 * time.Millisecond)
	}

	for _, title := range []string{"Alpha", "Beta", "Delta"} {
		if !g.onMain(strings.ToLower(title) + ".txt") {
			t.Fatalf("%s not on main", title)
		}
		if _, tk := e.REST("GET", "/api/v1/tasks/"+ids[title], nil); tk["status"] != "done" {
			t.Fatalf("%s is %v", title, tk["status"])
		}
	}
	if g.onMain("culprit.txt") {
		t.Fatal("culprit reached main")
	}
	if _, tk := e.REST("GET", "/api/v1/tasks/"+culprit, nil); tk["status"] != "approved" {
		t.Fatalf("culprit is %v", tk["status"])
	}

	code, out := e.REST("GET", "/api/v1/mergequeue/trains?repo=o/r", nil)
	trains, _ := out["trains"].([]any)
	if code != 200 || len(trains) == 0 {
		t.Fatalf("trains: %d %v", code, out)
	}
	results := map[string]string{}
	for _, x := range trains {
		tr := x.(map[string]any)
		if tr["state"] == "building" || tr["state"] == "testing" || tr["state"] == "merging" {
			t.Fatalf("train %v still active", tr["id"])
		}
		for _, m := range tr["members"].([]any) {
			mm := m.(map[string]any)
			if r := mm["result"].(string); r != "requeued" {
				results[mm["task_id"].(string)] = r
			}
		}
	}
	if results[culprit] != "ejected" || results[ids["Alpha"]] != "merged" || results[ids["Delta"]] != "merged" {
		t.Fatalf("member results %v", results)
	}
	first := trains[len(trains)-1].(map[string]any)
	if code, got := e.REST("GET", "/api/v1/mergequeue/trains/"+first["id"].(string), nil); code != 200 || got["id"] != first["id"] {
		t.Fatalf("get train: %d %v", code, got)
	}
	if code, _ := e.REST("GET", "/api/v1/mergequeue/trains/MQT-404", nil); code != 404 {
		t.Fatalf("unknown train: %d", code)
	}
	if code, _ := e.REST("GET", "/api/v1/mergequeue/trains?limit=0", nil); code != 400 {
		t.Fatalf("limit=0: %d", code)
	}

	thread := func(kind, id string) string {
		_, out := e.REST("GET", "/api/v1/threads/"+kind+"/"+id+"/messages", nil)
		var b strings.Builder
		for _, x := range out["messages"].([]any) {
			b.WriteString(x.(map[string]any)["body"].(string) + "\n")
		}
		return b.String()
	}
	if s := thread("task", culprit); !strings.Contains(s, "ejected") || !strings.Contains(s, "culprit.txt breaks the build") {
		t.Fatalf("culprit thread:\n%s", s)
	}
	if s := thread("task", ids["Alpha"]); !strings.Contains(s, "by merge train MQT-") || !strings.Contains(s, "approved → done") {
		t.Fatalf("alpha thread:\n%s", s)
	}
	if s := thread("agent", "claude-1"); !strings.Contains(s, culprit+" was not merged") {
		t.Fatalf("worker thread:\n%s", s)
	}
}
