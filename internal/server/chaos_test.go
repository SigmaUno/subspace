package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	chaosSeed  = 20261120
	chaosLease = time.Second
	chaosSweep = 30 * time.Millisecond
)

type chaosNode struct {
	srv    *Server
	ts     *httptest.Server
	cancel context.CancelFunc
	done   chan struct{}
}

type chaosClock struct{ skew atomic.Int64 }

func (c *chaosClock) now() time.Time {
	return time.Now().UTC().Add(time.Duration(c.skew.Load()))
}

func (c *chaosClock) advance(d time.Duration) { c.skew.Add(int64(d)) }

func startChaosNode(t *testing.T, db string, clock *chaosClock) *chaosNode {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s, err := New(ctx, Config{DBPath: db, OwnerToken: ownerTok, PublicURL: "http://sub.test", Lease: chaosLease, Now: clock.now,
		FlushEvery: chaosSweep, SweepEvery: chaosSweep, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	n := &chaosNode{srv: s, cancel: cancel, done: make(chan struct{})}
	go func() {
		s.Board.Run(ctx)
		close(n.done)
	}()
	n.ts = httptest.NewServer(s.Handler())
	return n
}

func (n *chaosNode) crash() {
	n.ts.CloseClientConnections()
	_ = n.srv.Close()
	n.cancel()
	n.ts.Close()
	<-n.done
}

type chaosClient struct {
	base atomic.Pointer[string]
	hc   *http.Client
}

func newChaosClient() *chaosClient {
	return &chaosClient{hc: &http.Client{Timeout: 10 * time.Second}}
}

func (c *chaosClient) point(n *chaosNode) { u := n.ts.URL; c.base.Store(&u) }

func (c *chaosClient) do(ctx context.Context, tok, method, path string, body any) (int, map[string]any, error) {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, *c.base.Load()+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, out, nil
}

func (c *chaosClient) tool(ctx context.Context, tok, name string, args any) (map[string]any, string, error) {
	if args == nil {
		args = map[string]any{}
	}
	code, out, err := c.do(ctx, tok, "POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args}})
	if err != nil {
		return nil, "", err
	}
	if code != 200 {
		return nil, "", fmt.Errorf("%s: http %d", name, code)
	}
	if out["error"] != nil {
		return nil, "rpc", nil
	}
	r, _ := out["result"].(map[string]any)
	sc, _ := r["structuredContent"].(map[string]any)
	if r["isError"] == true {
		e, _ := sc["error"].(map[string]any)
		c, _ := e["code"].(string)
		return sc, c, nil
	}
	return sc, "", nil
}

type chaosAgent struct {
	id, role, tok string
	rng           *rand.Rand
}

type chaosWorld struct {
	t       *testing.T
	c       *chaosClient
	db      string
	clock   *chaosClock
	node    *chaosNode
	brief   string
	agents  []*chaosAgent
	workers []string
	planIDs []string
	seq     atomic.Int64

	mu      sync.Mutex
	created map[string]bool
	acked   map[string]int
	maxRR   map[string]float64
	maxCost map[string]float64
}

func (w *chaosWorld) ack(task, tool, to string) {
	w.mu.Lock()
	w.acked[task+"|"+tool+"|"+to]++
	w.mu.Unlock()
}

func (w *chaosWorld) owner(method, path string, body any) map[string]any {
	w.t.Helper()
	code, out, err := w.c.do(context.Background(), ownerTok, method, path, body)
	if err != nil || code >= 300 {
		w.t.Fatalf("%s %s: %d %v %v", method, path, code, out, err)
	}
	return out
}

func taskOf(m map[string]any) map[string]any {
	t, _ := m["task"].(map[string]any)
	return t
}

func (w *chaosWorld) step(ctx context.Context, a *chaosAgent) {
	r := a.rng
	n := w.seq.Add(1)
	var (
		out  map[string]any
		code string
		err  error
	)
	switch a.role {
	case "worker":
		out, code, err = w.c.tool(ctx, a.tok, "list_my_tasks", nil)
		if err != nil || code != "" {
			break
		}
		var mine, claimable []map[string]any
		for _, x := range out["tasks"].([]any) {
			tk := x.(map[string]any)
			switch tk["status"] {
			case "in_progress":
				mine = append(mine, tk)
			case "assigned", "changes_requested":
				claimable = append(claimable, tk)
			}
		}
		switch {
		case len(mine) > 0:
			tk := mine[r.IntN(len(mine))]
			id := tk["id"].(string)
			switch x := r.IntN(20); {
			case x < 8:
				_, code, err = w.c.tool(ctx, a.tok, "heartbeat", map[string]any{"task_id": id})
			case x < 14:
				cost := tk["cost_used"].(float64) + 0.01*float64(1+r.IntN(5))
				_, code, err = w.c.tool(ctx, a.tok, "update_status", map[string]any{"task_id": id, "status": "in_progress",
					"progress_pct": r.IntN(100), "cost_eur": cost, "note": fmt.Sprint("n", n)})
			case x < 19:
				out, code, err = w.c.tool(ctx, a.tok, "submit", map[string]any{"task_id": id,
					"pr_url": fmt.Sprintf("https://git.example/o/r/pulls/%d", n), "summary": "s"})
				if err == nil && code == "" {
					w.ack(id, "submit", taskOf(out)["status"].(string))
				}
			default:
				_, code, err = w.c.tool(ctx, a.tok, "release_task", map[string]any{"task_id": id, "reason": fmt.Sprint(n)})
			}
		case len(claimable) > 0:
			id := claimable[r.IntN(len(claimable))]["id"].(string)
			out, code, err = w.c.tool(ctx, a.tok, "claim_task", map[string]any{"task_id": id})
			if err == nil && code == "" {
				w.ack(id, "claim_task", taskOf(out)["status"].(string))
			}
		}
	case "reviewer":
		out, code, err = w.c.tool(ctx, a.tok, "list_reviews", nil)
		if err != nil || code != "" {
			break
		}
		ts := out["tasks"].([]any)
		if len(ts) == 0 {
			break
		}
		id := ts[r.IntN(len(ts))].(map[string]any)["id"].(string)
		verdict := "approve"
		if r.IntN(10) < 4 {
			verdict = "changes_requested"
		}
		out, code, err = w.c.tool(ctx, a.tok, "review", map[string]any{"task_id": id, "verdict": verdict, "comments": fmt.Sprint("c", n)})
		if err == nil && code == "" {
			w.ack(id, "review", taskOf(out)["status"].(string))
		}
	case "leader":
		if r.IntN(4) == 0 {
			out, code, err = w.c.tool(ctx, a.tok, "create_task", map[string]any{"brief_id": w.brief,
				"title": fmt.Sprint("extra ", n), "spec": "s", "repo": "o/r"})
			if err == nil && code == "" {
				w.mu.Lock()
				w.created[taskOf(out)["id"].(string)] = true
				w.mu.Unlock()
			}
			break
		}
		out, code, err = w.c.tool(ctx, a.tok, "board", map[string]any{"brief_id": w.brief})
		if err != nil || code != "" {
			break
		}
		ready, _ := out["columns"].(map[string]any)["ready"].([]any)
		for _, x := range ready {
			id := x.(map[string]any)["id"].(string)
			_, code, err = w.c.tool(ctx, a.tok, "assign", map[string]any{"task_id": id, "agent_id": w.workers[r.IntN(len(w.workers))]})
			if err != nil {
				break
			}
		}
	case "owner":
		var st int
		st, out, err = w.c.do(ctx, ownerTok, "GET", "/api/v1/tasks?status=approved,needs_owner&limit=1000", nil)
		if err != nil || st != 200 {
			break
		}
		ts := out["tasks"].([]any)
		if len(ts) == 0 {
			break
		}
		id := ts[r.IntN(len(ts))].(map[string]any)["id"].(string)
		st, _, err = w.c.do(ctx, ownerTok, "POST", "/api/v1/tasks/"+id+"/done", map[string]any{"note": "merged"})
		if err == nil && st == 200 {
			w.ack(id, "mark_done", "done")
		}
	}
	if err == nil && code == "agent_paused" {
		_, _, _ = w.c.do(ctx, ownerTok, "POST", "/api/v1/agents/"+a.id+"/resume", map[string]any{})
	}
}

func (w *chaosWorld) load(ctx context.Context) *sync.WaitGroup {
	var wg sync.WaitGroup
	for _, a := range w.agents {
		wg.Go(func() {
			for ctx.Err() == nil {
				w.step(ctx, a)
				time.Sleep(time.Duration(2+a.rng.IntN(10)) * time.Millisecond)
			}
		})
	}
	return &wg
}

func (w *chaosWorld) done() int {
	n := 0
	for _, tk := range w.tasks() {
		if tk["status"] == "done" {
			n++
		}
	}
	return n
}

func (w *chaosWorld) tasks() []map[string]any {
	var out []map[string]any
	for _, x := range w.owner("GET", "/api/v1/tasks?limit=1000", nil)["tasks"].([]any) {
		out = append(out, x.(map[string]any))
	}
	return out
}

func (w *chaosWorld) audit() []map[string]any {
	var out []map[string]any
	var since int64
	for {
		evs := w.owner("GET", fmt.Sprintf("/api/v1/audit?since=%d&limit=1000", since), nil)["events"].([]any)
		for _, x := range evs {
			out = append(out, x.(map[string]any))
		}
		if len(evs) < 1000 {
			return out
		}
		since = int64(out[len(out)-1]["id"].(float64))
	}
}

func leaseOf(t *testing.T, tk map[string]any) (time.Time, bool) {
	s, ok := tk["lease_expires_at"].(string)
	if !ok {
		return time.Time{}, false
	}
	l, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("lease %q: %v", s, err)
	}
	return l, true
}

func integrityOK(t *testing.T, db string) {
	t.Helper()
	conn, err := sql.Open("sqlite", "file:"+db+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	var res string
	if err := conn.QueryRow("PRAGMA integrity_check").Scan(&res); err != nil || res != "ok" {
		t.Fatalf("integrity_check: %q %v", res, err)
	}
}

func statusesOf(ts []map[string]any) map[string]string {
	out := map[string]string{}
	for _, tk := range ts {
		out[tk["id"].(string)] = tk["status"].(string)
	}
	return out
}

func (w *chaosWorld) replay(label string) (map[string]string, map[string]int, int) {
	state := map[string]string{}
	for _, id := range w.planIDs {
		state[id] = "ready"
	}
	creates := 0
	seen := map[string]int{}
	for _, ev := range w.audit() {
		tool, _ := ev["tool"].(string)
		if tool == "create_task" {
			creates++
		}
		tid, _ := ev["task_id"].(string)
		after, _ := ev["after"].(map[string]any)
		to, ok := after["status"].(string)
		if tid == "" || !ok {
			continue
		}
		if b, _ := ev["before"].(map[string]any); b != nil {
			if from, ok := b["status"].(string); ok && from != state[tid] {
				w.t.Fatalf("%s: audit chain broken for %s at event %v: before %s, replayed %s", label, tid, ev["id"], from, state[tid])
			}
		}
		state[tid] = to
		seen[tid+"|"+tool+"|"+to]++
	}
	return state, seen, creates
}

func (w *chaosWorld) sweep() {
	w.t.Helper()
	if err := w.node.srv.Board.Sweep(context.Background()); err != nil {
		w.t.Fatalf("sweep: %v", err)
	}
}

func (w *chaosWorld) verify(label string) {
	t := w.t
	t.Helper()
	integrityOK(t, w.db)

	for _, a := range w.agents {
		if a.role == "owner" {
			continue
		}
		out, code, err := w.c.tool(context.Background(), a.tok, "whoami", nil)
		if err != nil || code != "" || out["agent"].(map[string]any)["id"] != a.id {
			t.Fatalf("%s: token of %s does not authenticate: %v %q %v", label, a.id, out, code, err)
		}
	}

	t0 := w.clock.now()
	before := w.tasks()
	w.sweep()
	ts := w.tasks()
	byID := map[string]map[string]any{}
	for _, tk := range ts {
		id := tk["id"].(string)
		if byID[id] != nil {
			t.Fatalf("%s: duplicate task %s", label, id)
		}
		byID[id] = tk
	}
	for _, tk := range before {
		id := tk["id"].(string)
		if tk["status"] != "in_progress" {
			continue
		}
		if l, ok := leaseOf(t, tk); ok && l.Before(t0) && byID[id]["status"] != "assigned" {
			t.Fatalf("%s: %s lease expired at %v but status is %v after sweep", label, id, l, byID[id]["status"])
		}
	}
	for id, tk := range byID {
		if tk["status"] == "in_progress" {
			if _, ok := leaseOf(t, tk); !ok {
				t.Fatalf("%s: %s in_progress without lease", label, id)
			}
		}
	}

	w.mu.Lock()
	for id := range w.created {
		if byID[id] == nil {
			t.Fatalf("%s: created task %s lost", label, id)
		}
	}
	for _, tk := range ts {
		id := tk["id"].(string)
		rr, cost := tk["review_rounds"].(float64), tk["cost_used"].(float64)
		if rr < w.maxRR[id] || cost < w.maxCost[id] {
			t.Fatalf("%s: %s went backwards: review_rounds %v<%v or cost_used %v<%v", label, id, rr, w.maxRR[id], cost, w.maxCost[id])
		}
		w.maxRR[id], w.maxCost[id] = rr, cost
	}
	acked := map[string]int{}
	for k, v := range w.acked {
		acked[k] = v
	}
	w.mu.Unlock()

	var (
		state   map[string]string
		seen    map[string]int
		creates int
		got     map[string]any
	)
	statuses := statusesOf(ts)
	for attempt := 0; ; attempt++ {
		state, seen, creates = w.replay(label)
		got = w.owner("GET", "/api/v1/state", nil)["counts"].(map[string]any)["tasks"].(map[string]any)
		again := statusesOf(w.tasks())
		if maps.Equal(statuses, again) {
			break
		}
		if attempt == 10 {
			t.Fatalf("%s: board never quiesced for a consistent snapshot", label)
		}
		statuses = again
	}
	if want := len(w.planIDs) + creates; len(statuses) != want {
		t.Fatalf("%s: %d tasks, audit implies %d", label, len(statuses), want)
	}
	counts := map[string]float64{}
	for id, s := range statuses {
		if state[id] != s {
			t.Fatalf("%s: %s is %s, audit replay says %s", label, id, s, state[id])
		}
		counts[s]++
	}
	for s, n := range counts {
		if got[s] != n {
			t.Fatalf("%s: state count %s=%v, audit replay %v (%v)", label, s, got[s], n, got)
		}
	}
	for s, n := range got {
		if counts[s] != n.(float64) {
			t.Fatalf("%s: state count %s=%v, audit replay %v", label, s, n, counts[s])
		}
	}
	for k, n := range acked {
		if seen[k] < n {
			t.Fatalf("%s: acknowledged %s x%d, audit has %d", label, k, n, seen[k])
		}
	}
}

func TestChaosRestartUnderLoad(t *testing.T) {
	db := filepath.Join(t.TempDir(), "chaos.db")
	rng := rand.New(rand.NewPCG(chaosSeed, 0))
	t.Logf("seed %d", chaosSeed)
	clock := &chaosClock{}
	w := &chaosWorld{t: t, c: newChaosClient(), db: db, clock: clock, created: map[string]bool{}, acked: map[string]int{},
		maxRR: map[string]float64{}, maxCost: map[string]float64{}}

	node := startChaosNode(t, db, clock)
	w.node = node
	w.c.point(node)
	defer func() { node.crash() }()

	reg := func(id, role, kind string) {
		out := w.owner("POST", "/api/v1/agents", map[string]any{"id": id, "role": role, "kind": kind, "model": "m"})
		tok := out["connect"].(map[string]any)["token"].(string)
		w.agents = append(w.agents, &chaosAgent{id: id, role: role, tok: tok, rng: rand.New(rand.NewPCG(chaosSeed, uint64(len(w.agents)+1)))})
	}
	reg("rex", "leader", "other")
	for i := range 8 {
		id := fmt.Sprintf("claude-%d", i)
		reg(id, "worker", "claude-code")
		w.workers = append(w.workers, id)
	}
	reg("codex-r1", "reviewer", "codex")
	reg("codex-r2", "reviewer", "codex")
	w.agents = append(w.agents, &chaosAgent{id: "owner", role: "owner", rng: rand.New(rand.NewPCG(chaosSeed, 99))})

	w.brief = w.owner("POST", "/api/v1/briefs", map[string]any{"title": "Chaos", "repos": []string{"o/r"}})["id"].(string)
	var specs []any
	for i := range 40 {
		specs = append(specs, map[string]any{"title": fmt.Sprint("task ", i), "spec": "s", "repo": "o/r"})
	}
	out, code, err := w.c.tool(context.Background(), w.agents[0].tok, "propose_plan", map[string]any{"brief_id": w.brief, "tasks": specs})
	if err != nil || code != "" {
		t.Fatalf("propose_plan: %v %q %v", out, code, err)
	}
	plan := out["plan"].(map[string]any)
	w.owner("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{})
	for i, x := range plan["tasks"].([]any) {
		id := x.(map[string]any)["id"].(string)
		w.planIDs = append(w.planIDs, id)
		if _, code, err := w.c.tool(context.Background(), w.agents[0].tok, "assign", map[string]any{"task_id": id, "agent_id": w.workers[i%len(w.workers)]}); err != nil || code != "" {
			t.Fatalf("assign %s: %q %v", id, code, err)
		}
	}
	w.verify("setup")

	for round := range 24 {
		ctx, cancel := context.WithCancel(context.Background())
		wg := w.load(ctx)
		time.Sleep(time.Duration(80+rng.IntN(250)) * time.Millisecond)
		node.crash()
		cancel()
		wg.Wait()
		node = startChaosNode(t, db, clock)
		w.node = node
		w.c.point(node)
		w.verify(fmt.Sprint("round ", round))
	}

	ctx, cancel := context.WithCancel(context.Background())
	wg := w.load(ctx)
	for deadline := time.Now().Add(60 * time.Second); w.done() == 0 && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	wg.Wait()

	clock.advance(chaosLease + chaosSweep)
	w.sweep()
	for _, tk := range w.tasks() {
		if tk["status"] == "in_progress" {
			t.Fatalf("%s still in_progress after every lease expired: %v", tk["id"], tk["lease_expires_at"])
		}
	}
	w.verify("final")
	done := w.done()
	if done == 0 {
		t.Fatal("workload made no progress: no task reached done")
	}
	t.Logf("done %d/%d tasks, %d calls", done, len(w.tasks()), w.seq.Load())
}

func TestChaosBulkRegistrationCrash(t *testing.T) {
	db := filepath.Join(t.TempDir(), "bulk.db")
	rng := rand.New(rand.NewPCG(chaosSeed, 1))
	c := newChaosClient()
	clock := &chaosClock{}
	const count = 200
	for i := range 5 {
		node := startChaosNode(t, db, clock)
		c.point(node)
		prefix := fmt.Sprintf("bulk%d", i)
		type result struct {
			code int
			out  map[string]any
			err  error
		}
		res := make(chan result, 1)
		go func() {
			code, out, err := c.do(context.Background(), ownerTok, "POST", "/api/v1/agents/bulk",
				map[string]any{"prefix": prefix, "count": count, "role": "worker", "kind": "codex", "model": "m"})
			res <- result{code, out, err}
		}()
		time.Sleep(time.Duration(rng.IntN(800)) * time.Millisecond)
		node.crash()
		r := <-res

		node = startChaosNode(t, db, clock)
		c.point(node)
		_, list, err := c.do(context.Background(), ownerTok, "GET", "/api/v1/agents?limit=1000", nil)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, x := range list["agents"].([]any) {
			if strings.HasPrefix(x.(map[string]any)["id"].(string), prefix+"-") {
				n++
			}
		}
		if n != 0 && n != count {
			t.Fatalf("bulk %s partially applied: %d of %d agents", prefix, n, count)
		}
		_, au, err := c.do(context.Background(), ownerTok, "GET", "/api/v1/audit?tool=register_agents&limit=1000", nil)
		if err != nil {
			t.Fatal(err)
		}
		events := 0
		for _, x := range au["events"].([]any) {
			if x.(map[string]any)["after"].(map[string]any)["first"] == prefix+"-001" {
				events++
			}
		}
		if events != n/count {
			t.Fatalf("bulk %s: %d agents but %d audit events", prefix, n, events)
		}
		if r.err == nil && r.code == 201 {
			if n != count {
				t.Fatalf("bulk %s acknowledged but %d agents persisted", prefix, n)
			}
			for _, x := range r.out["agents"].([]any) {
				ag := x.(map[string]any)
				tok := ag["connect"].(map[string]any)["token"].(string)
				out, code, err := c.tool(context.Background(), tok, "whoami", nil)
				if err != nil || code != "" || out["agent"].(map[string]any)["id"] != ag["agent"].(map[string]any)["id"] {
					t.Fatalf("bulk token does not authenticate after restart: %v %q %v", out, code, err)
				}
			}
		}
		t.Logf("%s: http %d err %v, persisted %d", prefix, r.code, r.err, n)
		node.crash()
		integrityOK(t, db)
	}
}
