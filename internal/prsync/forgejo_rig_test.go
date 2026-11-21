package prsync_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/prsync"
	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

type forgeRig struct {
	t    *testing.T
	url  string
	repo string
	e    *servertest.Env
	p    *prsync.Poller
	lead string
	wk   string
	rev  string
	bid  string
	next int
}

type logSink struct{ t *testing.T }

func (l *logSink) Write(p []byte) (int, error) {
	l.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

func newForgeRig(t *testing.T, forgeURL, token, repo string) *forgeRig {
	t.Helper()
	e := servertest.Start(t, server.Config{PublicURL: "http://sub.test",
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})
	raw, err := json.Marshal(map[string]any{
		"interval": "30s",
		"rate":     map[string]any{"per_minute": 60000, "burst": 10000},
		"hosts": map[string]any{"forge": map[string]any{
			"kind": "forgejo", "url": forgeURL, "token": token,
			"rate": map[string]any{"per_minute": 60000, "burst": 10000},
		}},
		"repos": map[string]string{repo: "forge"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := prsync.ParseConfig(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	st, ok := e.Srv.Store.(prsync.Store)
	if !ok {
		t.Fatal("store does not implement prsync.Store")
	}
	p, err := prsync.New(prsync.Options{Config: cfg, Board: e.Srv.Board, Store: st, Hub: e.Srv.Hub,
		Log: slog.New(slog.NewTextHandler(&logSink{t}, nil))})
	if err != nil {
		t.Fatal(err)
	}
	r := &forgeRig{t: t, url: forgeURL, repo: repo, e: e, p: p}
	r.lead = e.Register("rex", "leader", "other")
	r.wk = e.Register("w1", "worker", "claude-code")
	r.rev = e.Register("r1", "reviewer", "codex")
	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B", "repos": []string{repo}, "max_parallel_agents": 50})
	r.bid = br["id"].(string)
	return r
}

func (r *forgeRig) prURL(n int64) string { return fmt.Sprintf("%s/%s/pulls/%d", r.url, r.repo, n) }

func (r *forgeRig) submit(n int64) string {
	r.t.Helper()
	r.next++
	plan := r.e.Must(r.lead, "propose_plan", map[string]any{"brief_id": r.bid, "tasks": []any{
		map[string]any{"title": fmt.Sprintf("Task %d", r.next), "spec": "s", "repo": r.repo},
	}})["plan"].(map[string]any)
	if code, out := r.e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		r.t.Fatalf("approve plan: %d %v", code, out)
	}
	id := plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
	r.e.Must(r.lead, "assign", map[string]any{"task_id": id, "agent_id": "w1"})
	r.e.Must(r.wk, "claim_task", map[string]any{"task_id": id})
	r.e.Must(r.wk, "submit", map[string]any{"task_id": id, "pr_url": r.prURL(n), "summary": "done"})
	return id
}

func (r *forgeRig) sync() {
	r.t.Helper()
	if err := r.p.SyncOnce(context.Background()); err != nil {
		r.t.Fatal(err)
	}
}

func (r *forgeRig) statusCode(id string) (int, map[string]any) {
	r.t.Helper()
	return r.e.REST("GET", "/api/v1/tasks/"+id+"/pr", nil)
}

func (r *forgeRig) status(id string) map[string]any {
	r.t.Helper()
	code, out := r.statusCode(id)
	if code != 200 {
		r.t.Fatalf("GET pr for %s: %d %v", id, code, out)
	}
	return out
}

func (r *forgeRig) stored(id string) prsync.Status {
	r.t.Helper()
	raw, ok, err := r.e.Srv.Store.(prsync.Store).PRStatus(context.Background(), id)
	if err != nil || !ok {
		r.t.Fatalf("stored status: %v %v", ok, err)
	}
	var s prsync.Status
	if err := json.Unmarshal(raw, &s); err != nil {
		r.t.Fatal(err)
	}
	return s
}

func (r *forgeRig) syncMessages(id string) []string {
	r.t.Helper()
	_, out := r.e.REST("GET", "/api/v1/threads/task/"+id+"/messages", nil)
	var bodies []string
	for _, x := range out["messages"].([]any) {
		m := x.(map[string]any)
		if m["author_id"] == "prsync" {
			bodies = append(bodies, m["body"].(string))
		}
	}
	return bodies
}

func (r *forgeRig) syncUntil(id string, done func(map[string]any) bool) map[string]any {
	r.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		r.sync()
		if code, s := r.statusCode(id); code == 200 && done(s) {
			return s
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("status never converged: %v", r.status(id))
		}
		time.Sleep(200 * time.Millisecond)
	}
}
