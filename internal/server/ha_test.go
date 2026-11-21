package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/audit"
	"github.com/SigmaUno/subspace/internal/loadtest"
	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

const haFailover = 5 * time.Second

type haCluster struct {
	t     *testing.T
	nodes []*servertest.Env
	down  map[int]bool
}

func startCluster(t *testing.T, n int, lease time.Duration) *haCluster {
	t.Helper()
	if !strings.HasPrefix(os.Getenv("SUBSPACE_TEST_PG_DSN"), "postgres") {
		t.Skip("SUBSPACE_TEST_PG_DSN not set")
	}
	dsn := servertest.DB(t)
	c := &haCluster{t: t, down: map[int]bool{}}
	for i := range n {
		c.nodes = append(c.nodes, servertest.Start(t, server.Config{
			DBPath: dsn, PublicURL: "http://sub.test", InstanceID: "node-" + strconv.Itoa(i),
			FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: lease,
			LeaderInterval: 100 * time.Millisecond, LeaderTTL: 500 * time.Millisecond,
		}))
	}
	return c
}

func (c *haCluster) node(i int) *servertest.Env {
	alive := c.alive()
	return c.nodes[alive[i%len(alive)]]
}

func (c *haCluster) alive() []int {
	var out []int
	for i := range c.nodes {
		if !c.down[i] {
			out = append(out, i)
		}
	}
	return out
}

func (c *haCluster) leader() int {
	c.t.Helper()
	deadline := time.Now().Add(haFailover)
	for {
		var leaders []int
		agreed := true
		for _, i := range c.alive() {
			_, st := c.nodes[i].REST("GET", "/api/v1/ha", nil)
			if st["leader"] == true {
				leaders = append(leaders, i)
			}
			if st["leader_instance"] == nil {
				agreed = false
			}
		}
		if len(leaders) > 1 {
			c.t.Fatalf("split brain: %v", leaders)
		}
		if len(leaders) == 1 && agreed {
			return leaders[0]
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("no leader elected within %s", haFailover)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (c *haCluster) kill(i int) {
	c.nodes[i].Stop()
	c.down[i] = true
}

func (c *haCluster) follower() *servertest.Env {
	l := c.leader()
	for _, i := range c.alive() {
		if i != l {
			return c.nodes[i]
		}
	}
	return c.nodes[l]
}

type notifySink struct {
	mu  sync.Mutex
	got []notify.Notification
}

func (s *notifySink) ServeHTTP(_ http.ResponseWriter, r *http.Request) {
	var n notify.Notification
	_ = json.NewDecoder(r.Body).Decode(&n)
	s.mu.Lock()
	s.got = append(s.got, n)
	s.mu.Unlock()
}

func (s *notifySink) count(kind string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, x := range s.got {
		if x.Kind == kind {
			n++
		}
	}
	return n
}

func (s *notifySink) duplicates() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	var dup []string
	for _, x := range s.got {
		k := x.ID + "|" + x.Kind + "|" + x.Title + "|" + x.Text
		if seen[k] {
			dup = append(dup, k)
		}
		seen[k] = true
	}
	return dup
}

type haGit struct {
	mu     sync.Mutex
	head   string
	pushes []string
}

func (g *haGit) Run(_ context.Context, _, name string, args ...string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if name != "git" {
		return "ok\n", nil
	}
	switch args[0] {
	case "checkout":
		g.head = strings.TrimPrefix(args[len(args)-1], "refs/remotes/origin/")
	case "rev-parse":
		return "1111111111111111111111111111111111111111\n", nil
	case "push":
		g.pushes = append(g.pushes, g.head)
	}
	return "", nil
}

func (g *haGit) pushed() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.pushes...)
}

func approvedTasks(t *testing.T, c *haCluster, n int) []string {
	t.Helper()
	lead := c.node(0).Register("rex", "leader", "other")
	wk := c.node(1).Register("claude-1", "worker", "claude-code")
	rev := c.node(2).Register("codex-r", "reviewer", "codex")
	_, br := c.node(0).REST("POST", "/api/v1/briefs", map[string]any{"title": "HA", "repos": []string{"o/r"}, "max_parallel_agents": 5})
	var specs []any
	for i := range n {
		specs = append(specs, map[string]any{"title": fmt.Sprintf("Task %d", i), "spec": "s", "repo": "o/r"})
	}
	plan := c.node(1).Must(lead, "propose_plan", map[string]any{"brief_id": br["id"], "tasks": specs})["plan"].(map[string]any)
	if code, out := c.node(2).REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	var ids []string
	for i, x := range plan["tasks"].([]any) {
		id := x.(map[string]any)["id"].(string)
		ids = append(ids, id)
		c.node(i).Must(lead, "assign", map[string]any{"task_id": id, "agent_id": "claude-1"})
		c.node(i+1).Must(wk, "claim_task", map[string]any{"task_id": id})
		c.node(i+2).Must(wk, "submit", map[string]any{"task_id": id, "pr_url": "https://git.example/o/r/pulls/" + strconv.Itoa(i+1)})
		c.node(i).Must(rev, "review", map[string]any{"task_id": id, "verdict": "approve", "comments": "lgtm"})
	}
	return ids
}

func mergeQueueConfig(t *testing.T) *haGit {
	t.Helper()
	dir := t.TempDir()
	cfg, _ := json.Marshal(map[string]any{
		"workdir": filepath.Join(dir, "work"), "interval": "20ms",
		"repos": map[string]any{"o/r": map[string]any{"remote": "https://git.example/o/r.git", "verify": []string{"make", "test"}}},
	})
	path := filepath.Join(dir, "mq.json")
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUBSPACE_MQ_CONFIG", path)
	g := &haGit{}
	t.Cleanup(server.SetMergeQueueRunner(g))
	return g
}

func mergeOnce(t *testing.T, c *haCluster, g *haGit, taskID string, pushes int) {
	t.Helper()
	if code, out := c.follower().REST("POST", "/api/v1/mergequeue/candidates", map[string]any{"task_id": taskID}); code != 201 {
		t.Fatalf("enqueue %s: %d %v", taskID, code, out)
	}
	waitFor(t, taskID+" merged", func() bool {
		_, tk := c.node(0).REST("GET", "/api/v1/tasks/"+taskID, nil)
		return tk["status"] == "done"
	})
	time.Sleep(300 * time.Millisecond)
	if p := g.pushed(); len(p) != pushes {
		t.Fatalf("pushes after merging %s: %v, want %d", taskID, p, pushes)
	}
}

func askOnce(t *testing.T, c *haCluster, sink *notifySink, tok string, want int) {
	t.Helper()
	c.follower().Must(tok, "ask_owner", map[string]any{"question": fmt.Sprintf("question %d?", want), "options": []string{"a", "b"}})
	waitFor(t, "question notification", func() bool { return sink.count("question") >= want })
	time.Sleep(1500 * time.Millisecond)
	if n := sink.count("question"); n != want {
		t.Fatalf("question notifications %d, want %d", n, want)
	}
}

func TestHAFailoverKeepsSideEffectsSingle(t *testing.T) {
	sink := &notifySink{}
	hook := httptest.NewServer(sink)
	defer hook.Close()
	t.Setenv(notify.EnvWebhookURL, hook.URL)
	t.Setenv(notify.EnvAllowPrivate, "true")
	g := mergeQueueConfig(t)

	c := startCluster(t, 3, time.Minute)
	first := c.leader()
	ids := approvedTasks(t, c, 2)
	asker := c.node(0).Register("asker", "worker", "claude-code")

	askOnce(t, c, sink, asker, 1)
	mergeOnce(t, c, g, ids[0], 1)

	start := time.Now()
	c.kill(first)
	next := c.leader()
	if next == first {
		t.Fatal("killed node still leader")
	}
	if took := time.Since(start); took > haFailover {
		t.Fatalf("failover took %s", took)
	}
	_, st := c.nodes[next].REST("GET", "/api/v1/ha", nil)
	if st["backend"] != "postgres" || st["instance"] != "node-"+strconv.Itoa(next) || st["leader_instance"] != st["instance"] {
		t.Fatalf("ha status %v", st)
	}

	askOnce(t, c, sink, asker, 2)
	mergeOnce(t, c, g, ids[1], 2)
	if d := sink.duplicates(); len(d) > 0 {
		t.Fatalf("duplicate notifications: %v", d)
	}
}

func TestHALeaseHeartbeatsOnFollowers(t *testing.T) {
	c := startCluster(t, 3, 2*time.Second)
	lead := c.node(0).Register("rex", "leader", "other")
	wk := c.node(0).Register("w1", "worker", "claude-code")
	_, br := c.node(0).REST("POST", "/api/v1/briefs", map[string]any{"title": "Lease", "repos": []string{"o/r"}})
	plan := c.node(0).Must(lead, "propose_plan", map[string]any{"brief_id": br["id"], "tasks": []any{map[string]any{"title": "a", "spec": "s", "repo": "o/r"}}})["plan"].(map[string]any)
	c.node(0).REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{})
	id := plan["tasks"].([]any)[0].(map[string]any)["id"].(string)
	c.node(0).Must(lead, "assign", map[string]any{"task_id": id, "agent_id": "w1"})

	f := c.follower()
	f.Must(wk, "claim_task", map[string]any{"task_id": id})
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(400 * time.Millisecond) {
		c.follower().Must(wk, "heartbeat", map[string]any{"task_id": id})
		_, tk := c.nodes[c.leader()].REST("GET", "/api/v1/tasks/"+id, nil)
		if tk["status"] != "in_progress" {
			t.Fatalf("live lease expired by the leader's sweeper: %v", tk)
		}
	}
	waitFor(t, "abandoned lease to expire", func() bool {
		_, tk := c.node(0).REST("GET", "/api/v1/tasks/"+id, nil)
		return tk["status"] == "assigned"
	})
}

type sseReader struct {
	mu     sync.Mutex
	events []string
}

func (s *sseReader) read(resp *http.Response) {
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	typ := ""
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "event: "); ok {
			typ = v
		}
		if v, ok := strings.CutPrefix(line, "data: "); ok {
			s.mu.Lock()
			s.events = append(s.events, typ+" "+v)
			s.mu.Unlock()
		}
	}
}

func (s *sseReader) has(typ, needle string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.events {
		if strings.HasPrefix(e, typ+" ") && strings.Contains(e, needle) {
			n++
		}
	}
	return n
}

func subscribe(ctx context.Context, t *testing.T, e *servertest.Env, topics string) *sseReader {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "GET", e.TS.URL+"/api/v1/events?topics="+topics, nil)
	req.Header.Set("Authorization", "Bearer "+servertest.OwnerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("events: %d", resp.StatusCode)
	}
	r := &sseReader{}
	go func() {
		defer func() { _ = resp.Body.Close() }()
		r.read(resp)
	}()
	return r
}

func TestHASSEFanOutAcrossInstances(t *testing.T) {
	c := startCluster(t, 3, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var streams []*sseReader
	for i := range c.nodes {
		streams = append(streams, subscribe(ctx, t, c.nodes[i], "all"))
	}
	wk := c.node(0).Register("w1", "worker", "claude-code")
	small := "fan-out-small"
	large := "fan-out-large-" + strings.Repeat("x", 12<<10)
	c.node(1).Must(wk, "post_message", map[string]any{"thread": "agent:me", "body": small})
	c.node(2).Must(wk, "post_message", map[string]any{"thread": "agent:me", "body": large})
	for i, s := range streams {
		waitFor(t, fmt.Sprintf("events on node %d", i), func() bool {
			return s.has("message.created", small) == 1 && s.has("message.created", large[:64]) == 1 && s.has("agent.updated", `"w1"`) >= 1
		})
	}
	time.Sleep(300 * time.Millisecond)
	for i, s := range streams {
		if s.has("message.created", small) != 1 || s.has("message.created", large[:64]) != 1 {
			t.Fatalf("node %d delivered an event more than once", i)
		}
	}
}

func TestHASharedCachesFollowWrites(t *testing.T) {
	c := startCluster(t, 3, time.Minute)
	wk := c.node(0).Register("w1", "worker", "claude-code")
	for i := range c.nodes {
		c.nodes[i].Must(wk, "whoami", map[string]any{})
	}
	if code, out := c.node(0).REST("POST", "/api/v1/agents/w1/revoke", map[string]any{"reason": "gone"}); code != 200 {
		t.Fatalf("revoke: %d %v", code, out)
	}
	for i := range c.nodes {
		waitFor(t, fmt.Sprintf("revocation on node %d", i), func() bool {
			r, _ := c.nodes[i].RPC(wk, "tools/call", map[string]any{"name": "whoami", "arguments": map[string]any{}})["result"].(map[string]any)
			return r == nil || r["isError"] == true
		})
	}
	c.node(1).REST("POST", "/api/v1/pause-all", map[string]any{"reason": "maintenance"})
	for i := range c.nodes {
		waitFor(t, fmt.Sprintf("pause on node %d", i), func() bool {
			_, st := c.nodes[i].REST("GET", "/healthz", nil)
			return st["paused"] == true
		})
	}
}

func TestHAPerInstanceLoopGuardBound(t *testing.T) {
	c := startCluster(t, 3, time.Minute)
	wk := c.node(0).Register("w1", "worker", "claude-code")
	ok := 0
	for i := range 3*audit.DefaultMaxRepeats + 1 {
		out, _ := c.node(i).Call(wk, "add_note", map[string]any{"task_id": "T-99", "body": "x"})
		if code, _ := out["error"].(map[string]any)["code"].(string); code == "agent_paused" {
			break
		}
		ok++
	}
	if ok != 3*audit.DefaultMaxRepeats {
		t.Fatalf("loop guard allowed %d identical calls across 3 instances, want the per-instance bound %d", ok, 3*audit.DefaultMaxRepeats)
	}
	for i := range c.nodes {
		c.nodes[i].MustFail(wk, "add_note", map[string]any{"body": "other"}, "agent_paused")
	}
}

func TestHAPerInstanceRateLimitBound(t *testing.T) {
	t.Setenv("SUBSPACE_RATE_MCP", "1/m:2")
	c := startCluster(t, 3, time.Minute)
	wk := c.node(0).Register("w1", "worker", "claude-code")
	ok := 0
	for i := range 3*2 + 3 {
		res := c.node(i).RPC(wk, "tools/call", map[string]any{"name": "whoami", "arguments": map[string]any{}})
		if res["result"] != nil {
			ok++
		}
	}
	if ok != 3*2 {
		t.Fatalf("rate limit allowed %d calls across 3 instances, want the per-instance bound %d", ok, 3*2)
	}
}

func TestHAIdempotencyKeyAcrossInstances(t *testing.T) {
	c := startCluster(t, 3, time.Minute)
	wk := c.node(0).Register("w1", "worker", "claude-code")
	var wg sync.WaitGroup
	results := make([]string, 6)
	for i := range results {
		wg.Go(func() {
			res := c.node(i).RPC(wk, "tools/call", map[string]any{"name": "post_message",
				"arguments": map[string]any{"thread": "agent:me", "body": "exactly once"},
				"_meta":     map[string]any{"idempotencyKey": "k-1"}})
			b, _ := json.Marshal(res["result"])
			results[i] = string(b)
		})
	}
	wg.Wait()
	for _, r := range results[1:] {
		if r != results[0] {
			t.Fatalf("replayed results differ:\n%s\n%s", results[0], r)
		}
	}
	_, out := c.node(1).REST("GET", "/api/v1/threads/agent/w1/messages", nil)
	n := 0
	for _, m := range out["messages"].([]any) {
		if m.(map[string]any)["body"] == "exactly once" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("idempotent call executed %d times", n)
	}
}

func TestHALoadThroughRoundRobinProxy(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	c := startCluster(t, 3, time.Minute)
	var next atomic.Uint64
	var proxies []*httputil.ReverseProxy
	for _, n := range c.nodes {
		u, _ := url.Parse(n.TS.URL)
		p := httputil.NewSingleHostReverseProxy(u)
		p.FlushInterval = -1
		proxies = append(proxies, p)
	}
	lb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxies[next.Add(1)%uint64(len(proxies))].ServeHTTP(w, r)
	}))
	defer lb.Close()
	rep, err := loadtest.Run(context.Background(), loadtest.Config{
		URL: lb.URL, OwnerToken: servertest.OwnerToken, Agents: 20, Duration: 5 * time.Second,
		SSESubs: 10, Think: 5 * time.Millisecond, Drain: 15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Breached() || rep.Tasks.Done == 0 {
		t.Fatalf("errors %d of %d (%v), done %d", rep.Errors, rep.Requests, rep.ErrorSamples, rep.Tasks.Done)
	}
}
