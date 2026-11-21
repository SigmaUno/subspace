package digest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/reload"
)

const (
	maxWait   = time.Minute
	maxWindow = 8 * 24 * time.Hour
	stateKey  = "digest.state."
)

type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

type Source interface {
	Snapshot(ctx context.Context, since, until time.Time) (Snapshot, error)
}

type Sink interface {
	Publish(n notify.Notification)
}

type KV interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, value string) error
}

type Options struct {
	Rules  []Rule
	Source Source
	Sink   Sink
	KV     KV
	Clock  Clock
	Log    *slog.Logger
}

type ruleState struct {
	Slot        int64  `json:"slot"`
	At          int64  `json:"at"`
	Fingerprint string `json:"fingerprint"`
}

type Scheduler struct {
	o     Options
	rules atomic.Pointer[[]Rule]
	wake  chan struct{}

	mu     sync.Mutex
	rebase map[string]bool
}

func NewScheduler(o Options) *Scheduler {
	if o.Clock == nil {
		o.Clock = realClock{}
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	s := &Scheduler{o: o, wake: make(chan struct{}, 1), rebase: map[string]bool{}}
	s.rules.Store(&o.Rules)
	return s
}

func (s *Scheduler) Rules() []Rule { return *s.rules.Load() }

func (s *Scheduler) SetRules(rules []Rule) {
	old := s.Rules()
	s.mu.Lock()
	for _, r := range rules {
		if i := slices.IndexFunc(old, func(o Rule) bool { return o.Name == r.Name }); i < 0 || !sameRule(old[i], r) {
			s.rebase[r.Name] = true
		}
	}
	s.mu.Unlock()
	s.rules.Store(&rules)
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Scheduler) PrepareReload(getenv func(string) string) (reload.Plan, error) {
	cfg, err := ConfigFromEnv(getenv)
	if err != nil {
		return reload.Plan{}, err
	}
	return reload.Plan{Commit: func() { s.SetRules(cfg.Rules) }}, nil
}

func sameRule(a, b Rule) bool {
	return a.Loc.String() == b.Loc.String() && slices.Equal(a.Slots, b.Slots)
}

func (s *Scheduler) rebasing(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rebase[name]
}

func (s *Scheduler) rebased(name string) {
	s.mu.Lock()
	delete(s.rebase, name)
	s.mu.Unlock()
}

func (s *Scheduler) enabled() bool {
	e, ok := s.o.Sink.(interface{ Enabled() bool })
	return !ok || e.Enabled()
}

func (s *Scheduler) Run(ctx context.Context) {
	for {
		now := s.o.Clock.Now()
		s.Step(ctx, now)
		wait := maxWait
		for _, r := range s.Rules() {
			if next, ok := r.Next(now); ok {
				wait = min(wait, next.Sub(now))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-s.o.Clock.After(wait):
		}
	}
}

func (s *Scheduler) Step(ctx context.Context, now time.Time) {
	if !s.enabled() {
		return
	}
	for _, r := range s.Rules() {
		if err := s.step(ctx, r, now); err != nil && ctx.Err() == nil {
			s.o.Log.Error("digest", "rule", r.Name, "err", err)
		}
	}
}

func (s *Scheduler) step(ctx context.Context, r Rule, now time.Time) error {
	due, ok := r.Latest(now)
	if !ok {
		return nil
	}
	key := stateKey + r.Name
	var st ruleState
	raw, found, err := s.o.KV.Get(ctx, key)
	if err != nil {
		return err
	}
	if found && raw != "" {
		if err := json.Unmarshal([]byte(raw), &st); err != nil {
			return fmt.Errorf("decode %s: %w", key, err)
		}
	}
	if found && !due.After(time.Unix(st.Slot, 0)) {
		return nil
	}
	if found && s.rebasing(r.Name) {
		st.Slot, st.At = due.Unix(), now.UnixMilli()
		if err := s.save(ctx, key, st); err != nil {
			return err
		}
		s.rebased(r.Name)
		return nil
	}
	if !found {
		return s.save(ctx, key, ruleState{Slot: due.Unix(), At: due.UnixMilli()})
	}
	since := time.UnixMilli(st.At)
	if floor := now.Add(-maxWindow); since.Before(floor) {
		since = floor
	}
	snap, err := s.o.Source.Snapshot(ctx, since, now)
	if err != nil {
		return err
	}
	d := Build(snap)
	if d.ShouldSend(st.Fingerprint) {
		s.o.Sink.Publish(d.Notification(fmt.Sprintf("digest:%s:%d", r.Name, due.Unix())))
		st.Fingerprint = d.Fingerprint()
	}
	st.Slot, st.At = due.Unix(), now.UnixMilli()
	return s.save(ctx, key, st)
}

func (s *Scheduler) save(ctx context.Context, key string, st ruleState) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.o.KV.Set(ctx, key, string(raw))
}
