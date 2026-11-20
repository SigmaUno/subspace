// Package loadtest simulates a Subspace team (1 leader, N workers, reviewers, the owner and SSE
// subscribers) over the real REST and MCP HTTP endpoints and reports latency, throughput and errors
// (SPEC §11).
package loadtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"
)

// Config configures a load test run.
type Config struct {
	Agents        int           // simulated workers
	Duration      time.Duration // workload phase length
	URL           string        // external server; "" = start one in-process
	OwnerToken    string        // owner token of the external server
	SSESubs       int           // SSE subscribers
	MaxErrorRate  float64       // fraction 0..1 of failed requests tolerated
	TasksPerAgent int           // k: tasks created per worker up front
	Heartbeats    int           // m: heartbeats per task
	Reviewers     int           // 0 = auto (1 per 25 workers)
	Think         time.Duration // pause between an agent's calls
	Drain         time.Duration // grace period for reviews/done after Duration
	Log           *slog.Logger
}

// Defaults fills unset fields.
func (c *Config) Defaults() {
	if c.TasksPerAgent <= 0 {
		c.TasksPerAgent = 2
	}
	if c.Heartbeats < 0 {
		c.Heartbeats = 0
	}
	if c.Reviewers <= 0 {
		c.Reviewers = max(1, c.Agents/25)
	}
	if c.Drain <= 0 {
		c.Drain = 30 * time.Second
	}
	if c.Log == nil {
		c.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
}

// Validate checks the configuration.
func (c *Config) Validate() error {
	switch {
	case c.Agents < 1 || c.Agents > 1000:
		return errors.New("agents must be 1..1000")
	case c.Duration <= 0:
		return errors.New("duration must be > 0")
	case c.SSESubs < 0:
		return errors.New("sse-subs must be >= 0")
	case c.MaxErrorRate < 0 || c.MaxErrorRate > 1:
		return errors.New("max-error-rate must be within 0..1")
	case c.URL != "" && c.OwnerToken == "":
		return errors.New("owner-token is required with url")
	case c.Reviewers > 1000:
		return errors.New("reviewers must be <= 1000")
	}
	return nil
}

// TaskCounts counts task lifecycle steps completed by the simulation.
type TaskCounts struct {
	Created   int64 `json:"created"`
	Claimed   int64 `json:"claimed"`
	Submitted int64 `json:"submitted"`
	Approved  int64 `json:"approved"`
	Done      int64 `json:"done"`
}

// Report is the result of a run.
type Report struct {
	Target       string     `json:"target"`
	Agents       int        `json:"agents"`
	Reviewers    int        `json:"reviewers"`
	Elapsed      float64    `json:"elapsed_s"`
	Requests     int64      `json:"requests"`
	Errors       int64      `json:"errors"`
	ErrorRate    float64    `json:"error_rate"`
	MaxErrorRate float64    `json:"max_error_rate"`
	Throughput   float64    `json:"throughput_rps"`
	Overall      OpReport   `json:"overall"`
	Ops          []OpReport `json:"ops"`
	Tasks        TaskCounts `json:"tasks"`
	SSE          SSEReport  `json:"sse"`
	ErrorSamples []string   `json:"error_samples,omitempty"`
}

// Breached reports whether the error rate exceeds the configured maximum (or nothing was measured).
func (r *Report) Breached() bool { return r.Requests == 0 || r.ErrorRate > r.MaxErrorRate }

// WriteText prints a human-readable report.
func (r *Report) WriteText(w io.Writer) {
	_, _ = fmt.Fprintf(w, "Subspace load test against %s: %d workers, %d reviewers, %d SSE subscribers\n", r.Target, r.Agents, r.Reviewers, r.SSE.Subscribers)
	_, _ = fmt.Fprintf(w, "elapsed %.1fs, requests %d, throughput %.1f req/s, errors %d (rate %.4f, max %.4f)\n",
		r.Elapsed, r.Requests, r.Throughput, r.Errors, r.ErrorRate, r.MaxErrorRate)
	_, _ = fmt.Fprintf(w, "tasks: created %d, claimed %d, submitted %d, approved %d, done %d\n\n",
		r.Tasks.Created, r.Tasks.Claimed, r.Tasks.Submitted, r.Tasks.Approved, r.Tasks.Done)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	_, _ = fmt.Fprintln(tw, "op\tcount\terrors\tp50 ms\tp95 ms\tp99 ms\tmax ms\t")
	for _, o := range append(append([]OpReport{}, r.Ops...), r.Overall) {
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%d\t%.2f\t%.2f\t%.2f\t%.2f\t\n", o.Name, o.Count, o.Errors, o.P50ms, o.P95ms, o.P99ms, o.MaxMs)
	}
	_ = tw.Flush()
	s := r.SSE
	_, _ = fmt.Fprintf(w, "\nSSE: %d/%d connected, %d connect errors, %d stream errors, %d events, %d lagged (%d dropped), lag p50 %.2f ms p95 %.2f ms p99 %.2f ms max %.2f ms\n",
		s.Connected, s.Subscribers, s.ConnectErrors, s.StreamErrors, s.Events, s.LaggedEvents, s.Dropped, s.LagP50ms, s.LagP95ms, s.LagP99ms, s.LagMaxms)
	for _, e := range r.ErrorSamples {
		_, _ = fmt.Fprintf(w, "error sample: %s\n", e)
	}
	if r.Breached() {
		_, _ = fmt.Fprintln(w, "RESULT: FAIL (error rate above maximum or no requests)")
	} else {
		_, _ = fmt.Fprintln(w, "RESULT: OK")
	}
}

// wire types (subset of docs/api.md objects)
type taskObj struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type registered struct {
	Agent struct {
		ID string `json:"id"`
	} `json:"agent"`
	Connect struct {
		Token string `json:"token"`
	} `json:"connect"`
}

type agentCreds struct{ id, token string }

type refillReq struct {
	agent string
	reply chan string
}

type runner struct {
	cfg    Config
	rec    *Recorder
	owner  *ownerClient
	mcpURL string
	runID  string
	brief  string
	leader *mcpClient
	tasks  TaskCounts
	seq    atomic.Int64
	start  time.Time
}

const (
	repo        = "loadtest/sandbox"
	maxPlanSize = 500
	leaderConc  = 16
	ownerConc   = 8
)

// Run executes the load test and returns its report. A non-nil error means setup failed.
func Run(ctx context.Context, cfg Config) (*Report, error) {
	cfg.Defaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	target := cfg.URL
	if cfg.URL == "" {
		p, err := StartInProcess(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer p.Stop()
		cfg.URL, cfg.OwnerToken, target = p.URL, p.OwnerToken, "in-process "+p.URL
	}
	base := strings.TrimRight(cfg.URL, "/")
	hc := newHTTPClient(cfg.Agents + cfg.Reviewers + leaderConc + ownerConc + 8)
	if err := waitHealthy(ctx, hc, base, 10*time.Second, nil); err != nil {
		return nil, err
	}
	rid := make([]byte, 3)
	_, _ = rand.Read(rid)
	r := &runner{cfg: cfg, rec: NewRecorder(), mcpURL: base + "/mcp", runID: "lt" + hex.EncodeToString(rid), start: time.Now()}
	r.owner = &ownerClient{hc: hc, base: base, token: cfg.OwnerToken, rec: r.rec}

	leader, workers, reviewers, err := r.setupAgents(ctx)
	if err != nil {
		return nil, err
	}
	mk := func(a agentCreds) *mcpClient {
		return &mcpClient{hc: hc, url: r.mcpURL, agent: a.id, token: a.token, rec: r.rec}
	}
	r.leader = mk(leader)

	sseCtx, sseCancel := context.WithCancel(ctx)
	defer sseCancel()
	fleet := newSSEFleet(base, cfg.OwnerToken, cfg.SSESubs)
	if cfg.SSESubs > 0 {
		fleet.Start(sseCtx, 15*time.Second)
	}

	if err := r.setupBoard(ctx, workers); err != nil {
		sseCancel()
		fleet.Wait()
		return nil, err
	}

	r.workload(ctx, workers, reviewers, mk)

	time.Sleep(200 * time.Millisecond) // let the last events reach subscribers
	sseCancel()
	fleet.Wait()

	elapsed := time.Since(r.start)
	overall, ops, samples := r.rec.Snapshot()
	rep := &Report{Target: target, Agents: cfg.Agents, Reviewers: cfg.Reviewers, Elapsed: elapsed.Seconds(),
		Requests: overall.Count, Errors: overall.Errors, MaxErrorRate: cfg.MaxErrorRate, Overall: overall, Ops: ops,
		SSE: fleet.Report(), ErrorSamples: samples}
	rep.Tasks = TaskCounts{Created: atomic.LoadInt64(&r.tasks.Created), Claimed: atomic.LoadInt64(&r.tasks.Claimed),
		Submitted: atomic.LoadInt64(&r.tasks.Submitted), Approved: atomic.LoadInt64(&r.tasks.Approved), Done: atomic.LoadInt64(&r.tasks.Done)}
	if rep.Requests > 0 {
		rep.ErrorRate = float64(rep.Errors) / float64(rep.Requests)
	}
	if elapsed > 0 {
		rep.Throughput = float64(rep.Requests) / elapsed.Seconds()
	}
	// SSE failures count against the error budget as well.
	sseErr := rep.SSE.ConnectErrors + rep.SSE.StreamErrors
	if sseErr > 0 {
		rep.Errors += sseErr
		rep.Requests += int64(cfg.SSESubs)
		rep.ErrorRate = float64(rep.Errors) / float64(rep.Requests)
	}
	return rep, nil
}

// setupAgents creates the brief and registers 1 leader, N workers and the reviewers via the owner API.
func (r *runner) setupAgents(ctx context.Context) (leader agentCreds, workers, reviewers []agentCreds, err error) {
	var br struct {
		ID string `json:"id"`
	}
	err = r.owner.do(ctx, "rest:create_brief", "POST", "/api/v1/briefs", map[string]any{
		"title": "Load test " + r.runID, "body": "Synthetic brief created by cmd/loadtest.", "repos": []string{repo},
		"max_cost_eur": 0, "max_parallel_agents": r.cfg.Agents, "allowed_providers": []string{},
	}, &br)
	if err != nil {
		return leader, nil, nil, fmt.Errorf("create brief: %w", err)
	}
	r.brief = br.ID

	var l registered
	err = r.owner.do(ctx, "rest:register_agent", "POST", "/api/v1/agents", map[string]any{
		"id": r.runID + "-leader", "role": "leader", "kind": "other", "model": "loadtest", "provider": "loadtest",
	}, &l)
	if err != nil {
		return leader, nil, nil, fmt.Errorf("register leader: %w", err)
	}
	leader = agentCreds{l.Agent.ID, l.Connect.Token}
	// Workers use anthropic, reviewers openai: reviewer provider must differ from the worker's.
	if workers, err = r.bulk(ctx, r.runID+"-w", "worker", "claude-code", r.cfg.Agents); err != nil {
		return leader, nil, nil, err
	}
	if reviewers, err = r.bulk(ctx, r.runID+"-r", "reviewer", "codex", r.cfg.Reviewers); err != nil {
		return leader, nil, nil, err
	}
	return leader, workers, reviewers, nil
}

func (r *runner) bulk(ctx context.Context, prefix, role, kind string, n int) ([]agentCreds, error) {
	var out struct {
		Agents []registered `json:"agents"`
	}
	err := r.owner.do(ctx, "rest:register_bulk", "POST", "/api/v1/agents/bulk", map[string]any{
		"prefix": prefix, "count": n, "role": role, "kind": kind, "model": "loadtest",
	}, &out)
	if err != nil {
		return nil, fmt.Errorf("register %ss: %w", role, err)
	}
	if len(out.Agents) != n {
		return nil, fmt.Errorf("register %ss: got %d agents, want %d", role, len(out.Agents), n)
	}
	cs := make([]agentCreds, n)
	for i, a := range out.Agents {
		if a.Connect.Token == "" {
			return nil, fmt.Errorf("register %ss: agent %s has no token", role, a.Agent.ID)
		}
		cs[i] = agentCreds{a.Agent.ID, a.Connect.Token}
	}
	return cs, nil
}

func (r *runner) taskSpec(i int64) map[string]any {
	return map[string]any{
		"title": fmt.Sprintf("Synthetic task %d", i), "spec": "Load test task; no real work.",
		"acceptance_criteria": []string{"submitted"}, "repo": repo,
	}
}

// setupBoard has the leader propose a plan (N×k tasks), the owner approve it, and the leader assign every task.
func (r *runner) setupBoard(ctx context.Context, workers []agentCreds) error {
	if err := r.leader.Initialize(ctx); err != nil {
		return fmt.Errorf("leader initialize: %w", err)
	}
	if err := r.leader.Call(ctx, "get_instructions", nil, nil); err != nil {
		return fmt.Errorf("leader get_instructions: %w", err)
	}
	total := int64(len(workers) * r.cfg.TasksPerAgent)
	planN := min(total, maxPlanSize)
	specs := make([]map[string]any, planN)
	for i := range specs {
		specs[i] = r.taskSpec(r.seq.Add(1))
	}
	var pr struct {
		Plan struct {
			ID    string    `json:"id"`
			Tasks []taskObj `json:"tasks"`
		} `json:"plan"`
	}
	if err := r.leader.Call(ctx, "propose_plan", map[string]any{"brief_id": r.brief, "note": "load test plan", "tasks": specs}, &pr); err != nil {
		return fmt.Errorf("propose_plan: %w", err)
	}
	if err := r.owner.do(ctx, "rest:approve_plan", "POST", "/api/v1/plans/"+pr.Plan.ID+"/approve", map[string]any{"note": "load test"}, nil); err != nil {
		return fmt.Errorf("approve plan: %w", err)
	}
	ids := make([]string, 0, total)
	for _, t := range pr.Plan.Tasks {
		ids = append(ids, t.ID)
	}
	if int64(len(ids)) != planN {
		return fmt.Errorf("propose_plan returned %d tasks, want %d", len(ids), planN)
	}
	extra := make([]string, total-planN)
	if err := r.parallel(ctx, len(extra), func(i int) error {
		var out struct {
			Task taskObj `json:"task"`
		}
		args := r.taskSpec(r.seq.Add(1))
		args["brief_id"] = r.brief
		if err := r.leader.Call(ctx, "create_task", args, &out); err != nil {
			return fmt.Errorf("create_task: %w", err)
		}
		extra[i] = out.Task.ID
		return nil
	}); err != nil {
		return err
	}
	ids = append(ids, extra...)
	atomic.StoreInt64(&r.tasks.Created, int64(len(ids)))
	return r.parallel(ctx, len(ids), func(i int) error {
		if err := r.leader.Call(ctx, "assign", map[string]any{"task_id": ids[i], "agent_id": workers[i%len(workers)].id}, nil); err != nil {
			return fmt.Errorf("assign %s: %w", ids[i], err)
		}
		return nil
	})
}

// parallel runs fn(0..n-1) on leaderConc goroutines and returns the first error.
func (r *runner) parallel(ctx context.Context, n int, fn func(i int) error) error {
	var (
		next     atomic.Int64
		firstErr error
		once     sync.Once
		wg       sync.WaitGroup
	)
	for g := 0; g < min(leaderConc, n); g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= n || ctx.Err() != nil {
					return
				}
				if err := fn(i); err != nil {
					once.Do(func() { firstErr = err })
					return
				}
			}
		}()
	}
	wg.Wait()
	if firstErr == nil {
		firstErr = ctx.Err()
	}
	return firstErr
}

func (r *runner) think(ctx context.Context) {
	if r.cfg.Think <= 0 {
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(r.cfg.Think):
	}
}

func shard(id string, n int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return int(h.Sum32() % uint32(n)) //nolint:gosec
}

// workload runs workers, the leader's refill pool, reviewers and the owner until Duration has passed,
// then drains outstanding reviews and done marks.
func (r *runner) workload(ctx context.Context, workers, reviewers []agentCreds, mk func(agentCreds) *mcpClient) {
	deadline := time.Now().Add(r.cfg.Duration)
	hard, cancel := context.WithDeadline(ctx, deadline.Add(r.cfg.Drain))
	defer cancel()

	refill := make(chan refillReq)
	reviewQ := make([]chan string, len(reviewers))
	for i := range reviewQ {
		reviewQ[i] = make(chan string, 4096)
	}
	doneQ := make(chan string, 4096)

	var leaderWG, workerWG, reviewerWG, ownerWG sync.WaitGroup
	for g := 0; g < leaderConc; g++ {
		leaderWG.Add(1)
		go func() {
			defer leaderWG.Done()
			r.leaderLoop(hard, refill)
		}()
	}
	for g := 0; g < ownerConc; g++ {
		ownerWG.Add(1)
		go func() {
			defer ownerWG.Done()
			r.ownerLoop(hard, doneQ)
		}()
	}
	for i, rv := range reviewers {
		reviewerWG.Add(1)
		go func() {
			defer reviewerWG.Done()
			r.reviewerLoop(hard, mk(rv), reviewQ[i], doneQ)
		}()
	}
	for _, w := range workers {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			r.workerLoop(hard, deadline, mk(w), refill, reviewQ)
		}()
	}
	workerWG.Wait()
	close(refill)
	leaderWG.Wait()
	for _, q := range reviewQ {
		close(q)
	}
	reviewerWG.Wait()
	close(doneQ)
	ownerWG.Wait()
}

func (r *runner) workerLoop(ctx context.Context, deadline time.Time, c *mcpClient, refill chan<- refillReq, reviewQ []chan string) {
	if err := c.Initialize(ctx); err != nil {
		return
	}
	if err := c.Call(ctx, "get_instructions", nil, nil); err != nil {
		return
	}
	for time.Now().Before(deadline) && ctx.Err() == nil {
		var mine struct {
			Tasks []taskObj `json:"tasks"`
		}
		if err := c.Call(ctx, "list_my_tasks", map[string]any{"status": []string{"assigned", "changes_requested"}}, &mine); err != nil {
			r.think(ctx)
			continue
		}
		if len(mine.Tasks) == 0 {
			reply := make(chan string, 1)
			select {
			case refill <- refillReq{agent: c.agent, reply: reply}:
			case <-ctx.Done():
				return
			}
			if id := <-reply; id == "" {
				r.think(ctx)
			}
			continue
		}
		for _, t := range mine.Tasks {
			if !time.Now().Before(deadline) || ctx.Err() != nil {
				return
			}
			if r.work(ctx, c, t.ID) {
				select {
				case reviewQ[shard(t.ID, len(reviewQ))] <- t.ID:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// work runs claim → heartbeat ×m → update_status → submit; it reports whether the task was submitted.
func (r *runner) work(ctx context.Context, c *mcpClient, id string) bool {
	if err := c.Call(ctx, "claim_task", map[string]any{"task_id": id}, nil); err != nil {
		return false
	}
	atomic.AddInt64(&r.tasks.Claimed, 1)
	for j := 0; j < r.cfg.Heartbeats; j++ {
		r.think(ctx)
		_ = c.Call(ctx, "heartbeat", map[string]any{"task_id": id}, nil)
	}
	r.think(ctx)
	_ = c.Call(ctx, "update_status", map[string]any{"task_id": id, "status": "in_progress", "progress_pct": 80,
		"note": "load test progress", "cost_eur": 0.01}, nil)
	r.think(ctx)
	err := c.Call(ctx, "submit", map[string]any{"task_id": id, "pr_url": "https://example.invalid/loadtest/pull/" + strings.TrimPrefix(id, "T-"),
		"summary": "synthetic change"}, nil)
	if err != nil {
		return false
	}
	atomic.AddInt64(&r.tasks.Submitted, 1)
	return true
}

// leaderLoop creates and assigns a fresh task for every worker that ran out of work.
func (r *runner) leaderLoop(ctx context.Context, refill <-chan refillReq) {
	for req := range refill {
		req.reply <- r.refillOne(ctx, req.agent)
	}
}

func (r *runner) refillOne(ctx context.Context, agent string) string {
	var out struct {
		Task taskObj `json:"task"`
	}
	args := r.taskSpec(r.seq.Add(1))
	args["brief_id"] = r.brief
	if err := r.leader.Call(ctx, "create_task", args, &out); err != nil {
		return ""
	}
	atomic.AddInt64(&r.tasks.Created, 1)
	if err := r.leader.Call(ctx, "assign", map[string]any{"task_id": out.Task.ID, "agent_id": agent}, nil); err != nil {
		return ""
	}
	return out.Task.ID
}

func (r *runner) reviewerLoop(ctx context.Context, c *mcpClient, q <-chan string, doneQ chan<- string) {
	initErr := c.Initialize(ctx)
	if initErr == nil {
		_ = c.Call(ctx, "get_instructions", nil, nil)
	}
	idle := time.NewTicker(2 * time.Second)
	defer idle.Stop()
	for {
		select {
		case id, ok := <-q:
			if !ok {
				return
			}
			if initErr != nil || ctx.Err() != nil {
				continue
			}
			if err := c.Call(ctx, "review", map[string]any{"task_id": id, "verdict": "approve", "comments": "LGTM (load test)"}, nil); err != nil {
				continue
			}
			atomic.AddInt64(&r.tasks.Approved, 1)
			select {
			case doneQ <- id:
			case <-ctx.Done():
			}
		case <-idle.C:
			if initErr == nil && ctx.Err() == nil {
				_ = c.Call(ctx, "list_reviews", nil, nil)
			}
		}
	}
}

func (r *runner) ownerLoop(ctx context.Context, doneQ <-chan string) {
	for id := range doneQ {
		if ctx.Err() != nil {
			continue
		}
		if err := r.owner.do(ctx, "rest:mark_done", "POST", "/api/v1/tasks/"+id+"/done", map[string]any{"note": "merged (load test)"}, nil); err == nil {
			atomic.AddInt64(&r.tasks.Done, 1)
		}
	}
}
