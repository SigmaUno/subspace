package digest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/SigmaUno/subspace/internal/notify"
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
	o Options
}

func NewScheduler(o Options) *Scheduler {
	if o.Clock == nil {
		o.Clock = realClock{}
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Scheduler{o: o}
}

func (s *Scheduler) Run(ctx context.Context) {
	for {
		now := s.o.Clock.Now()
		s.Step(ctx, now)
		wait := maxWait
		for _, r := range s.o.Rules {
			if next, ok := r.Next(now); ok {
				wait = min(wait, next.Sub(now))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-s.o.Clock.After(wait):
		}
	}
}

func (s *Scheduler) Step(ctx context.Context, now time.Time) {
	for _, r := range s.o.Rules {
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
