package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/loadtest"
	"github.com/SigmaUno/subspace/internal/model"
)

type Config struct {
	Duration          time.Duration
	Sample            time.Duration
	Warmup            time.Duration
	Seed              uint64
	Workers           int
	Testers           int
	Reviewers         int
	SSE               int
	Briefs            int
	MaxPlan           int
	Lease             time.Duration
	AgentLife         time.Duration
	BriefLife         time.Duration
	RestartEvery      time.Duration
	PauseEvery        time.Duration
	BackupEvery       time.Duration
	VerifyEvery       time.Duration
	MaxHeapSlope      float64
	MaxGoroutineSlope float64
	Features          bool
	Streamers         int
	StreamerLife      time.Duration
	DB                string
	HeapProfile       string
	Dir               string
	Log               *slog.Logger
}

const maxDuration = 24 * time.Hour

func (c *Config) validate() error {
	switch {
	case c.Duration <= 0 || c.Duration > maxDuration:
		return fmt.Errorf("duration must be within (0, %s]", maxDuration)
	case c.Sample <= 0 || c.Sample > c.Duration:
		return errors.New("sample must be within (0, duration]")
	case c.Warmup < 0 || c.Warmup >= c.Duration:
		return errors.New("warmup must be within [0, duration)")
	case c.Workers < 1 || c.Testers < 0 || c.Reviewers < 1 || c.SSE < 0:
		return errors.New("need workers >= 1, reviewers >= 1, testers >= 0, sse >= 0")
	case c.Briefs < 1 || c.MaxPlan < 1 || c.MaxPlan > 500:
		return errors.New("need briefs >= 1 and max-plan within 1..500")
	case c.Lease < time.Second || c.AgentLife < time.Second || c.BriefLife < time.Second:
		return errors.New("lease, agent-life and brief-life must be >= 1s")
	case c.RestartEvery < 0 || c.PauseEvery < 0 || c.BackupEvery < 0 || c.VerifyEvery < 0:
		return errors.New("restart-every, pause-every, backup-every and verify-every must be >= 0")
	case c.Streamers < 0 || (c.Streamers > 0 && c.StreamerLife < time.Second):
		return errors.New("need streamers >= 0 and streamer-life >= 1s")
	}
	return nil
}

type SSEReport struct {
	Connects int64 `json:"connects"`
	Failures int64 `json:"connect_failures"`
	Events   int64 `json:"events"`
	Lagged   int64 `json:"lagged"`
	Kicks    int64 `json:"restart_disconnects"`
}

type Report struct {
	Seed           uint64              `json:"seed"`
	StartedAt      time.Time           `json:"started_at"`
	Duration       float64             `json:"duration_s"`
	Elapsed        float64             `json:"elapsed_s"`
	SampleEvery    float64             `json:"sample_every_s"`
	Warmup         float64             `json:"warmup_s"`
	Samples        int                 `json:"samples"`
	Actions        map[string]int64    `json:"actions"`
	Errors         map[string]int64    `json:"errors_by_code"`
	Latency        loadtest.OpReport   `json:"latency"`
	Ops            []loadtest.OpReport `json:"ops"`
	SSE            SSEReport           `json:"sse"`
	Last           Sample              `json:"last_sample"`
	Peak           Sample              `json:"peak"`
	Trends         []Trend             `json:"trends"`
	Growth         []Trend             `json:"growth"`
	Fakes          map[string]int64    `json:"fake_hits,omitempty"`
	ViolationCount int                 `json:"violation_count"`
	Violations     []Violation         `json:"violations"`
	Aborted        string              `json:"aborted,omitempty"`
	Passed         bool                `json:"passed"`

	series []Sample
}

const maxViolations = 100

type harness struct {
	cfg      Config
	start    time.Time
	proc     *proc
	gate     sync.RWMutex
	tr       *http.Transport
	hc       *http.Client
	owner    *client.Owner
	direct   *client.Owner
	stats    *stats
	roster   *roster
	sse      *sseFleet
	checker  *checker
	fakes    *fakes
	streamMu sync.Mutex
	tasks    atomic.Pointer[[]string]
	seq      atomic.Int64

	mu         sync.Mutex
	actions    map[string]int64
	violations []Violation
	nviolation int
	drills     map[string]bool
}

func (h *harness) count(action string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.actions[action]++
}

func (h *harness) elapsed() float64 { return time.Since(h.start).Seconds() }

func (h *harness) violate(inv, detail string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nviolation++
	if len(h.violations) < maxViolations {
		h.violations = append(h.violations, Violation{At: h.elapsed(), Invariant: inv, Detail: detail})
	}
}

type gatedTransport struct {
	gate *sync.RWMutex
	next http.RoundTripper
}

func (t gatedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.gate.RLock()
	defer t.gate.RUnlock()
	return t.next.RoundTrip(r)
}

func (h *harness) mcp(token string) *client.MCP {
	return client.NewMCP(h.proc.url()+"/mcp", token, client.WithHTTPClient(h.hc), client.WithObserver(h.stats.observe),
		client.WithClientName("subspace-soak"))
}

func Run(ctx context.Context, cfg Config) (*Report, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	dir := cfg.Dir
	if dir == "" {
		d, err := os.MkdirTemp("", "subspace-soak-")
		if err != nil {
			return nil, err
		}
		defer func() { _ = os.RemoveAll(d) }()
		dir = d
	}
	p, err := newProc(dir, cfg.DB, cfg.Lease, cfg.Log)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{MaxIdleConns: 1024, MaxIdleConnsPerHost: 1024, IdleConnTimeout: 30 * time.Second}
	h := &harness{cfg: cfg, proc: p, tr: tr, stats: newStats(), roster: newRoster(), actions: map[string]int64{}}
	h.hc = &http.Client{Timeout: time.Minute, Transport: gatedTransport{gate: &h.gate, next: tr}}
	h.owner = client.NewOwner(p.url(), p.cfg.OwnerToken, client.WithHTTPClient(h.hc), client.WithObserver(h.stats.observe))
	raw := &http.Client{Timeout: time.Minute, Transport: tr}
	h.direct = client.NewOwner(p.url(), p.cfg.OwnerToken, client.WithHTTPClient(raw), client.WithObserver(h.stats.observe))
	h.checker = newChecker(client.NewOwner(p.url(), p.cfg.OwnerToken, client.WithHTTPClient(raw)), cfg.Lease)
	if cfg.Features {
		h.fakes = newFakes(cfg.Seed)
		defer h.fakes.close()
		if err := h.seedPhase(ctx, dir); err != nil {
			return nil, fmt.Errorf("seed: %w", err)
		}
	}
	if err := p.start(); err != nil {
		return nil, fmt.Errorf("start server: %w", err)
	}
	h.start = time.Now()
	h.stats = newStats()
	h.actions = map[string]int64{}

	rep := &Report{Seed: cfg.Seed, StartedAt: h.start.UTC(), Duration: cfg.Duration.Seconds(), SampleEvery: cfg.Sample.Seconds(), Warmup: cfg.Warmup.Seconds()}
	wctx, stopWork := context.WithCancel(ctx)
	h.sse = newSSEFleet(wctx)
	var wg sync.WaitGroup
	if err := h.launch(wctx, &wg); err != nil {
		stopWork()
		return nil, errors.Join(err, p.stop())
	}
	rep.Aborted = h.observe(ctx, rep)
	stopWork()
	h.sse.stop()
	wg.Wait()

	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	if rep.Aborted == "" {
		h.checkOnce(fctx)
		h.verifyChain(fctx)
		var s Sample
		h.sampleInto(&s)
		rep.series = append(rep.series, s)
	}
	if cfg.HeapProfile != "" {
		if err := writeProfiles(cfg.HeapProfile, "end"); err != nil {
			cfg.Log.Error("heap profile", "err", err)
		}
	}
	if err := p.stop(); err != nil {
		h.violate(invRestart, "final stop: "+err.Error())
	}
	tr.CloseIdleConnections()
	h.finish(rep)
	return rep, nil
}

func (h *harness) launch(ctx context.Context, wg *sync.WaitGroup) error {
	cfg := h.cfg
	var n uint64
	rnd := func() *rand.Rand {
		n++
		return rand.New(rand.NewPCG(cfg.Seed, n)) //nolint:gosec // reproducible workload choices
	}
	leaders, err := h.register(ctx, model.RoleLeader, 1)
	if err != nil {
		return fmt.Errorf("register leader: %w", err)
	}
	if h.roster.reviewers, err = h.register(ctx, model.RoleReviewer, cfg.Reviewers); err != nil {
		return fmt.Errorf("register reviewers: %w", err)
	}
	gor := func(fn func(r *rand.Rand)) {
		r := rnd()
		wg.Go(func() { fn(r) })
	}
	lm := h.mcp(leaders[0].token)
	gor(func(r *rand.Rand) { h.leaderLoop(ctx, lm, r) })
	gor(func(r *rand.Rand) { h.ownerLoop(ctx, r) })
	for _, a := range h.roster.reviewers {
		gor(func(r *rand.Rand) { h.reviewerLoop(ctx, a, r) })
	}
	for range cfg.Workers {
		gor(func(r *rand.Rand) { h.slot(ctx, model.RoleWorker, r) })
	}
	for range cfg.Testers {
		gor(func(r *rand.Rand) { h.slot(ctx, model.RoleTester, r) })
	}
	for range cfg.SSE {
		gor(func(r *rand.Rand) { h.subscriber(ctx, r) })
	}
	gor(func(r *rand.Rand) { every(ctx, r, cfg.PauseEvery, func() { h.pauseCycle(ctx, r) }) })
	gor(func(r *rand.Rand) { every(ctx, r, cfg.BackupEvery, func() { h.backup(ctx) }) })
	if h.fakes != nil {
		gor(func(r *rand.Rand) { h.featureLoop(ctx, r) })
		for range cfg.Streamers {
			gor(func(r *rand.Rand) { h.streamSlot(ctx, r) })
		}
	}
	return nil
}

func (h *harness) seedPhase(ctx context.Context, dir string) error {
	env, err := featureEnv(dir, h.fakes)
	if err != nil {
		return err
	}
	if err := applyEnv(env); err != nil {
		return err
	}
	if err := h.proc.start(); err != nil {
		return err
	}
	err = h.seed(ctx)
	h.tr.CloseIdleConnections()
	return errors.Join(err, h.proc.stop())
}

func (h *harness) observe(ctx context.Context, rep *Report) string {
	deadline := time.NewTimer(h.cfg.Duration)
	defer deadline.Stop()
	tick := time.NewTicker(h.cfg.Sample)
	defer tick.Stop()
	r := rand.New(rand.NewPCG(h.cfg.Seed, 0)) //nolint:gosec // reproducible workload choices
	nextRestart := h.jitter(r, h.cfg.RestartEvery)
	nextVerify := h.cfg.VerifyEvery
	var faults int64
	restarted := false
	warm := time.NewTimer(h.cfg.Warmup)
	defer warm.Stop()
	for {
		select {
		case <-ctx.Done():
			return ""
		case <-deadline.C:
			return ""
		case <-warm.C:
			if h.cfg.HeapProfile != "" {
				if err := writeProfiles(h.cfg.HeapProfile, "warm"); err != nil {
					h.cfg.Log.Error("profiles", "err", err)
				}
			}
			continue
		case <-tick.C:
		}
		s := Sample{Restarted: restarted}
		restarted = false
		h.sampleInto(&s)
		rep.series = append(rep.series, s)
		if n, last := h.stats.faults(); n > faults {
			h.violate(invInternal, fmt.Sprintf("%d new server errors, last: %s", n-faults, last))
			faults = n
		}
		if !h.checkOnce(ctx) && ctx.Err() == nil {
			return "invariant snapshot failed"
		}
		at := time.Since(h.start)
		if h.cfg.VerifyEvery > 0 && at >= nextVerify {
			h.verifyChain(ctx)
			nextVerify = at + h.cfg.VerifyEvery
		}
		if h.cfg.RestartEvery > 0 && at >= nextRestart {
			if err := h.restart(); err != nil {
				h.violate(invRestart, err.Error())
				return "restart failed: " + err.Error()
			}
			restarted = true
			nextRestart = at + h.jitter(r, h.cfg.RestartEvery)
		}
	}
}

func (h *harness) jitter(r *rand.Rand, d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return d*3/4 + time.Duration(r.Int64N(int64(d/2)+1))
}

func (h *harness) sampleInto(s *Sample) {
	runtimeSample(s)
	s.T = h.elapsed()
	h.gate.RLock()
	s.WriterQueue = h.proc.writerQueue()
	h.gate.RUnlock()
	s.WAL = h.proc.walBytes()
	s.SSE = h.sse.open.Load()
	if h.fakes != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		h.gate.RLock()
		s.Tables = tableCounts(ctx, h.proc.srv.Store, h.proc.postgres())
		h.gate.RUnlock()
		cancel()
		s.DataBytes = dirBytes(h.proc.dataDir())
		s.RetainBytes = dirBytes(filepath.Join(h.proc.dataDir(), "retention"))
	}
	w := h.stats.rotate()
	s.Requests, s.Errors, s.P99ms = w.Count, w.Errors, w.P99ms
}

func (h *harness) checkOnce(ctx context.Context) bool {
	vs, err := h.checker.check(ctx, &h.gate)
	if err != nil {
		if ctx.Err() == nil {
			h.violate(invSnapshot, err.Error())
		}
		return false
	}
	for _, v := range vs {
		h.violate(v.Invariant, v.Detail)
	}
	return true
}

func (h *harness) verifyChain(ctx context.Context) {
	v, err := h.checker.verifyChain(ctx)
	switch {
	case err != nil && ctx.Err() == nil:
		h.violate(invChain, "verify: "+err.Error())
	case v != nil:
		h.violate(v.Invariant, v.Detail)
	}
}

func (h *harness) restart() error {
	h.gate.Lock()
	defer h.gate.Unlock()
	h.sse.kick()
	if err := h.proc.stop(); err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	if err := h.proc.start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	h.tr.CloseIdleConnections()
	h.count("restart")
	return nil
}

func (h *harness) finish(rep *Report) {
	rep.Elapsed = h.elapsed()
	rep.Samples = len(rep.series)
	h.mu.Lock()
	rep.Actions = h.actions
	rep.Violations = append([]Violation{}, h.violations...)
	rep.ViolationCount = h.nviolation
	h.mu.Unlock()
	rep.Errors = h.stats.errorCodes()
	rep.Latency, rep.Ops, _ = h.stats.total.Snapshot()
	rep.SSE = SSEReport{Connects: h.sse.connects.Load(), Failures: h.sse.failures.Load(), Events: h.sse.events.Load(),
		Lagged: h.sse.lagged.Load(), Kicks: h.sse.kicks.Load()}
	for _, s := range rep.series {
		rep.Peak = Sample{T: max(rep.Peak.T, s.T), Goroutines: max(rep.Peak.Goroutines, s.Goroutines), HeapLive: max(rep.Peak.HeapLive, s.HeapLive),
			HeapInuse: max(rep.Peak.HeapInuse, s.HeapInuse), FDs: max(rep.Peak.FDs, s.FDs), WAL: max(rep.Peak.WAL, s.WAL),
			WriterQueue: max(rep.Peak.WriterQueue, s.WriterQueue), Requests: max(rep.Peak.Requests, s.Requests),
			Errors: max(rep.Peak.Errors, s.Errors), P99ms: max(rep.Peak.P99ms, s.P99ms), SSE: max(rep.Peak.SSE, s.SSE)}
	}
	if n := len(rep.series); n > 0 {
		rep.Last = rep.series[n-1]
	}
	series := rep.series
	if n := len(series); n > 0 && rep.Aborted == "" {
		series = series[:n-1]
	}
	rep.Trends = []Trend{
		trend("heap_live", "MiB/min", series, h.cfg.Warmup, h.cfg.MaxHeapSlope, func(s Sample) float64 { return float64(s.HeapLive) / (1 << 20) }),
		trend("goroutines", "goroutines/min", series, h.cfg.Warmup, h.cfg.MaxGoroutineSlope, func(s Sample) float64 { return float64(s.Goroutines) }),
	}
	rep.Growth = growth(series, h.cfg.Warmup)
	if h.fakes != nil {
		rep.Fakes = h.fakes.counts()
	}
	rep.Passed = rep.Aborted == "" && rep.ViolationCount == 0
	for _, t := range rep.Trends {
		rep.Passed = rep.Passed && !t.Exceeded
	}
}

func writeProfiles(path, stage string) error {
	runtime.GC()
	for _, p := range []struct{ name, file string }{{"heap", path + "." + stage}, {"goroutine", path + ".goroutines." + stage}} {
		f, err := create(p.file)
		if err != nil {
			return err
		}
		debug := 0
		if p.name == "goroutine" {
			debug = 1
		}
		if err := pprof.Lookup(p.name).WriteTo(f, debug); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}
