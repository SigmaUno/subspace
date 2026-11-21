package schedule

import (
	"context"
	"errors"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	DefaultInterval = 30 * time.Second
	DefaultGrace    = 5 * time.Minute
	maxLookback     = 31 * 24 * time.Hour
	activeScan      = 500
	maxDetail       = 500
)

var system = model.Actor{ID: "schedule", Role: "system"}

type Options struct {
	Store    store.Store
	Exec     Executor
	Now      func() time.Time
	Interval time.Duration
	Grace    time.Duration
	Log      *slog.Logger
}

type Runner struct {
	st       store.Store
	exec     Executor
	now      func() time.Time
	interval time.Duration
	grace    time.Duration
	log      *slog.Logger
}

func NewRunner(o Options) *Runner {
	r := &Runner{st: o.Store, exec: o.Exec, now: o.Now, interval: o.Interval, grace: o.Grace, log: o.Log}
	if r.now == nil {
		r.now = func() time.Time { return time.Now().UTC() }
	}
	if r.interval <= 0 {
		r.interval = DefaultInterval
	}
	if r.grace <= 0 {
		r.grace = DefaultGrace
	}
	if r.log == nil {
		r.log = slog.Default()
	}
	return r
}

func (r *Runner) Run(ctx context.Context) {
	if err := r.Recover(ctx); err != nil {
		r.log.Error("schedule recovery failed", "err", err)
	}
	tick := time.NewTicker(r.interval)
	defer tick.Stop()
	for {
		if err := r.Tick(ctx); err != nil && ctx.Err() == nil {
			r.log.Error("schedule tick failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (r *Runner) Recover(ctx context.Context) error {
	return r.st.Atomic(ctx, store.WithSQL(func(_ store.Tx, q store.SQL) error {
		n, err := interrupt(q, r.now().UTC())
		if n > 0 {
			r.log.Warn("schedule runs interrupted by a restart", "runs", n)
		}
		return err
	}))
}

func (r *Runner) Tick(ctx context.Context) error {
	var all []*Schedule
	if err := r.st.View(ctx, store.WithSQL(func(_ store.Tx, q store.SQL) error {
		var err error
		all, err = list(q)
		return err
	})); err != nil {
		return err
	}
	now := r.now().UTC()
	var errs []error
	for _, s := range all {
		if !s.Enabled {
			continue
		}
		if err := r.handle(ctx, s, now); err != nil {
			r.log.Error("schedule failed", "schedule", s.ID, "err", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (r *Runner) handle(ctx context.Context, s *Schedule, now time.Time) error {
	spec, err := ParseSpec(s.Cron, s.Timezone)
	if err != nil {
		return err
	}
	from := s.cursor
	if floor := now.Add(-maxLookback); from.Before(floor) {
		from = floor
	}
	var last time.Time
	due := 0
	for {
		next, ok := spec.Next(from)
		if !ok || next.After(now) {
			break
		}
		last, from = next, next
		due++
	}
	if due == 0 {
		return nil
	}
	if s.CatchUp == CatchUpSkip && now.Sub(last) > r.grace {
		return r.record(ctx, s, last, now, due, ReasonMissed)
	}
	if s.MaxActive > 0 {
		active, err := r.active(ctx, s)
		if err != nil {
			return err
		}
		if active >= s.MaxActive {
			return r.record(ctx, s, last, now, due-1, ReasonMaxActive)
		}
	}
	return r.execute(ctx, s, last, now, due-1)
}

func (r *Runner) record(ctx context.Context, s *Schedule, at, now time.Time, missed int, reason string) error {
	return r.st.Atomic(ctx, store.WithSQL(func(_ store.Tx, q store.SQL) error {
		_, err := claim(q, s, at, now, RunSkipped, missed, reason)
		return err
	}))
}

func (r *Runner) execute(ctx context.Context, s *Schedule, at, now time.Time, missed int) error {
	var claimed bool
	err := r.st.Atomic(ctx, store.WithSQL(func(_ store.Tx, q store.SQL) error {
		var err error
		claimed, err = claim(q, s, at, now, RunRunning, missed, "")
		return err
	}))
	if err != nil || !claimed {
		return err
	}
	briefID, taskIDs, runErr := r.do(ctx, s, at)
	status, detail := RunCreated, ""
	if runErr != nil {
		status, detail = RunFailed, clip(runErr.Error())
		r.log.Warn("schedule run failed", "schedule", s.ID, "at", at, "err", runErr)
	}
	done := r.now().UTC()
	return r.st.Atomic(ctx, store.WithSQL(func(t store.Tx, q store.SQL) error {
		if err := finish(q, s.ID, at, done, status, detail, briefID, taskIDs); err != nil {
			return err
		}
		failed := 0
		if runErr != nil {
			failed = 1
		}
		note := s.Name
		return t.InsertAudit(&model.AuditEvent{
			TS: done, AgentID: system.ID, Role: system.Role, Tool: "run_schedule", Note: &note,
			After: map[string]any{"schedule_id": s.ID, "scheduled_for": at.UTC().Format(time.RFC3339), "status": status,
				"failed": failed, "brief_id": briefID, "tasks": len(taskIDs)},
		})
	}))
}

func (r *Runner) do(ctx context.Context, s *Schedule, at time.Time) (string, []string, error) {
	if s.Action == ActionAddTasks {
		ids, err := r.exec.AddTasks(ctx, s, at)
		return s.BriefID, ids, err
	}
	id, err := r.exec.CreateBrief(ctx, s, at)
	return id, nil, err
}

func (r *Runner) active(ctx context.Context, s *Schedule) (int, error) {
	n := 0
	err := r.st.View(ctx, store.WithSQL(func(t store.Tx, q store.SQL) error {
		past, err := queryRuns(q, `WHERE schedule_id=? AND status=? ORDER BY instant DESC LIMIT ?`, s.ID, RunCreated, activeScan)
		if err != nil {
			return err
		}
		for _, run := range past {
			open, err := openRun(t, s, run)
			if err != nil {
				return err
			}
			if open {
				n++
			}
		}
		return nil
	}))
	return n, err
}

func openRun(t store.Tx, s *Schedule, run Run) (bool, error) {
	if s.Action == ActionCreateBrief {
		b, err := t.GetBrief(run.BriefID)
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return err == nil && b.Status == "open", err
	}
	for _, id := range run.TaskIDs {
		task, err := t.GetTask(id)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return false, err
		}
		if task.Status != model.StatusDone && task.Status != model.StatusCancelled {
			return true, nil
		}
	}
	return false, nil
}

func clip(s string) string {
	if len(s) <= maxDetail {
		return s
	}
	s = s[:maxDetail]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
