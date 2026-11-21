package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/events"
)

type gatedGit struct {
	*fakeGit
	open chan struct{}
}

func (g gatedGit) Run(ctx context.Context, dir, name string, args ...string) (string, error) {
	select {
	case <-g.open:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return g.fakeGit.Run(ctx, dir, name, args...)
}

func approvedTask(e *env) string {
	e.t.Helper()
	lead := e.register("rex", "leader", "other")
	wk := e.register("claude-1", "worker", "claude-code")
	rev := e.register("codex-r", "reviewer", "codex")
	_, br := e.rest("POST", "/api/v1/briefs", map[string]any{"title": "MQ", "repos": []string{"o/r"}, "max_parallel_agents": 5})
	plan := e.must(lead, "propose_plan", map[string]any{"brief_id": br["id"], "tasks": []any{map[string]any{"title": "Good", "spec": "s", "repo": "o/r"}}})["plan"].(map[string]any)
	if code, out := e.rest("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		e.t.Fatalf("approve: %d %v", code, out)
	}
	id := plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
	e.must(lead, "assign", map[string]any{"task_id": id, "agent_id": "claude-1"})
	e.must(wk, "claim_task", map[string]any{"task_id": id})
	e.must(wk, "submit", map[string]any{"task_id": id, "pr_url": "https://git.example/o/r/pulls/1"})
	e.must(rev, "review", map[string]any{"task_id": id, "verdict": "approve", "comments": "lgtm"})
	return id
}

func (e *env) decision(id string) map[string]any {
	e.t.Helper()
	_, out := e.rest("GET", "/api/v1/decisions", nil)
	for _, x := range out["decisions"].([]any) {
		if d := x.(map[string]any); d["id"] == id {
			return d
		}
	}
	return nil
}

func (e *env) taskStatus(id string) string {
	e.t.Helper()
	_, tk := e.rest("GET", "/api/v1/tasks/"+id, nil)
	s, _ := tk["status"].(string)
	return s
}

func TestInboxMergeEnqueuesAndDeployFailureRaisesAnAlert(t *testing.T) {
	dir := t.TempDir()
	cfg, _ := json.Marshal(map[string]any{
		"workdir": filepath.Join(dir, "work"), "interval": "10ms",
		"repos": map[string]any{"o/r": map[string]any{"remote": "https://git.example/o/r.git", "verify": []string{"make", "test"},
			"deploy": []string{"make", "deploy"}, "deploy_timeout": "1m"}},
	})
	path := filepath.Join(dir, "mq.json")
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUBSPACE_MQ_CONFIG", path)
	fg := &fakeGit{fail: map[string]bool{"1111111111111111111111111111111111111111": true}}
	gate := gatedGit{fakeGit: fg, open: make(chan struct{})}
	mqRunner = gate
	t.Cleanup(func() { mqRunner = nil })

	e := newEnv(t)
	sub := e.srv.Hub.Subscribe([]string{"board"})
	defer sub.Close()
	id := approvedTask(e)

	d := e.decision("merge:" + id)
	if d == nil || !slices.Equal(toStrings(d["actions"]), []string{"merge", "skip"}) {
		t.Fatalf("merge decision: %v", d)
	}
	code, out := e.rest("POST", "/api/v1/decisions/merge:"+id, map[string]any{"action": "merge", "note": "ship it"})
	c, _ := out["result"].(map[string]any)
	if code != 200 || c["state"] != "queued" || c["task_id"] != id || c["pr"] != float64(1) {
		t.Fatalf("enqueue via inbox: %d %v", code, out)
	}
	if s := e.taskStatus(id); s != "approved" {
		t.Fatalf("task is %s before the merge", s)
	}
	if e.decision("merge:"+id) != nil {
		t.Fatal("a queued task is still offered for merge")
	}
	close(gate.open)

	thread := func() string {
		_, msgs := e.rest("GET", "/api/v1/threads/task/"+id+"/messages", nil)
		var b strings.Builder
		for _, x := range msgs["messages"].([]any) {
			b.WriteString(x.(map[string]any)["body"].(string) + "\n")
		}
		return b.String()
	}
	deadline := time.Now().Add(10 * time.Second)
	var cand map[string]any
	for {
		_, list := e.rest("GET", "/api/v1/mergequeue/candidates?repo=o/r", nil)
		if cs := list["candidates"].([]any); len(cs) == 1 {
			cand = cs[0].(map[string]any)
		}
		if cand != nil && cand["deploy_state"] == "failed" && e.taskStatus(id) == "done" && strings.Contains(thread(), "failed; the merge stays") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue did not settle: %v task %s thread:\n%s", cand, e.taskStatus(id), thread())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if cand["state"] != "merged" || !strings.Contains(cand["deploy_log"].(string), "make deploy") {
		t.Fatalf("candidate %v", cand)
	}
	if s := thread(); !strings.Contains(s, "merged (fast-forward)") || !strings.Contains(s, "FAIL: TestLogin") {
		t.Fatalf("thread:\n%s", s)
	}
	alert := e.decision("deploy:" + cand["id"].(string))
	if alert == nil || alert["kind"] != "alert" || alert["task_id"] != id || !strings.Contains(alert["title"].(string), "Deploy of o/r") {
		t.Fatalf("alert decision: %v", alert)
	}
	if !sawEvent(sub, "train.deploy_failed", cand["id"].(string)) {
		t.Fatal("no train.deploy_failed event")
	}
}

func TestInboxMergeWithoutQueueMarksDone(t *testing.T) {
	t.Setenv("SUBSPACE_MQ_CONFIG", "")
	e := newEnv(t)
	id := approvedTask(e)

	d := e.decision("merge:" + id)
	if d == nil || !slices.Equal(toStrings(d["actions"]), []string{"mark_done", "skip"}) {
		t.Fatalf("merge decision: %v", d)
	}
	if code, out := e.rest("POST", "/api/v1/decisions/merge:"+id, map[string]any{"action": "merge", "note": "merged by hand"}); code != 200 {
		t.Fatalf("mark done via inbox: %d %v", code, out)
	}
	if s := e.taskStatus(id); s != "done" {
		t.Fatalf("task is %s", s)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func sawEvent(sub events.Subscription, typ, id string) bool {
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-sub.C():
			if dv, ok := ev.Data.(deployEvent); ev.Type == typ && ok && dv.ID == id && dv.Status == "failed" {
				return true
			}
		case <-timeout:
			return false
		}
	}
}
