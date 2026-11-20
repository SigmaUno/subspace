package mcp_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func pushEval(t *testing.T, work, bare, branch, loss string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(work, "eval.sh"), []byte("echo val loss "+loss+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, work, "add", "-A")
	gitT(t, work, "commit", "--quiet", "-m", loss)
	gitT(t, work, "push", "--quiet", "--force", bare, "HEAD:refs/heads/"+branch)
}

func TestMeasureToolOverMCP(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	bare, work := filepath.Join(dir, "origin.git"), filepath.Join(dir, "work")
	gitT(t, dir, "init", "--bare", "--quiet", bare)
	gitT(t, dir, "init", "--quiet", "-b", "main", work)
	repos, _ := json.Marshal(map[string]string{"o/r": bare})
	t.Setenv("SUBSPACE_HILLCLIMB_WORKDIR", filepath.Join(dir, "hc"))
	t.Setenv("SUBSPACE_HILLCLIMB_REPOS", string(repos))
	t.Setenv("SUBSPACE_HILLCLIMB_TIMEOUT", "30s")

	dbPath := filepath.Join(dir, "s.db")
	cfg := server.Config{DBPath: dbPath, PublicURL: "http://sub.test", FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute}
	e := servertest.Start(t, cfg)
	lead := e.Register("rex", "leader", "other")
	w1 := e.Register("w1", "worker", "claude-code")
	w2 := e.Register("w2", "worker", "claude-code")
	rev := e.Register("rev", "reviewer", "codex")

	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B", "repos": []string{"o/r"}})
	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": br["id"], "tasks": []any{
		map[string]any{"title": "lower loss", "spec": "s", "repo": "o/r"},
		map[string]any{"title": "other", "spec": "s", "repo": "o/r"},
	}})["plan"].(map[string]any)
	if code, _ := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatal("approve")
	}
	e.Must(lead, "assign", map[string]any{"task_id": "T-1", "agent_id": "w1"})
	e.Must(lead, "assign", map[string]any{"task_id": "T-2", "agent_id": "w2"})
	task := e.Must(w1, "claim_task", map[string]any{"task_id": "T-1"})["task"].(map[string]any)
	e.Must(w2, "claim_task", map[string]any{"task_id": "T-2"})
	branch := task["branch"].(string)
	pushEval(t, work, bare, branch, "0.8")
	pushEval(t, work, bare, branch+"-v2", "0.3")
	pushEval(t, work, bare, "main", "0.01")

	code, job := e.REST("POST", "/api/v1/hillclimb/jobs", map[string]any{
		"brief_id": br["id"], "repo": "o/r", "metric_cmd": "sh eval.sh", "direction": "min", "budget": 2})
	if code != 201 {
		t.Fatalf("create job: %d %v", code, job)
	}
	jid := job["id"].(string)

	for tok, want := range map[string]bool{w1: true, rev: false, lead: false} {
		got := false
		for _, x := range e.RPC(tok, "tools/list", nil)["result"].(map[string]any)["tools"].([]any) {
			got = got || x.(map[string]any)["name"] == "measure"
		}
		if got != want {
			t.Fatalf("measure listed = %v, want %v", got, want)
		}
	}
	e.MustFail(rev, "measure", map[string]any{"job_id": jid, "branch": branch}, "")
	e.MustFail(w2, "measure", map[string]any{"job_id": jid, "branch": branch}, "not_assigned")
	e.MustFail(w1, "measure", map[string]any{"job_id": jid, "branch": "main"}, "not_assigned")
	e.MustFail(w1, "measure", map[string]any{"job_id": "H-99", "branch": branch}, "not_found")

	out := e.Must(w1, "measure", map[string]any{"job_id": jid, "branch": branch})
	if out["metric"] != 0.8 || out["loss"] != 0.8 || out["remaining_attempts"] != 1.0 || out["job_status"] != "running" {
		t.Fatalf("first measure: %v", out)
	}
	out = e.Must(w1, "measure", map[string]any{"job_id": jid, "branch": branch + "-v2"})
	if out["metric"] != 0.3 || out["best"] != 0.3 || out["remaining_attempts"] != 0.0 || out["stop_reason"] != "budget_exhausted" {
		t.Fatalf("second measure: %v", out)
	}
	e.MustFail(w1, "measure", map[string]any{"job_id": jid, "branch": branch}, "conflict")

	if code, s := e.REST("GET", "/api/v1/hillclimb/jobs/"+jid+"/series", nil); code != 200 || len(s["points"].([]any)) != 2 {
		t.Fatalf("series: %d %v", code, s)
	}
	_ = e.Srv.Close()
	e2 := servertest.Start(t, cfg)
	code, s := e2.REST("GET", "/api/v1/hillclimb/jobs/"+jid+"/series", nil)
	if code != 200 || len(s["points"].([]any)) != 2 || s["job"].(map[string]any)["best"] != 0.3 {
		t.Fatalf("series after restart: %d %v", code, s)
	}
}
