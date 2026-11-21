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
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/model"
)

// Config configures a load test run.
type Config struct {
	Agents        int           // simulated workers
	Duration      time.Duration // workload phase length
	Ramp          time.Duration // workers start evenly over this part of Duration (0 = all at once)
	URL           string        // external server; "" = start one in-process
	OwnerToken    string        // owner token of the external server
	SSESubs       int           // SSE subscribers
	MaxErrorRate  float64       // fraction 0..1 of failed requests tolerated
	TasksPerAgent int           // k: tasks created per worker up front
	Heartbeats    int           // m: heartbeats per task
	Reviewers     int           // 0 = auto (1 per 25 workers)
	Think         time.Duration // pause between an agent's calls
	Drain         time.Duration // grace period for reviews/done after Duration
	Scenario      string
	Briefs        int
	Tasks         int
	Messages      int
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
	c.scenarioDefaults()
}

// Validate checks the configuration.
func (c *Config) Validate() error {
	switch {
	case c.Agents < 1 || c.Agents > 1000:
		return errors.New("agents must be 1..1000")
	case c.Duration <= 0:
		return errors.New("duration must be > 0")
	case c.Ramp < 0 || c.Ramp > c.Duration:
		return errors.New("ramp must be within 0..duration")
	case c.SSESubs < 0 || c.SSESubs > 1000:
		return errors.New("sse-subs must be 0..1000")
	case c.MaxErrorRate < 0 || c.MaxErrorRate > 1:
		return errors.New("max-error-rate must be within 0..1")
	case c.URL != "" && c.OwnerToken == "":
		return errors.New("owner-token is required with url")
	case c.Reviewers > 1000:
		return errors.New("reviewers must be <= 1000")
	}
	return c.validateScenario()
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
	Scenario     string      `json:"scenario,omitempty"`
	Invariants   []Invariant `json:"invariants,omitempty"`
	Target       string      `json:"target"`
	Agents       int         `json:"agents"`
	Reviewers    int         `json:"reviewers"`
	Elapsed      float64     `json:"elapsed_s"`
	Requests     int64       `json:"requests"`
	Errors       int64       `json:"errors"`
	ErrorRate    float64     `json:"error_rate"`
	MaxErrorRate float64     `json:"max_error_rate"`
	Throughput   float64     `json:"throughput_rps"`
	Overall      OpReport    `json:"overall"`
	Ops          []OpReport  `json:"ops"`
	Tasks        TaskCounts  `json:"tasks"`
	SSE          SSEReport   `json:"sse"`
	Heap         HeapReport  `json:"heap"`
	ErrorSamples []string    `json:"error_samples,omitempty"`
}

// Breached reports whether the error rate exceeds the configured maximum (or nothing was measured).
func (r *Report) Breached() bool {
	return r.Requests == 0 || r.ErrorRate > r.MaxErrorRate || r.InvariantsFailed() > 0
}

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
	_, _ = fmt.Fprintf(w, "heap (live, process): after ramp %.1f MB, end %.1f MB, max %.1f MB\n", r.Heap.StartMB, r.Heap.EndMB, r.Heap.MaxMB)
	if r.SSE.Reconnects > 0 {
		_, _ = fmt.Fprintf(w, "SSE reconnects: %d, resyncs %d, events at or below the hello seq %d, duplicates %d\n", r.SSE.Reconnects, r.SSE.Resyncs, r.SSE.Overlap, r.SSE.Duplicates)
	}
	for _, e := range r.ErrorSamples {
		_, _ = fmt.Fprintf(w, "error sample: %s\n", e)
	}
	if len(r.Invariants) > 0 {
		_, _ = fmt.Fprintf(w, "\nscenario %s invariants:\n", r.Scenario)
		for _, inv := range r.Invariants {
			_, _ = fmt.Fprintf(w, "  %s\n", inv)
		}
	}
	if r.Breached() {
		_, _ = fmt.Fprintln(w, "RESULT: FAIL (error rate above maximum, failed invariant or no requests)")
	} else {
		_, _ = fmt.Fprintln(w, "RESULT: OK")
	}
}

type agentCreds struct{ id, token string }

type refillReq struct {
	agent string
	reply chan string
}

type runner struct {
	cfg    Config
	rec    *Recorder
	owner  *client.Owner
	verify *client.Owner
	mcpURL string
	runID  string
	brief  string
	leader *client.MCP
	tasks  TaskCounts
	seq    atomic.Int64
	start  time.Time
	obs    client.Option
	base   string
	hc     *http.Client
	proc   *InProcess
	soft   client.Option
	retry  *retrier
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
	var proc *InProcess
	if cfg.URL == "" {
		p, err := StartInProcess(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer p.Stop()
		proc = p
		cfg.URL, cfg.OwnerToken, target = p.URL, p.OwnerToken, "in-process "+p.URL
	}
	base := strings.TrimRight(cfg.URL, "/")
	hc := newHTTPClient(cfg.Agents + cfg.Reviewers + leaderConc + ownerConc + 8)
	if err := waitHealthy(ctx, hc, base, 10*time.Second, nil); err != nil {
		return nil, err
	}
	rid := make([]byte, 3)
	_, _ = rand.Read(rid)
	r := &runner{cfg: cfg, rec: NewRecorder(), mcpURL: base + "/mcp", runID: "lt" + hex.EncodeToString(rid), start: time.Now(),
		base: base, hc: hc, proc: proc}
	r.obs = client.WithObserver(func(op string, d time.Duration, err error) { r.record(op, d, err, false) })
	r.soft = client.WithObserver(func(op string, d time.Duration, err error) { r.record(op, d, err, true) })
	r.owner = client.NewOwner(base, cfg.OwnerToken, client.WithHTTPClient(hc), r.obs)
	r.verify = client.NewOwner(base, cfg.OwnerToken, client.WithHTTPClient(hc))

	heapCtx, heapCancel := context.WithCancel(ctx)
	defer heapCancel()
	heap := sampleHeap(heapCtx, time.Now().Add(cfg.Ramp))

	var sse SSEReport
	var invs []Invariant
	var err error
	if run := scenarios[cfg.Scenario]; run != nil {
		invs, sse, err = run(ctx, r)
	} else {
		sse, err = r.steady(ctx)
	}
	if err != nil {
		return nil, err
	}

	elapsed := time.Since(r.start)
	overall, ops, samples := r.rec.Snapshot()
	rep := &Report{Scenario: cfg.Scenario, Invariants: invs, Target: target, Agents: cfg.Agents, Reviewers: cfg.Reviewers,
		Elapsed: elapsed.Seconds(), Requests: overall.Count, Errors: overall.Errors, MaxErrorRate: cfg.MaxErrorRate,
		Overall: overall, Ops: ops, SSE: sse, Heap: heap(), ErrorSamples: samples}
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
		rep.Requests += int64(rep.SSE.Subscribers)
		rep.ErrorRate = float64(rep.Errors) / float64(rep.Requests)
	}
	return rep, nil
}

func (r *runner) record(op string, d time.Duration, err error, soft bool) {
	if soft && rejected(err) || errors.Is(err, context.Canceled) || r.retry.suppress(err) {
		err = nil
	}
	r.rec.Observe(op, d, err)
}

func (r *runner) mk(a agentCreds) *client.MCP {
	return client.NewMCP(r.mcpURL, a.token, client.WithHTTPClient(r.hc), r.obs, client.WithClientName("subspace-loadtest"))
}

func (r *runner) mkSoft(a agentCreds) *client.MCP {
	return client.NewMCP(r.mcpURL, a.token, client.WithHTTPClient(r.hc), r.soft, client.WithClientName("subspace-loadtest"))
}

func (r *runner) steady(ctx context.Context) (SSEReport, error) {
	if err := r.newBrief(ctx); err != nil {
		return SSEReport{}, err
	}
	leader, workers, reviewers, err := r.setupAgents(ctx)
	if err != nil {
		return SSEReport{}, err
	}
	r.leader = r.mk(leader)

	sseCtx, sseCancel := context.WithCancel(ctx)
	defer sseCancel()
	var churn time.Duration
	if r.cfg.Scenario == ScenarioReconnectStorm {
		churn = stormChurn
	}
	fleet := newSSEFleet(r.base, r.cfg.OwnerToken, r.cfg.SSESubs, churn)
	if r.cfg.SSESubs > 0 {
		fleet.Start(sseCtx, 15*time.Second)
	}

	if err := r.setupBoard(ctx, workers); err != nil {
		sseCancel()
		fleet.Wait()
		return SSEReport{}, err
	}

	r.workload(ctx, workers, reviewers)

	time.Sleep(200 * time.Millisecond) // let the last events reach subscribers
	sseCancel()
	fleet.Wait()
	return fleet.Report(), nil
}

func (r *runner) newBrief(ctx context.Context) error {
	id, err := r.createBrief(ctx, "Load test "+r.runID)
	r.brief = id
	return err
}

func (r *runner) createBrief(ctx context.Context, title string) (string, error) {
	br, err := r.owner.CreateBrief(ctx, board.BriefInput{
		Title: title, Body: "Synthetic brief created by cmd/loadtest.", Repos: []string{repo},
		MaxParallel: r.cfg.Agents, AllowedProviders: []string{},
	})
	if err != nil {
		return "", fmt.Errorf("create brief: %w", err)
	}
	return br.ID, nil
}

// setupAgents registers 1 leader, N workers and the reviewers via the owner API.
func (r *runner) setupAgents(ctx context.Context) (leader agentCreds, workers, reviewers []agentCreds, err error) {
	l, err := r.owner.RegisterAgent(ctx, board.AgentInput{
		ID: r.runID + "-leader", Role: model.RoleLeader, Kind: model.KindOther, Model: "loadtest", Provider: "loadtest",
	})
	if err != nil {
		return leader, nil, nil, fmt.Errorf("register leader: %w", err)
	}
	leader = agentCreds{l.Agent.ID, l.Connect.Token}
	// Workers use anthropic, reviewers openai: reviewer provider must differ from the worker's.
	if workers, err = r.bulk(ctx, r.runID+"-w", model.RoleWorker, model.KindClaudeCode, r.cfg.Agents); err != nil {
		return leader, nil, nil, err
	}
	if reviewers, err = r.bulk(ctx, r.runID+"-r", model.RoleReviewer, model.KindCodex, r.cfg.Reviewers); err != nil {
		return leader, nil, nil, err
	}
	return leader, workers, reviewers, nil
}

func (r *runner) bulk(ctx context.Context, prefix, role, kind string, n int) ([]agentCreds, error) {
	out, err := r.owner.RegisterBulk(ctx, board.BulkInput{Prefix: prefix, Count: n, Role: role, Kind: kind, Model: "loadtest"})
	if err != nil {
		return nil, fmt.Errorf("register %ss: %w", role, err)
	}
	if len(out) != n {
		return nil, fmt.Errorf("register %ss: got %d agents, want %d", role, len(out), n)
	}
	cs := make([]agentCreds, n)
	for i, a := range out {
		if a.Connect.Token == "" {
			return nil, fmt.Errorf("register %ss: agent %s has no token", role, a.Agent.ID)
		}
		cs[i] = agentCreds{a.Agent.ID, a.Connect.Token}
	}
	return cs, nil
}

func (r *runner) taskSpec(i int64) board.TaskInput {
	return board.TaskInput{
		BriefID: r.brief, Title: fmt.Sprintf("Synthetic task %d", i), Spec: "Load test task; no real work.",
		AcceptanceCriteria: []string{"submitted"}, Repo: repo,
	}
}

// setupBoard has the leader propose a plan (N×k tasks), the owner approve it, and the leader assign every task.
func (r *runner) setupBoard(ctx context.Context, workers []agentCreds) error {
	if err := r.leader.Initialize(ctx); err != nil {
		return fmt.Errorf("leader initialize: %w", err)
	}
	if _, err := r.leader.GetInstructions(ctx); err != nil {
		return fmt.Errorf("leader get_instructions: %w", err)
	}
	total := int64(len(workers) * r.cfg.TasksPerAgent)
	planN := min(total, maxPlanSize)
	specs := make([]board.TaskInput, planN)
	for i := range specs {
		specs[i] = r.taskSpec(r.seq.Add(1))
	}
	plan, err := r.leader.ProposePlan(ctx, board.PlanInput{BriefID: r.brief, Note: "load test plan", Tasks: specs})
	if err != nil {
		return fmt.Errorf("propose_plan: %w", err)
	}
	if _, err := r.owner.ApprovePlan(ctx, plan.ID, "load test"); err != nil {
		return fmt.Errorf("approve plan: %w", err)
	}
	ids := make([]string, 0, total)
	for _, t := range plan.Tasks {
		ids = append(ids, t.ID)
	}
	if int64(len(ids)) != planN {
		return fmt.Errorf("propose_plan returned %d tasks, want %d", len(ids), planN)
	}
	extra := make([]string, total-planN)
	if err := r.parallel(ctx, len(extra), func(i int) error {
		t, err := r.leader.CreateTask(ctx, r.taskSpec(r.seq.Add(1)))
		if err != nil {
			return fmt.Errorf("create_task: %w", err)
		}
		extra[i] = t.ID
		return nil
	}); err != nil {
		return err
	}
	ids = append(ids, extra...)
	atomic.StoreInt64(&r.tasks.Created, int64(len(ids)))
	return r.parallel(ctx, len(ids), func(i int) error {
		if _, err := r.leader.Assign(ctx, ids[i], workers[i%len(workers)].id, ""); err != nil {
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
func (r *runner) workload(ctx context.Context, workers, reviewers []agentCreds) {
	begin := time.Now()
	deadline := begin.Add(r.cfg.Duration)
	hard, cancel := context.WithDeadline(ctx, deadline.Add(r.cfg.Drain))
	defer cancel()

	refill := make(chan refillReq)
	p := r.startPipeline(hard, reviewers)

	var leaderWG, workerWG sync.WaitGroup
	for g := 0; g < leaderConc; g++ {
		leaderWG.Add(1)
		go func() {
			defer leaderWG.Done()
			r.leaderLoop(hard, refill)
		}()
	}
	for i, w := range workers {
		workerWG.Add(1)
		joinAt := begin.Add(r.cfg.Ramp * time.Duration(i) / time.Duration(len(workers)))
		go func() {
			defer workerWG.Done()
			select {
			case <-time.After(time.Until(joinAt)):
			case <-hard.Done():
				return
			}
			r.workerLoop(hard, deadline, w.id, r.mk(w), refill, p)
		}()
	}
	workerWG.Wait()
	close(refill)
	leaderWG.Wait()
	p.stop()
}

type pipeline struct {
	reviewQ   []chan string
	doneQ     chan string
	reviewers sync.WaitGroup
	owners    sync.WaitGroup
}

func (r *runner) startPipeline(ctx context.Context, reviewers []agentCreds) *pipeline {
	p := &pipeline{doneQ: make(chan string, 4096)}
	for g := 0; g < ownerConc; g++ {
		p.owners.Add(1)
		go func() {
			defer p.owners.Done()
			r.ownerLoop(ctx, p.doneQ)
		}()
	}
	for _, rv := range reviewers {
		q := make(chan string, 4096)
		p.reviewQ = append(p.reviewQ, q)
		p.reviewers.Add(1)
		go func() {
			defer p.reviewers.Done()
			r.reviewerLoop(ctx, r.mk(rv), q, p.doneQ)
		}()
	}
	return p
}

func (p *pipeline) submit(ctx context.Context, id string) bool {
	select {
	case p.reviewQ[shard(id, len(p.reviewQ))] <- id:
		return true
	case <-ctx.Done():
		return false
	}
}

func (p *pipeline) stop() {
	for _, q := range p.reviewQ {
		close(q)
	}
	p.reviewers.Wait()
	close(p.doneQ)
	p.owners.Wait()
}

func (r *runner) workerLoop(ctx context.Context, deadline time.Time, agent string, c *client.MCP, refill chan<- refillReq, p *pipeline) {
	if err := r.do(ctx, func() error { return c.Initialize(ctx) }, nil); err != nil {
		return
	}
	if _, err := c.GetInstructions(ctx); err != nil {
		return
	}
	for time.Now().Before(deadline) && ctx.Err() == nil {
		mine, err := c.ListMyTasks(ctx, model.StatusAssigned, model.StatusChangesRequested)
		if err != nil {
			r.think(ctx)
			continue
		}
		if len(mine) == 0 {
			reply := make(chan string, 1)
			select {
			case refill <- refillReq{agent: agent, reply: reply}:
			case <-ctx.Done():
				return
			}
			if id := <-reply; id == "" {
				r.think(ctx)
			}
			continue
		}
		for _, t := range mine {
			if !time.Now().Before(deadline) || ctx.Err() != nil {
				return
			}
			if r.work(ctx, c, t.ID) && !p.submit(ctx, t.ID) {
				return
			}
		}
	}
}

// work runs claim → heartbeat ×m → update_status → submit; it reports whether the task was submitted.
func (r *runner) work(ctx context.Context, c *client.MCP, id string) bool {
	return r.claim(ctx, c, id) && r.complete(ctx, c, id)
}

func (r *runner) claim(ctx context.Context, c *client.MCP, id string) bool {
	err := r.do(ctx, func() error { _, err := c.ClaimTask(ctx, id); return err }, r.moved(ctx, id, model.StatusAssigned, model.StatusChangesRequested))
	if err != nil {
		return false
	}
	atomic.AddInt64(&r.tasks.Claimed, 1)
	return true
}

func (r *runner) complete(ctx context.Context, c *client.MCP, id string) bool {
	for j := 0; j < r.cfg.Heartbeats; j++ {
		r.think(ctx)
		_, _ = c.Heartbeat(ctx, id)
	}
	r.think(ctx)
	pct, cost := 80, 0.01
	_, _ = c.UpdateStatus(ctx, board.StatusInput{TaskID: id, Status: model.StatusInProgress, ProgressPct: &pct,
		Note: "load test progress", CostEUR: &cost})
	r.think(ctx)
	err := r.do(ctx, func() error {
		_, err := c.Submit(ctx, board.SubmitInput{TaskID: id, PRURL: "https://example.invalid/loadtest/pull/" + strings.TrimPrefix(id, "T-"),
			Summary: "synthetic change"})
		return err
	}, r.moved(ctx, id, model.StatusInProgress))
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
	t, err := r.leader.CreateTask(ctx, r.taskSpec(r.seq.Add(1)))
	if err != nil {
		return ""
	}
	atomic.AddInt64(&r.tasks.Created, 1)
	if _, err := r.leader.Assign(ctx, t.ID, agent, ""); err != nil {
		return ""
	}
	return t.ID
}

func (r *runner) reviewerLoop(ctx context.Context, c *client.MCP, q <-chan string, doneQ chan<- string) {
	initErr := c.Initialize(ctx)
	if initErr == nil {
		_, _ = c.GetInstructions(ctx)
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
			err := r.do(ctx, func() error { _, err := c.Review(ctx, id, "approve", "LGTM (load test)"); return err },
				r.moved(ctx, id, model.StatusSubmitted, model.StatusInReview))
			if err != nil {
				continue
			}
			atomic.AddInt64(&r.tasks.Approved, 1)
			select {
			case doneQ <- id:
			case <-ctx.Done():
			}
		case <-idle.C:
			if initErr == nil && ctx.Err() == nil {
				_, _ = c.ListReviews(ctx)
			}
		}
	}
}

func (r *runner) ownerLoop(ctx context.Context, doneQ <-chan string) {
	for id := range doneQ {
		if ctx.Err() != nil {
			continue
		}
		err := r.do(ctx, func() error { _, err := r.owner.MarkDone(ctx, id, "merged (load test)"); return err },
			func() bool {
				t, err := r.owner.GetTask(ctx, id)
				return err == nil && t.Status == model.StatusDone
			})
		if err == nil {
			atomic.AddInt64(&r.tasks.Done, 1)
		}
	}
}
