package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeGit struct {
	mu     sync.Mutex
	fail   map[string]bool
	head   string
	pushes []string
}

func (f *fakeGit) Run(_ context.Context, _, name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name != "git" {
		if f.fail[f.head] {
			return "FAIL: TestLogin\n", errors.New("exit status 1")
		}
		return "ok\n", nil
	}
	switch args[0] {
	case "checkout":
		f.head = strings.TrimPrefix(args[len(args)-1], "refs/remotes/origin/")
	case "rev-parse":
		if strings.HasPrefix(args[len(args)-1], "HEAD") {
			return "1111111111111111111111111111111111111111\n", nil
		}
		return "2222222222222222222222222222222222222222\n", nil
	case "push":
		f.pushes = append(f.pushes, f.head)
	}
	return "", nil
}

func (f *fakeGit) pushed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.pushes...)
}

func setupMergeQueue(t *testing.T) *fakeGit {
	t.Helper()
	dir := t.TempDir()
	cfg, _ := json.Marshal(map[string]any{
		"workdir":  filepath.Join(dir, "work"),
		"interval": "10ms",
		"repos":    map[string]any{"o/r": map[string]any{"remote": "https://git.example/o/r.git", "verify": []string{"make", "test"}}},
	})
	path := filepath.Join(dir, "mq.json")
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUBSPACE_MQ_CONFIG", path)
	fg := &fakeGit{fail: map[string]bool{}}
	mqRunner = fg
	t.Cleanup(func() { mqRunner = nil })
	return fg
}

func TestMergeQueueMergesApprovedTasksAndEjectsFailures(t *testing.T) {
	fg := setupMergeQueue(t)
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	wk := e.register("claude-1", "worker", "claude-code")
	rev := e.register("codex-r", "reviewer", "codex")

	_, br := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "MQ", "repos": []string{"o/r"}, "max_parallel_agents": 5})
	plan := e.must(lead, "propose_plan", map[string]any{"brief_id": br["id"], "tasks": []any{
		map[string]any{"title": "Good", "spec": "s", "repo": "o/r"},
		map[string]any{"title": "Broken", "spec": "s", "repo": "o/r"},
		map[string]any{"title": "Unreviewed", "spec": "s", "repo": "o/r"},
	}})["plan"].(map[string]any)
	if code, out := e.rest("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	var ids, branches []string
	for i, x := range plan["tasks"].([]any) {
		id := x.(map[string]any)["id"].(string)
		ids = append(ids, id)
		branches = append(branches, x.(map[string]any)["branch"].(string))
		e.must(lead, "assign", map[string]any{"task_id": id, "agent_id": "claude-1"})
		e.must(wk, "claim_task", map[string]any{"task_id": id})
		e.must(wk, "submit", map[string]any{"task_id": id, "pr_url": "https://git.example/o/r/pulls/" + strconv.Itoa(i+1)})
	}
	good, broken, unreviewed := ids[0], ids[1], ids[2]
	fg.mu.Lock()
	fg.fail[branches[1]] = true
	fg.mu.Unlock()

	if code, out := e.rest("POST", "/api/v1/mergequeue/candidates", map[string]any{"task_id": good}); code != 409 {
		t.Fatalf("unapproved task enqueued: %d %v", code, out)
	}
	for _, id := range []string{good, broken} {
		e.must(rev, "review", map[string]any{"task_id": id, "verdict": "approve", "comments": "lgtm"})
	}

	code, c := e.rest("POST", "/api/v1/mergequeue/candidates", map[string]any{"task_id": good})
	if code != 201 || c["pr"] != float64(1) || c["head"] != branches[0] || c["base"] != "main" || c["repo"] != "o/r" {
		t.Fatalf("enqueue: %d %v", code, c)
	}
	if code, out := e.rest("POST", "/api/v1/mergequeue/candidates", map[string]any{"task_id": broken}); code != 201 {
		t.Fatalf("enqueue broken: %d %v", code, out)
	}
	if code, out := e.rest("POST", "/api/v1/mergequeue/candidates", map[string]any{"task_id": unreviewed}); code != 409 {
		t.Fatalf("unreviewed task enqueued: %d %v", code, out)
	}

	states := map[string]string{}
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, out := e.rest("GET", "/api/v1/mergequeue/candidates?repo=o/r", nil)
		for _, x := range out["candidates"].([]any) {
			m := x.(map[string]any)
			states[m["task_id"].(string)] = m["state"].(string)
		}
		_, tk := e.rest("GET", "/api/v1/tasks/"+good, nil)
		if states[good] == "merged" && states[broken] == "ejected" && tk["status"] == "done" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue did not settle: %v, %s is %v", states, good, tk["status"])
		}
		time.Sleep(10 * time.Millisecond)
	}

	if p := fg.pushed(); len(p) != 1 || p[0] != branches[0] {
		t.Fatalf("pushes %v", p)
	}
	if _, tk := e.rest("GET", "/api/v1/tasks/"+broken, nil); tk["status"] != "approved" {
		t.Fatalf("ejected task must not be done: %v", tk["status"])
	}
	if code, out := e.rest("POST", "/api/v1/mergequeue/candidates", map[string]any{"task_id": good}); code != 409 {
		t.Fatalf("done task re-enqueued: %d %v", code, out)
	}

	thread := func(kind, id string) string {
		_, out := e.rest("GET", "/api/v1/threads/"+kind+"/"+id+"/messages", nil)
		var b strings.Builder
		for _, x := range out["messages"].([]any) {
			b.WriteString(x.(map[string]any)["body"].(string) + "\n")
		}
		return b.String()
	}
	if s := thread("task", good); !strings.Contains(s, "merged (fast-forward)") || !strings.Contains(s, "approved → done") {
		t.Fatalf("good thread:\n%s", s)
	}
	if s := thread("task", broken); !strings.Contains(s, "ejected") || !strings.Contains(s, "FAIL: TestLogin") {
		t.Fatalf("broken thread:\n%s", s)
	}
	if s := thread("agent", "claude-1"); !strings.Contains(s, broken+" was not merged") {
		t.Fatalf("worker thread:\n%s", s)
	}
}

func TestMergeQueueIsGatedByOwnerApprovalFromDecisionInbox(t *testing.T) {
	fg := setupMergeQueue(t)
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	wk := e.register("claude-1", "worker", "claude-code")
	rev := e.register("codex-r", "reviewer", "codex")

	_, br := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "MQ", "repos": []string{"o/r", "o/manual"}, "max_parallel_agents": 5})
	plan := e.must(lead, "propose_plan", map[string]any{"brief_id": br["id"], "tasks": []any{
		map[string]any{"title": "Good", "spec": "s", "repo": "o/r"},
		map[string]any{"title": "Broken", "spec": "s", "repo": "o/r"},
		map[string]any{"title": "Manual", "spec": "s", "repo": "o/manual"},
	}})["plan"].(map[string]any)
	if code, out := e.rest("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	var ids, branches []string
	for i, x := range plan["tasks"].([]any) {
		id := x.(map[string]any)["id"].(string)
		ids = append(ids, id)
		branches = append(branches, x.(map[string]any)["branch"].(string))
		e.must(lead, "assign", map[string]any{"task_id": id, "agent_id": "claude-1"})
		e.must(wk, "claim_task", map[string]any{"task_id": id})
		e.must(wk, "submit", map[string]any{"task_id": id, "pr_url": "https://git.example/o/r/pulls/" + strconv.Itoa(i+1)})
		e.must(rev, "review", map[string]any{"task_id": id, "verdict": "approve", "comments": "lgtm"})
	}
	good, broken, manual := ids[0], ids[1], ids[2]
	fg.mu.Lock()
	fg.fail[branches[1]] = true
	fg.mu.Unlock()

	inbox := func() map[string]map[string]any {
		code, out := e.rest("GET", "/api/v1/decisions", nil)
		if code != 200 {
			t.Fatalf("decisions: %d %v", code, out)
		}
		ds := map[string]map[string]any{}
		for _, x := range out["decisions"].([]any) {
			d := x.(map[string]any)
			ds[d["id"].(string)] = d
		}
		return ds
	}
	actions := func(d map[string]any) []string {
		var out []string
		for _, a := range d["actions"].([]any) {
			out = append(out, a.(string))
		}
		return out
	}

	time.Sleep(50 * time.Millisecond)
	if p := fg.pushed(); len(p) != 0 {
		t.Fatalf("merged without owner approval: %v", p)
	}
	ds := inbox()
	for _, id := range []string{good, broken} {
		if got := strings.Join(actions(ds["merge:"+id]), ","); got != "enqueue,merge,skip" {
			t.Fatalf("%s actions: %q", id, got)
		}
	}
	if got := strings.Join(actions(ds["merge:"+manual]), ","); got != "merge,skip" {
		t.Fatalf("task outside the merge queue offers %q", got)
	}

	for _, id := range []string{good, broken} {
		code, out := e.rest("POST", "/api/v1/decisions/merge:"+id, map[string]any{"action": "enqueue"})
		if code != 200 {
			t.Fatalf("enqueue %s: %d %v", id, code, out)
		}
		if c := out["result"].(map[string]any); c["task_id"] != id || c["state"] != "queued" {
			t.Fatalf("enqueue %s result: %v", id, c)
		}
	}
	if code, out := e.rest("POST", "/api/v1/decisions/merge:"+manual, map[string]any{"action": "enqueue"}); code != 409 {
		t.Fatalf("enqueue outside the merge queue: %d %v", code, out)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		_, tk := e.rest("GET", "/api/v1/tasks/"+good, nil)
		ds = inbox()
		d, ejected := ds["merge:"+broken]
		if tk["status"] == "done" && ejected {
			if _, ok := ds["merge:"+good]; ok {
				t.Fatalf("merged task still in inbox")
			}
			if got := strings.Join(actions(d), ","); got != "enqueue,merge,skip" || !strings.Contains(d["summary"].(string), "ejected from the merge queue") {
				t.Fatalf("ejected decision: %v", d)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue did not settle: %s is %v, inbox %v", good, tk["status"], ds)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p := fg.pushed(); len(p) != 1 || p[0] != branches[0] {
		t.Fatalf("pushes %v", p)
	}

	_, out := e.rest("GET", "/api/v1/audit?tool=enqueue_merge", nil)
	var audited []string
	for _, x := range out["events"].([]any) {
		ev := x.(map[string]any)
		if ev["agent_id"] != "owner" {
			t.Fatalf("enqueue audited as %v", ev["agent_id"])
		}
		audited = append(audited, ev["task_id"].(string))
	}
	if strings.Join(audited, ",") != good+","+broken && strings.Join(audited, ",") != broken+","+good {
		t.Fatalf("audited enqueues %v", audited)
	}
}
