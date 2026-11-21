package mcp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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

	for body, want := range map[string]int{
		`B-99|o/r`:    404,
		`B-1|other/x`: 400,
	} {
		parts := strings.Split(body, "|")
		if code, _ := e.REST("POST", "/api/v1/hillclimb/jobs", map[string]any{
			"brief_id": parts[0], "repo": parts[1], "metric_cmd": "sh eval.sh", "direction": "min"}); code != want {
			t.Fatalf("create job %s: %d, want %d", body, code, want)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", e.TS.URL+"/api/v1/events?topics=brief:"+br["id"].(string), nil)
	req.Header.Set("Authorization", "Bearer "+servertest.OwnerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var (
		mu   sync.Mutex
		seen []string
	)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		var typ string
		for sc.Scan() {
			line := sc.Text()
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				typ = v
			}
			if v, ok := strings.CutPrefix(line, "data: "); ok && strings.HasPrefix(typ, "hillclimb.") {
				var ev struct {
					Data map[string]any `json:"data"`
				}
				_ = json.Unmarshal([]byte(v), &ev)
				mu.Lock()
				seen = append(seen, fmt.Sprintf("%s %v %v %v", typ, ev.Data["attempts"], ev.Data["value"], ev.Data["best"]))
				mu.Unlock()
			}
		}
	}()

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
	_, other := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "other", "repos": []string{"o/r"}})
	otherID := other["id"].(string)
	if code, j := e.REST("POST", "/api/v1/hillclimb/jobs", map[string]any{
		"brief_id": otherID, "repo": "o/r", "metric_cmd": "sh eval.sh", "direction": "max"}); code != 201 {
		t.Fatalf("create other job: %d %v", code, j)
	}
	for brief, want := range map[string]int{br["id"].(string): 1, otherID: 1, "B-99": 0, "": 2} {
		code, l := e.REST("GET", "/api/v1/hillclimb/jobs?brief_id="+brief, nil)
		if code != 200 || len(l["jobs"].([]any)) != want {
			t.Fatalf("jobs for %q: %d %v", brief, code, l)
		}
	}
	want := []string{
		"hillclimb.updated 0 <nil> <nil>",
		"hillclimb.attempt <nil> 0.8 0.8", "hillclimb.updated 1 <nil> 0.8",
		"hillclimb.attempt <nil> 0.3 0.3", "hillclimb.updated 2 <nil> 0.3",
	}
	var got []string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		mu.Lock()
		got = slices.Clone(seen)
		mu.Unlock()
		if len(got) >= len(want) {
			break
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("hillclimb events = %q, want %q", got, want)
	}
	_ = e.Srv.Close()
	e2 := servertest.Start(t, cfg)
	code, s := e2.REST("GET", "/api/v1/hillclimb/jobs/"+jid+"/series", nil)
	if code != 200 || len(s["points"].([]any)) != 2 || s["job"].(map[string]any)["best"] != 0.3 {
		t.Fatalf("series after restart: %d %v", code, s)
	}
}
