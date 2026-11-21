package spendguard

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/usage"
)

const (
	limitsKey = "spendguard.limits"
	stateKey  = "spendguard.state"
	bucket    = time.Minute
)

type TaskCost struct {
	ID        string
	CostEUR   float64
	UpdatedAt time.Time
}

type Tasks interface {
	Tasks(ctx context.Context) ([]TaskCost, error)
	Task(ctx context.Context, id string) (TaskCost, error)
}

type UsageSource interface {
	Query(ctx context.Context, q usage.Query) ([]usage.Record, error)
}

type Control interface {
	PauseAll(ctx context.Context, reason string) error
	ResumeAll(ctx context.Context) error
	Paused() (bool, string)
}

type KV interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, value string) error
}

type Options struct {
	Tasks    Tasks
	Usage    UsageSource
	EURToUSD float64
	Control  Control
	KV       KV
	Pub      events.Publisher
	Audit    func(ctx context.Context, before, after Limits) error
	Now      func() time.Time
	Log      *slog.Logger
}

type ledgerEntry struct {
	At  int64   `json:"at"`
	EUR float64 `json:"eur"`
}

type state struct {
	Seen     map[string]float64 `json:"seen"`
	Ledger   []ledgerEntry      `json:"ledger"`
	Tripped  map[Window]string  `json:"tripped"`
	Warned   map[Window]string  `json:"warned"`
	WarnedAt map[Window]int64   `json:"warned_at"`
}

type View struct {
	Limits
	Windows         []Status `json:"windows"`
	OrgBudgetPaused bool     `json:"org_budget_paused"`
}

type Guard struct {
	o      Options
	evalMu sync.Mutex
	mu     sync.Mutex
	limits Limits
	st     state
	usage  []Entry
	dirty  bool
}

func New(ctx context.Context, o Options) (*Guard, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.EURToUSD <= 0 {
		o.EURToUSD = 1
	}
	g := &Guard{o: o}
	if err := g.load(ctx, limitsKey, &g.limits); err != nil {
		return nil, err
	}
	if err := g.load(ctx, stateKey, &g.st); err != nil {
		return nil, err
	}
	if g.st.Seen == nil {
		g.st.Seen = map[string]float64{}
	}
	if g.st.Tripped == nil {
		g.st.Tripped = map[Window]string{}
	}
	if g.st.Warned == nil {
		g.st.Warned = map[Window]string{}
	}
	if g.st.WarnedAt == nil {
		g.st.WarnedAt = map[Window]int64{}
	}
	return g, nil
}

func (g *Guard) load(ctx context.Context, key string, into any) error {
	raw, ok, err := g.o.KV.Get(ctx, key)
	if err != nil || !ok || raw == "" {
		return err
	}
	if err := json.Unmarshal([]byte(raw), into); err != nil {
		return fmt.Errorf("spendguard: decode %s: %w", key, err)
	}
	return nil
}

func (g *Guard) save(ctx context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return g.o.KV.Set(ctx, key, string(raw))
}

func (g *Guard) Limits() Limits {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.limits
}

func (g *Guard) SetLimits(ctx context.Context, l Limits) (*View, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	g.evalMu.Lock()
	defer g.evalMu.Unlock()
	g.mu.Lock()
	before := g.limits
	g.mu.Unlock()
	if err := g.save(ctx, limitsKey, l); err != nil {
		return nil, err
	}
	g.mu.Lock()
	g.limits = l
	g.mu.Unlock()
	if g.o.Audit != nil && before != l {
		if err := g.o.Audit(ctx, before, l); err != nil {
			return nil, err
		}
	}
	if err := g.reconcile(ctx); err != nil {
		return nil, err
	}
	if err := g.evaluate(ctx); err != nil {
		return nil, err
	}
	return g.View(), nil
}

func (g *Guard) View() *View {
	g.mu.Lock()
	defer g.mu.Unlock()
	v := &View{Limits: g.limits, Windows: g.statuses(g.o.Now())}
	paused, why := g.o.Control.Paused()
	v.OrgBudgetPaused = paused && why == PauseReason
	return v
}

func (g *Guard) statuses(now time.Time) []Status {
	return Evaluate(g.limits, g.usage, g.ledger(), now)
}

func (g *Guard) ledger() []Entry {
	out := make([]Entry, len(g.st.Ledger))
	for i, e := range g.st.Ledger {
		out[i] = Entry{At: time.UnixMilli(e.At).UTC(), EUR: e.EUR}
	}
	return out
}

func (g *Guard) Observe(t TaskCost) bool {
	return g.observe(t, g.o.Now())
}

func (g *Guard) observe(t TaskCost, at time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	delta := t.CostEUR - g.st.Seen[t.ID]
	if delta <= 0 {
		return false
	}
	g.st.Seen[t.ID] = t.CostEUR
	ms := at.Truncate(bucket).UnixMilli()
	if n := len(g.st.Ledger); n > 0 && g.st.Ledger[n-1].At == ms {
		g.st.Ledger[n-1].EUR += delta
	} else {
		g.st.Ledger = append(g.st.Ledger, ledgerEntry{At: ms, EUR: delta})
	}
	g.dirty = true
	return true
}

func (g *Guard) Reconcile(ctx context.Context) error {
	g.evalMu.Lock()
	defer g.evalMu.Unlock()
	if err := g.reconcile(ctx); err != nil {
		return err
	}
	return g.evaluate(ctx)
}

func (g *Guard) reconcile(ctx context.Context) error {
	now := g.o.Now()
	if g.o.Tasks != nil {
		ts, err := g.o.Tasks.Tasks(ctx)
		if err != nil {
			return err
		}
		for _, t := range ts {
			at := t.UpdatedAt
			if at.IsZero() || at.After(now) {
				at = now
			}
			g.observe(t, at)
		}
	}
	if g.o.Usage == nil {
		return nil
	}
	recs, err := g.o.Usage.Query(ctx, usage.Query{From: WindowStart(Weekly, now), To: now.Add(time.Minute)})
	if err != nil {
		return err
	}
	entries := make([]Entry, len(recs))
	for i, r := range recs {
		entries[i] = Entry{At: r.TS, EUR: r.CostUSD / g.o.EURToUSD}
	}
	g.mu.Lock()
	g.usage = entries
	g.mu.Unlock()
	return nil
}

func (g *Guard) Evaluate(ctx context.Context) error {
	g.evalMu.Lock()
	defer g.evalMu.Unlock()
	return g.evaluate(ctx)
}

func windowKey(s Status) string {
	return fmt.Sprintf("%d|%g", s.StartsAt.Unix(), s.CapEUR)
}

func (g *Guard) evaluate(ctx context.Context) error {
	now := g.o.Now()
	g.mu.Lock()
	g.prune(now)
	statuses := g.statuses(now)
	var warn, trip []Status
	exceeded := false
	for _, s := range statuses {
		key := windowKey(s)
		if s.Level == LevelExceeded {
			exceeded = true
			if g.st.Tripped[s.Window] != key {
				trip = append(trip, s)
			}
		}
		if (s.Level == LevelWarning || s.Level == LevelExceeded) && g.st.Warned[s.Window] != key {
			g.st.Warned[s.Window] = key
			g.st.WarnedAt[s.Window] = now.UnixMilli()
			g.dirty = true
			warn = append(warn, s)
		}
	}
	g.mu.Unlock()

	for _, s := range warn {
		g.publishWarning(s)
	}
	paused, why := g.o.Control.Paused()
	var err error
	switch {
	case len(trip) > 0:
		if !paused {
			err = g.o.Control.PauseAll(ctx, PauseReason)
		}
		if err == nil {
			g.mu.Lock()
			for _, s := range trip {
				g.st.Tripped[s.Window] = windowKey(s)
			}
			g.dirty = true
			g.mu.Unlock()
		}
	case paused && why == PauseReason && !exceeded:
		err = g.o.Control.ResumeAll(ctx)
	}
	if perr := g.persist(ctx); err == nil {
		err = perr
	}
	return err
}

func (g *Guard) prune(now time.Time) {
	from := WindowStart(Weekly, now).UnixMilli()
	n := len(g.st.Ledger)
	g.st.Ledger = slices.DeleteFunc(g.st.Ledger, func(e ledgerEntry) bool { return e.At < from })
	if len(g.st.Ledger) != n {
		g.dirty = true
	}
}

func (g *Guard) persist(ctx context.Context) error {
	g.mu.Lock()
	if !g.dirty {
		g.mu.Unlock()
		return nil
	}
	raw, err := json.Marshal(g.st)
	g.dirty = false
	g.mu.Unlock()
	if err != nil {
		return err
	}
	if err := g.o.KV.Set(ctx, stateKey, string(raw)); err != nil {
		g.mu.Lock()
		g.dirty = true
		g.mu.Unlock()
		return err
	}
	return nil
}

func (g *Guard) publishWarning(s Status) {
	if g.o.Pub == nil {
		return
	}
	g.o.Pub.Publish([]string{"control"}, "spend.warning", map[string]any{
		"window": string(s.Window), "cap_eur": s.CapEUR, "spent_eur": s.SpentEUR, "percent": s.Percent,
		"level": string(s.Level), "starts_at": s.StartsAt,
	})
}

func (g *Guard) Decisions(context.Context) ([]*decisions.Decision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.o.Now()
	var out []*decisions.Decision
	for _, s := range g.statuses(now) {
		if s.Level != LevelWarning && s.Level != LevelExceeded {
			continue
		}
		created := now
		if ms, ok := g.st.WarnedAt[s.Window]; ok {
			created = time.UnixMilli(ms).UTC()
		}
		summary := fmt.Sprintf("€%.2f of the €%.2f %s cap is spent; the window resets %s", s.SpentEUR, s.CapEUR, s.Window, s.ResetsAt.Format(time.RFC3339))
		if s.Level == LevelExceeded {
			summary += "; spawns and claims are paused until then or until the cap is raised"
		} else if s.ProjectedExhaustionAt != nil {
			summary += "; projected to run out " + s.ProjectedExhaustionAt.Format(time.RFC3339)
		}
		out = append(out, &decisions.Decision{
			ID: "budget:org-" + string(s.Window), Kind: decisions.KindBudget,
			Title:     fmt.Sprintf("Organization %s spend is at %.0f%% of its cap", s.Window, s.Percent),
			Summary:   summary,
			CreatedAt: created,
		})
	}
	return out, nil
}

func (g *Guard) Run(ctx context.Context, every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	g.sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			g.sweep(ctx)
		}
	}
}

func (g *Guard) CheckTask(ctx context.Context, id string) error {
	t, err := g.o.Tasks.Task(ctx, id)
	if err != nil || !g.Observe(t) {
		return err
	}
	return g.Evaluate(ctx)
}

func (g *Guard) sweep(ctx context.Context) {
	err := g.Reconcile(ctx)
	if err == nil {
		err = g.Evaluate(ctx)
	}
	if err != nil && ctx.Err() == nil {
		g.o.Log.Error("spendguard sweep", "err", err)
	}
}
