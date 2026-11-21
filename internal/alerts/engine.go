package alerts

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/reload"
)

const (
	TopicAlerts   = "alerts"
	EventRaised   = "alert.raised"
	EventResolved = "alert.resolved"

	historyLimit   = 100
	priorityWarn   = 4
	priorityCrit   = 5
	decisionPrefix = "alert:"
)

type Source interface {
	Snapshot(ctx context.Context, now time.Time) (Snapshot, error)
}

type Options struct {
	Source     Source
	Thresholds Thresholds
	Publisher  events.Publisher
	Now        func() time.Time
	Log        *slog.Logger
}

type Engine struct {
	opt     Options
	th      atomic.Pointer[Thresholds]
	mu      sync.Mutex
	active  map[string]*Alert
	history []*Alert
}

func New(o Options) *Engine {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	e := &Engine{opt: o, active: map[string]*Alert{}}
	e.th.Store(&o.Thresholds)
	return e
}

func (e *Engine) Thresholds() Thresholds { return *e.th.Load() }

func (e *Engine) SetThresholds(t Thresholds) { e.th.Store(&t) }

func (e *Engine) PrepareReload(getenv func(string) string) (reload.Plan, error) {
	t, err := Parse(getenv)
	if err != nil {
		return reload.Plan{}, err
	}
	var ignored []reload.Ignored
	if cur := e.Thresholds(); t.Interval != cur.Interval {
		ignored = append(ignored, reload.Ignored{Key: EnvInterval, Reason: reload.ReasonRestart})
		t.Interval = cur.Interval
	}
	return reload.Plan{Commit: func() { e.SetThresholds(t) }, Ignored: ignored}, nil
}

func (e *Engine) Run(ctx context.Context, ticks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if err := e.Evaluate(ctx); err != nil && ctx.Err() == nil {
				e.opt.Log.Error("alert evaluation failed", "err", err)
			}
		}
	}
}

func (e *Engine) Evaluate(ctx context.Context) error {
	now := e.opt.Now().UTC()
	snap, err := e.opt.Source.Snapshot(ctx, now)
	if err != nil {
		return err
	}
	findings := Evaluate(snap, e.Thresholds())

	e.mu.Lock()
	defer e.mu.Unlock()
	seen := make(map[string]bool, len(findings))
	for _, f := range findings {
		seen[f.Key] = true
		if a, ok := e.active[f.Key]; ok {
			a.Severity, a.Title, a.Summary = f.Severity, f.Title, f.Summary
			continue
		}
		a := &Alert{
			ID: f.Key, Severity: f.Severity, Title: f.Title, Summary: f.Summary,
			BriefID: f.BriefID, TaskID: f.TaskID, AgentID: f.AgentID,
			Status: StatusActive, RaisedAt: now,
		}
		e.active[f.Key] = a
		e.publish(EventRaised, a, now)
	}
	for key, a := range e.active {
		if seen[key] {
			continue
		}
		delete(e.active, key)
		a.Status = StatusResolved
		a.ResolvedAt = &now
		e.history = append([]*Alert{a}, e.history...)
		if len(e.history) > historyLimit {
			e.history = e.history[:historyLimit]
		}
		e.publish(EventResolved, a, now)
	}
	return nil
}

func (e *Engine) publish(typ string, a *Alert, now time.Time) {
	if e.opt.Publisher == nil {
		return
	}
	prio := priorityWarn
	if a.Severity == SeverityCritical {
		prio = priorityCrit
	}
	e.opt.Publisher.Publish([]string{TopicAlerts}, typ, notify.Notification{
		ID: decisionPrefix + a.ID, Kind: decisions.KindAlert, Title: a.Title, Text: a.Summary,
		Priority: prio, BriefID: a.BriefID, TaskID: a.TaskID, TS: now,
	})
}

func (e *Engine) List(status string, limit int) []Alert {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := []Alert{}
	if status != StatusResolved {
		active := make([]*Alert, 0, len(e.active))
		for _, a := range e.active {
			active = append(active, a)
		}
		slices.SortFunc(active, func(a, b *Alert) int {
			if c := a.RaisedAt.Compare(b.RaisedAt); c != 0 {
				return c
			}
			return cmp.Compare(a.ID, b.ID)
		})
		for _, a := range active {
			out = append(out, *a)
		}
	}
	if status != StatusActive {
		for _, a := range e.history {
			out = append(out, *a)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (e *Engine) Decisions(context.Context) ([]*decisions.Decision, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]*decisions.Decision, 0, len(e.active))
	for _, a := range e.active {
		out = append(out, &decisions.Decision{
			ID: decisionPrefix + a.ID, Kind: decisions.KindAlert, Title: a.Title, Summary: a.Summary,
			BriefID: a.BriefID, TaskID: a.TaskID, AgentID: a.AgentID, CreatedAt: a.RaisedAt,
		})
	}
	return out, nil
}
