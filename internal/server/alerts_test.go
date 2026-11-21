package server_test

import (
	"bufio"
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

func alertList(t *testing.T, e *servertest.Env, query string) []map[string]any {
	t.Helper()
	code, out := e.REST("GET", "/api/v1/alerts"+query, nil)
	if code != 200 {
		t.Fatalf("alerts: %d %v", code, out)
	}
	var res []map[string]any
	for _, a := range out["alerts"].([]any) {
		res = append(res, a.(map[string]any))
	}
	return res
}

func waitAlert(t *testing.T, e *servertest.Env, query, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, a := range alertList(t, e, query) {
			if a["id"] == id {
				return a
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("alert %s not listed for %q: %v", id, query, alertList(t, e, query))
	return nil
}

func TestAlertsSwarmStalledLifecycle(t *testing.T) {
	t.Setenv("SUBSPACE_ALERTS_INTERVAL", "10ms")
	t.Setenv("SUBSPACE_ALERTS_STALL_AFTER", "10m")
	t.Setenv("SUBSPACE_ALERTS_DISK_MIN_FREE_BYTES", "0")
	clk := &fakeClock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	e := servertest.Start(t, server.Config{PublicURL: "http://sub.test", Now: clk.Now,
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", e.TS.URL+"/api/v1/events?topics=alerts", nil)
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
		for sc.Scan() {
			if typ, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
				mu.Lock()
				seen = append(seen, typ)
				mu.Unlock()
			}
		}
	}()
	events := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(seen)
	}

	lead := e.Register("rex", "leader", "other")
	e.Register("w1", "worker", "claude-code")
	code, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B", "repos": []string{"o/r"}})
	if code != 201 {
		t.Fatalf("brief: %d %v", code, br)
	}
	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": br["id"], "tasks": []any{
		map[string]any{"title": "a", "spec": "s", "acceptance_criteria": []string{"c"}, "repo": "o/r"},
	}})["plan"].(map[string]any)
	if code, out := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	tid := plan["tasks"].([]any)[0].(map[string]any)["id"].(string)

	time.Sleep(100 * time.Millisecond)
	if got := alertList(t, e, ""); len(got) != 0 {
		t.Fatalf("alerts before the threshold: %v", got)
	}

	clk.Advance(11 * time.Minute)
	raised := waitAlert(t, e, "?status=active", "swarm_stalled")
	if raised["raised_at"] != clk.Now().Format(time.RFC3339) || raised["severity"] != "warning" || raised["status"] != "active" {
		t.Fatalf("alert = %v", raised)
	}
	d, ok := decisionIDs(t, e)["alert:swarm_stalled"]
	if !ok || d["kind"] != "alert" {
		t.Fatalf("decision missing: %v", decisionIDs(t, e))
	}

	clk.Advance(5 * time.Minute)
	time.Sleep(100 * time.Millisecond)
	active := alertList(t, e, "?status=active")
	if len(active) != 1 || active[0]["raised_at"] != raised["raised_at"] {
		t.Fatalf("alert not deduplicated: %v", active)
	}

	e.Must(lead, "assign", map[string]any{"task_id": tid, "agent_id": "w1"})
	resolved := waitAlert(t, e, "?status=resolved", "swarm_stalled")
	if resolved["status"] != "resolved" || resolved["resolved_at"] == nil {
		t.Fatalf("resolved = %v", resolved)
	}
	if got := alertList(t, e, "?status=active"); len(got) != 0 {
		t.Fatalf("active after resolve: %v", got)
	}
	if _, ok := decisionIDs(t, e)["alert:swarm_stalled"]; ok {
		t.Fatal("resolved alert still in decisions")
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !slices.Equal(events(), []string{"hello", "alert.raised", "alert.resolved"}) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := events(); !slices.Equal(got, []string{"hello", "alert.raised", "alert.resolved"}) {
		t.Fatalf("events = %v", got)
	}

	if code, out := e.REST("GET", "/api/v1/alerts?status=bogus", nil); code != 400 {
		t.Fatalf("bad status: %d %v", code, out)
	}
}

func TestAlertsDataDirThresholds(t *testing.T) {
	t.Setenv("SUBSPACE_ALERTS_INTERVAL", "10ms")
	t.Setenv("SUBSPACE_ALERTS_DB_MAX_BYTES", "1")
	t.Setenv("SUBSPACE_ALERTS_DISK_MIN_FREE_BYTES", "18446744073709551615")
	e := servertest.New(t)
	disk := waitAlert(t, e, "?status=active", "disk_low")
	if disk["severity"] != "critical" {
		t.Fatalf("disk alert = %v", disk)
	}
	waitAlert(t, e, "?status=active", "db_size")
}
