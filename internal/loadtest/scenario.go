package loadtest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	ScenarioSteady         = "steady"
	ScenarioContention     = "contention"
	ScenarioBigPlan        = "big-plan"
	ScenarioManyBriefs     = "many-briefs"
	ScenarioThreadHeavy    = "thread-heavy"
	ScenarioReconnectStorm = "reconnect-storm"
	ScenarioRestart        = "restart"
)

var Scenarios = []string{ScenarioSteady, ScenarioContention, ScenarioBigPlan, ScenarioManyBriefs, ScenarioThreadHeavy, ScenarioReconnectStorm, ScenarioRestart}

const (
	contentionWaves = 10
	bigPlanLayers   = 5
	stormChurn      = 400 * time.Millisecond
	restartGrace    = 300 * time.Millisecond
	pageSize        = 1000
)

type scenarioFunc func(ctx context.Context, r *runner) ([]Invariant, SSEReport, error)

var scenarios = map[string]scenarioFunc{
	ScenarioContention:     contention,
	ScenarioBigPlan:        bigPlan,
	ScenarioManyBriefs:     manyBriefs,
	ScenarioThreadHeavy:    threadHeavy,
	ScenarioReconnectStorm: reconnectStorm,
	ScenarioRestart:        restart,
}

type Invariant struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

func (i Invariant) String() string {
	status := "ok  "
	if !i.OK {
		status = "FAIL"
	}
	if i.Detail == "" {
		return status + " " + i.Name
	}
	return status + " " + i.Name + ": " + i.Detail
}

func check(name string, ok bool, format string, args ...any) Invariant {
	return Invariant{Name: name, OK: ok, Detail: fmt.Sprintf(format, args...)}
}

func (r *Report) InvariantsFailed() int {
	n := 0
	for _, i := range r.Invariants {
		if !i.OK {
			n++
		}
	}
	return n
}

func (c *Config) scenarioDefaults() {
	if c.Scenario == ScenarioSteady {
		c.Scenario = ""
	}
	set := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	switch c.Scenario {
	case ScenarioContention:
		set(&c.Tasks, 5)
	case ScenarioBigPlan:
		set(&c.Tasks, maxPlanSize)
	case ScenarioManyBriefs:
		set(&c.Briefs, 100)
		set(&c.Tasks, 20)
	case ScenarioThreadHeavy:
		set(&c.Messages, 500)
	}
}

func (c *Config) validateScenario() error {
	switch c.Scenario {
	case "":
	case ScenarioContention, ScenarioBigPlan:
		if c.Tasks > maxPlanSize {
			return fmt.Errorf("tasks must be <= %d", maxPlanSize)
		}
	case ScenarioManyBriefs:
		if c.Briefs > 1000 || c.Tasks > maxPlanSize {
			return fmt.Errorf("briefs must be <= 1000 and tasks <= %d", maxPlanSize)
		}
	case ScenarioThreadHeavy:
		if c.Messages > 10000 {
			return errors.New("messages must be <= 10000")
		}
	case ScenarioReconnectStorm:
		if c.SSESubs < 1 {
			return errors.New("reconnect-storm needs sse-subs >= 1")
		}
	case ScenarioRestart:
		if c.URL != "" {
			return errors.New("restart needs the in-process server (no url)")
		}
	default:
		return fmt.Errorf("unknown scenario %q (one of %s)", c.Scenario, strings.Join(Scenarios, ", "))
	}
	return nil
}

func rejected(err error) bool {
	return client.IsCode(err, client.CodeConflict) || client.IsCode(err, client.CodeNotAssigned)
}

func transient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var ce *client.Error
	if errors.As(err, &ce) {
		return ce.Status >= http.StatusInternalServerError
	}
	return true
}

type retrier struct {
	down    atomic.Bool
	retries atomic.Int64
}

func (t *retrier) suppress(err error) bool {
	return t != nil && t.down.Load() && transient(err)
}

func (t *retrier) do(ctx context.Context, fn func() error, applied func() bool) error {
	retried := false
	for {
		err := fn()
		switch {
		case err == nil:
			return nil
		case transient(err) && ctx.Err() == nil:
			retried = true
			t.retries.Add(1)
			sleep(ctx, 100*time.Millisecond)
		case retried && rejected(err) && applied != nil && applied():
			return nil
		default:
			return err
		}
	}
}

func (r *runner) do(ctx context.Context, fn func() error, applied func() bool) error {
	if r.retry == nil {
		return fn()
	}
	return r.retry.do(ctx, fn, applied)
}

func (r *runner) moved(ctx context.Context, id string, from ...string) func() bool {
	return func() bool {
		t, err := r.verify.GetTask(ctx, id)
		return err == nil && !slices.Contains(from, t.Status)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (r *runner) idle(ctx context.Context) {
	sleep(ctx, max(4*r.cfg.Think, 20*time.Millisecond))
}

func (r *runner) specFor(brief string, i int64) board.TaskInput {
	in := r.taskSpec(i)
	in.BriefID = brief
	return in
}

func (r *runner) plan(ctx context.Context, brief string, specs []board.TaskInput) ([]string, error) {
	plan, err := r.leader.ProposePlan(ctx, board.PlanInput{BriefID: brief, Note: "load test plan", Tasks: specs})
	if err != nil {
		return nil, fmt.Errorf("propose_plan: %w", err)
	}
	if len(plan.Tasks) != len(specs) {
		return nil, fmt.Errorf("propose_plan returned %d tasks, want %d", len(plan.Tasks), len(specs))
	}
	if _, err := r.owner.ApprovePlan(ctx, plan.ID, "load test"); err != nil {
		return nil, fmt.Errorf("approve plan: %w", err)
	}
	ids := make([]string, len(plan.Tasks))
	for i, t := range plan.Tasks {
		ids[i] = t.ID
	}
	return ids, nil
}

func (r *runner) assignAll(ctx context.Context, ids []string, workers []agentCreds, offset int) error {
	return r.parallel(ctx, len(ids), func(i int) error {
		if _, err := r.leader.Assign(ctx, ids[i], workers[(offset+i)%len(workers)].id, ""); err != nil {
			return fmt.Errorf("assign %s: %w", ids[i], err)
		}
		return nil
	})
}

func (r *runner) setupTeam(ctx context.Context, defaultBrief bool) ([]agentCreds, []agentCreds, error) {
	if defaultBrief {
		if err := r.newBrief(ctx); err != nil {
			return nil, nil, err
		}
	}
	leader, workers, reviewers, err := r.setupAgents(ctx)
	if err != nil {
		return nil, nil, err
	}
	r.leader = r.mk(leader)
	if err := r.leader.Initialize(ctx); err != nil {
		return nil, nil, fmt.Errorf("leader initialize: %w", err)
	}
	return workers, reviewers, nil
}

func (r *runner) awaitDone(ctx context.Context, total int64) bool {
	for atomic.LoadInt64(&r.tasks.Done) < total {
		if ctx.Err() != nil {
			return false
		}
		sleep(ctx, 20*time.Millisecond)
	}
	return true
}

func (r *runner) drain(ctx context.Context, workers, reviewers []agentCreds, total int64, during func(ctx context.Context)) bool {
	sctx, cancel := context.WithTimeout(ctx, r.cfg.Duration)
	defer cancel()
	p := r.startPipeline(sctx, reviewers)
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.awaitDone(sctx, total)
		cancel()
	}()
	if during != nil {
		go during(sctx)
	}
	var wg sync.WaitGroup
	for _, w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.drainWorker(sctx, r.mkSoft(w), p)
		}()
	}
	wg.Wait()
	<-done
	p.stop()
	return atomic.LoadInt64(&r.tasks.Done) >= total
}

func (r *runner) drainWorker(ctx context.Context, c *client.MCP, p *pipeline) {
	if err := r.do(ctx, func() error { return c.Initialize(ctx) }, nil); err != nil {
		return
	}
	for ctx.Err() == nil {
		mine, err := c.ListMyTasks(ctx, model.StatusAssigned, model.StatusChangesRequested)
		progressed := false
		for _, t := range mine {
			if ctx.Err() != nil {
				return
			}
			if r.claim(ctx, c, t.ID) && r.complete(ctx, c, t.ID) {
				progressed = true
				if !p.submit(ctx, t.ID) {
					return
				}
			}
		}
		if err != nil || !progressed {
			r.idle(ctx)
		}
	}
}

func (r *runner) tasksOf(ctx context.Context, briefs ...string) ([]*model.Task, error) {
	var out []*model.Task
	for _, b := range briefs {
		ts, err := r.verify.ListTasks(ctx, board.TaskQuery{BriefID: b, Limit: pageSize})
		if err != nil {
			return nil, err
		}
		out = append(out, ts...)
	}
	return out, nil
}

func countDone(ts []*model.Task) int {
	n := 0
	for _, t := range ts {
		if t.Status == model.StatusDone {
			n++
		}
	}
	return n
}

func (r *runner) audit(ctx context.Context, tool string) (map[string][]*model.AuditEvent, error) {
	out := map[string][]*model.AuditEvent{}
	var since int64
	for {
		evs, err := r.verify.Audit(ctx, store.AuditFilter{Tool: tool, SinceID: since, Limit: pageSize})
		if err != nil {
			return nil, err
		}
		for _, e := range evs {
			since = e.ID
			if e.TaskID != nil {
				out[*e.TaskID] = append(out[*e.TaskID], e)
			}
		}
		if len(evs) < pageSize {
			return out, nil
		}
	}
}

func (r *runner) onePerTask(ctx context.Context, tool string, ids []string) (bad int, err error) {
	evs, err := r.audit(ctx, tool)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if len(evs[id]) != 1 {
			bad++
		}
	}
	return bad, nil
}

func (r *runner) allDone(ctx context.Context, want int, briefs ...string) (Invariant, error) {
	ts, err := r.tasksOf(ctx, briefs...)
	if err != nil {
		return Invariant{}, err
	}
	done := countDone(ts)
	return check("all tasks done", done == want && len(ts) == want, "%d of %d done, %d on the server", done, want, len(ts)), nil
}
