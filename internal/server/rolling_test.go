package server_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/postgres"
)

const (
	rollLease       = 2 * time.Second
	rollInterval    = 100 * time.Millisecond
	rollDrainDelay  = 300 * time.Millisecond
	rollLBTry       = 3 * time.Second
	rollWorkers     = 4
	rollMaxAttempts = 40
)

type rollNode struct {
	addr   string
	srv    *server.Server
	cancel context.CancelFunc
	done   chan error
	up     bool
}

type rollCluster struct {
	dsn   string
	data  string
	nodes []*rollNode
}

func (c *rollCluster) config(i int) server.Config {
	return server.Config{
		DBPath: c.dsn, DataDir: c.data, BackupDir: filepath.Join(c.data, "backups"), OwnerToken: servertest.OwnerToken,
		PublicURL: "http://sub.test", InstanceID: fmt.Sprintf("roll-%d", i), Lease: rollLease,
		FlushEvery: rollInterval, SweepEvery: rollInterval, LeaderInterval: rollInterval, LeaderTTL: 5 * rollInterval,
		DrainDelay: rollDrainDelay, ShutdownTimeout: 5 * time.Second, Log: slog.New(slog.DiscardHandler),
	}
}

func (c *rollCluster) start(i int) error {
	n := c.nodes[i]
	ctx, cancel := context.WithCancel(context.Background())
	s, err := server.New(ctx, c.config(i))
	if err != nil {
		cancel()
		return err
	}
	ln, err := net.Listen("tcp", n.addr)
	if err != nil {
		cancel()
		_ = s.Close()
		return err
	}
	n.srv, n.cancel, n.done, n.up = s, cancel, make(chan error, 1), true
	go func() {
		err := s.Serve(ctx, ln)
		n.done <- errors.Join(err, s.Close())
	}()
	return nil
}

func (c *rollCluster) stop(i int) error {
	n := c.nodes[i]
	n.cancel()
	return <-n.done
}

func ready(addr string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/readyz", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

type balancer struct {
	addrs  []string
	up     []atomic.Bool
	next   atomic.Uint64
	client *http.Client
}

func newBalancer(ctx context.Context, addrs []string) *balancer {
	b := &balancer{addrs: addrs, up: make([]atomic.Bool, len(addrs)), client: &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 64}}}
	go func() {
		t := time.NewTicker(rollInterval)
		defer t.Stop()
		for {
			for i, a := range b.addrs {
				b.up[i].Store(ready(a))
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return b
}

func (b *balancer) pick() int {
	for range b.addrs {
		i := int(b.next.Add(1) % uint64(len(b.addrs)))
		if b.up[i].Load() {
			return i
		}
	}
	return -1
}

func (b *balancer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	deadline := time.Now().Add(rollLBTry)
	for {
		if i := b.pick(); i >= 0 {
			req, _ := http.NewRequestWithContext(r.Context(), r.Method, "http://"+b.addrs[i]+r.URL.RequestURI(), bytes.NewReader(body))
			req.Header = r.Header.Clone()
			resp, err := b.client.Do(req)
			if err == nil {
				for k, v := range resp.Header {
					w.Header()[k] = v
				}
				w.WriteHeader(resp.StatusCode)
				_, _ = io.Copy(w, resp.Body)
				_ = resp.Body.Close()
				return
			}
			var op *net.OpError
			if !errors.As(err, &op) || op.Op != "dial" {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
		}
		if time.Now().After(deadline) {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

type loadStats struct {
	calls, retries, failures atomic.Int64
	asked                    atomic.Int64
	mu                       sync.Mutex
	samples                  []string
}

func (s *loadStats) fail(msg string) {
	s.failures.Add(1)
	s.mu.Lock()
	if len(s.samples) < 10 {
		s.samples = append(s.samples, msg)
	}
	s.mu.Unlock()
}

func (s *loadStats) call(ctx context.Context, url, tok, tool string, args map[string]any, key string) bool {
	params := map[string]any{"name": tool, "arguments": args}
	if key != "" {
		params["_meta"] = map[string]any{"idempotencyKey": key}
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params})
	s.calls.Add(1)
	for attempt := range rollMaxAttempts {
		if attempt > 0 {
			s.retries.Add(1)
			time.Sleep(50 * time.Millisecond)
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url+"/mcp", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if ctx.Err() != nil {
			s.calls.Add(-1)
			return false
		}
		if err != nil {
			continue
		}
		var out struct {
			Error  any `json:"error"`
			Result struct {
				IsError bool `json:"isError"`
				Content any  `json:"structuredContent"`
			} `json:"result"`
		}
		derr := json.NewDecoder(resp.Body).Decode(&out)
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusBadGateway, resp.StatusCode == http.StatusServiceUnavailable, resp.StatusCode == http.StatusGatewayTimeout:
			continue
		case resp.StatusCode != http.StatusOK || derr != nil:
			s.fail(fmt.Sprintf("%s: HTTP %d %v", tool, resp.StatusCode, derr))
		case out.Error != nil:
			s.fail(fmt.Sprintf("%s: rpc error %v", tool, out.Error))
		case out.Result.IsError:
			s.fail(fmt.Sprintf("%s: %v", tool, out.Result.Content))
		default:
			return true
		}
		return false
	}
	s.fail(tool + ": retries exhausted")
	return false
}

func TestRollingRestartUnderLoad(t *testing.T) {
	if !strings.HasPrefix(os.Getenv("SUBSPACE_TEST_PG_DSN"), "postgres") {
		t.Skip("SUBSPACE_TEST_PG_DSN not set")
	}
	for _, k := range []string{"SUBSPACE_RATE_MCP", "SUBSPACE_RATE_AUTHFAIL"} {
		t.Setenv(k, "off")
	}
	sink := &notifySink{}
	hook := httptest.NewServer(sink)
	defer hook.Close()
	t.Setenv(notify.EnvWebhookURL, hook.URL)
	t.Setenv(notify.EnvAllowPrivate, "true")

	c := &rollCluster{dsn: servertest.DB(t), data: t.TempDir()}
	var addrs []string
	for range 2 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addrs = append(addrs, ln.Addr().String())
		_ = ln.Close()
		c.nodes = append(c.nodes, &rollNode{addr: addrs[len(addrs)-1]})
	}

	errs := make([]error, len(c.nodes))
	var wg sync.WaitGroup
	for i := range c.nodes {
		wg.Go(func() { errs[i] = c.start(i) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent start of node %d on a fresh database: %v", i, err)
		}
	}
	t.Cleanup(func() {
		for i, n := range c.nodes {
			if n.up {
				n.up = false
				_ = c.stop(i)
			}
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lb := httptest.NewServer(newBalancer(ctx, addrs))
	defer lb.Close()
	waitFor(t, "both nodes ready", func() bool { return ready(addrs[0]) && ready(addrs[1]) })

	env := &servertest.Env{T: t, TS: &httptest.Server{URL: lb.URL}}
	lead := env.Register("rex", "leader", "other")
	asker := env.Register("asker", "worker", "claude-code")
	_, br := env.REST("POST", "/api/v1/briefs", map[string]any{"title": "Rolling", "repos": []string{"o/r"}, "max_parallel_agents": rollWorkers + 1})
	var specs []any
	for i := range rollWorkers {
		specs = append(specs, map[string]any{"title": fmt.Sprintf("Task %d", i), "spec": "s", "repo": "o/r"})
	}
	plan := env.Must(lead, "propose_plan", map[string]any{"brief_id": br["id"], "tasks": specs})["plan"].(map[string]any)
	if code, out := env.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	type worker struct{ id, tok, task string }
	var workers []worker
	for i, x := range plan["tasks"].([]any) {
		w := worker{id: fmt.Sprintf("w%d", i), task: x.(map[string]any)["id"].(string)}
		w.tok = env.Register(w.id, "worker", "claude-code")
		env.Must(lead, "assign", map[string]any{"task_id": w.task, "agent_id": w.id})
		env.Must(w.tok, "claim_task", map[string]any{"task_id": w.task})
		workers = append(workers, w)
	}

	stats := &loadStats{}
	load, stopLoad := context.WithCancel(ctx)
	var clients sync.WaitGroup
	for _, w := range workers {
		clients.Go(func() {
			for n := 0; load.Err() == nil; n++ {
				stats.call(load, lb.URL, w.tok, "heartbeat", map[string]any{"task_id": w.task}, "")
				if n%4 == 0 {
					stats.call(load, lb.URL, w.tok, "post_message", map[string]any{"thread": "task:" + w.task, "body": fmt.Sprintf("progress %d", n)}, fmt.Sprintf("%s-msg-%d", w.id, n))
				}
				time.Sleep(50 * time.Millisecond)
			}
		})
	}
	clients.Go(func() {
		for n := 0; load.Err() == nil; n++ {
			args := map[string]any{"question": fmt.Sprintf("rolling question %d?", n), "options": []string{"a", "b"}}
			if stats.call(load, lb.URL, asker, "ask_owner", args, fmt.Sprintf("ask-%d", n)) {
				stats.asked.Add(1)
			}
			time.Sleep(250 * time.Millisecond)
		}
	})

	time.Sleep(time.Second)
	handoffs := 0
	for i, n := range c.nodes {
		other := c.nodes[1-i]
		wasLeader := n.srv.Leader.IsLeader()
		stopped := make(chan error, 1)
		n.up = false
		go func() { stopped <- c.stop(i) }()
		if wasLeader {
			start := time.Now()
			waitFor(t, "hand-off to the other node", other.srv.Leader.IsLeader)
			select {
			case err := <-stopped:
				t.Fatalf("node %d finished shutting down (%v) before node %d took over", i, err, 1-i)
			default:
			}
			if took := time.Since(start); took > rollDrainDelay {
				t.Fatalf("hand-off took %s", took)
			}
			handoffs++
		}
		if err := <-stopped; err != nil {
			t.Fatalf("stop node %d: %v", i, err)
		}
		time.Sleep(200 * time.Millisecond)
		if err := c.start(i); err != nil {
			t.Fatalf("restart node %d: %v", i, err)
		}
		waitFor(t, fmt.Sprintf("node %d ready after restart", i), func() bool { return ready(n.addr) })
		time.Sleep(time.Second)
	}
	stopLoad()
	clients.Wait()

	if handoffs == 0 {
		t.Fatal("no leader was restarted")
	}
	calls := stats.calls.Load()
	if f := stats.failures.Load(); f > max(1, calls/200) {
		t.Fatalf("%d of %d client calls failed (%d retries): %v", f, calls, stats.retries.Load(), stats.samples)
	}
	t.Logf("%d calls, %d retries, %d failures, %d questions", calls, stats.retries.Load(), stats.failures.Load(), stats.asked.Load())

	for _, w := range workers {
		_, tk := env.REST("GET", "/api/v1/tasks/"+w.task, nil)
		if tk["status"] != "in_progress" || tk["assignee"] != w.id || tk["lease_expiries"] != float64(0) {
			t.Fatalf("lease lost across the rolling restart: %v", tk)
		}
	}
	waitFor(t, "question notifications", func() bool { return sink.count("question") > 0 })
	time.Sleep(500 * time.Millisecond)
	if d := sink.duplicates(); len(d) > 0 {
		t.Fatalf("duplicate notifications: %v", d)
	}
	if n := int64(sink.count("question")); n > stats.asked.Load() {
		t.Fatalf("%d question notifications for %d questions", n, stats.asked.Load())
	}
	if _, v := env.REST("GET", "/api/v1/audit/verify", nil); v["ok"] != true {
		t.Fatalf("audit chain after rolling restart: %v", v)
	}

	raw, err := sql.Open("pgx", c.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	var got store.SchemaVersion
	if err := raw.QueryRow(`SELECT version, hash FROM schema_version WHERE id = 1`).Scan(&got.Version, &got.Hash); err != nil {
		t.Fatal(err)
	}
	if got != postgres.BinarySchema() {
		t.Fatalf("schema_version %+v, binary %+v", got, postgres.BinarySchema())
	}
	if _, err := raw.Exec(`UPDATE schema_version SET version = version + 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := server.New(ctx, c.config(2)); !errors.Is(err, store.ErrSchemaNewer) {
		t.Fatalf("older binary started against a newer schema: %v", err)
	}
	cfg := c.config(2)
	cfg.SchemaReadOnly = true
	ro, err := server.New(ctx, cfg)
	if err != nil {
		t.Fatalf("read-only maintenance start: %v", err)
	}
	ts := httptest.NewServer(ro.Handler())
	roEnv := &servertest.Env{T: t, TS: ts}
	if code, _ := roEnv.REST("GET", "/readyz", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("read-only instance readyz %d", code)
	}
	if code, st := roEnv.REST("GET", "/api/v1/state", nil); code != http.StatusOK {
		t.Fatalf("read-only state: %d %v", code, st)
	}
	if code, _ := roEnv.REST("POST", "/api/v1/briefs", map[string]any{"title": "nope", "repos": []string{"o/r"}}); code < 400 {
		t.Fatalf("read-only instance accepted a write: %d", code)
	}
	ts.Close()
	_ = ro.Close()
	for i, a := range addrs {
		if !ready(a) {
			t.Fatalf("running node %d stopped being ready", i)
		}
	}
}

func TestReadyzFollowsDrain(t *testing.T) {
	e := servertest.New(t)
	get := func() (int, string) {
		resp, err := http.Get(e.TS.URL + "/readyz")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get(); code != http.StatusOK || body != "" {
		t.Fatalf("readyz %d %q", code, body)
	}
	if err := e.Srv.Close(); err != nil {
		t.Fatal(err)
	}
	if code, body := get(); code != http.StatusServiceUnavailable || body != "" {
		t.Fatalf("readyz while draining %d %q", code, body)
	}
}
