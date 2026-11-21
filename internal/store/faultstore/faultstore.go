package faultstore

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/store"
)

var (
	ErrInjected       = errors.New("faultstore: injected fault")
	ErrUnknownOutcome = fmt.Errorf("%w: transaction committed but reported as failed", ErrInjected)
)

type Kind string

const (
	KindError    Kind = "error"
	KindCancel   Kind = "cancel"
	KindPanic    Kind = "panic"
	KindDelay    Kind = "delay"
	KindRollback Kind = "rollback"
	KindCommit   Kind = "commit"
)

type Faults struct {
	FailAt       []int64
	ErrorRate    float64
	CancelRate   float64
	PanicRate    float64
	DelayRate    float64
	Delay        time.Duration
	RollbackRate float64
	CommitRate   float64
	Reads        bool
}

type Event struct {
	Op     int64
	Kind   Kind
	Method string
}

type Store struct {
	inner store.Store
	sleep func(time.Duration)

	mu   sync.Mutex
	rng  *rand.Rand
	f    Faults
	ops  int64
	hist []Event
}

var _ store.Store = (*Store)(nil)

func New(inner store.Store, seed int64, f Faults) *Store {
	return &Store{inner: inner, sleep: time.Sleep, rng: rand.New(rand.NewSource(seed)), f: f} //nolint:gosec // deterministic fault schedule
}

func (s *Store) Inner() store.Store { return s.inner }

func (s *Store) SetSleep(fn func(time.Duration)) {
	s.mu.Lock()
	s.sleep = fn
	s.mu.Unlock()
}

func (s *Store) Set(f Faults) {
	s.mu.Lock()
	s.f = f
	s.mu.Unlock()
}

func (s *Store) Reset(seed int64) {
	s.mu.Lock()
	s.rng.Seed(seed)
	s.ops, s.hist = 0, nil
	s.mu.Unlock()
}

func (s *Store) Faults() Faults {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f
}

func (s *Store) Ops() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ops
}

func (s *Store) Injected() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.hist)
}

func (s *Store) record(k Kind, method string) Kind {
	s.hist = append(s.hist, Event{Op: s.ops, Kind: k, Method: method})
	return k
}

func (s *Store) next(method string, write bool) (Kind, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !write && !s.f.Reads {
		return "", 0
	}
	s.ops++
	u := s.rng.Float64()
	if slices.Contains(s.f.FailAt, s.ops) {
		return s.record(KindError, method), 0
	}
	f := s.f
	for _, c := range []struct {
		k    Kind
		rate float64
	}{{KindError, f.ErrorRate}, {KindCancel, f.CancelRate}, {KindPanic, f.PanicRate}, {KindDelay, f.DelayRate}} {
		if c.k == KindPanic && !write {
			continue
		}
		if u < c.rate {
			return s.record(c.k, method), f.Delay
		}
		u -= c.rate
	}
	return "", 0
}

func (s *Store) commit() Kind {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ops++
	u := s.rng.Float64()
	switch {
	case slices.Contains(s.f.FailAt, s.ops):
		return s.record(KindRollback, "commit")
	case u < s.f.RollbackRate:
		return s.record(KindRollback, "commit")
	case u < s.f.RollbackRate+s.f.CommitRate:
		return s.record(KindCommit, "commit")
	}
	return ""
}

func (s *Store) delay(d time.Duration) {
	s.mu.Lock()
	sleep := s.sleep
	s.mu.Unlock()
	sleep(d)
}

func (s *Store) Atomic(ctx context.Context, fn func(store.Tx) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var outcome Kind
	err := s.inner.Atomic(ctx, func(in store.Tx) error {
		outcome = ""
		if err := fn(&Tx{s: s, in: in, ctx: ctx, cancel: cancel, write: true}); err != nil {
			return err
		}
		if outcome = s.commit(); outcome == KindRollback {
			return fmt.Errorf("%w: rolled back at commit", ErrInjected)
		}
		return nil
	})
	if err == nil && outcome == KindCommit {
		return ErrUnknownOutcome
	}
	return err
}

func (s *Store) View(ctx context.Context, fn func(store.Tx) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	return s.inner.View(ctx, func(in store.Tx) error {
		return fn(&Tx{s: s, in: in, ctx: ctx, cancel: cancel})
	})
}

func (s *Store) Backup(ctx context.Context, path string) error { return s.inner.Backup(ctx, path) }

func (s *Store) Close() error { return s.inner.Close() }

func (s *Store) Done() <-chan struct{} {
	if d, ok := s.inner.(interface{ Done() <-chan struct{} }); ok {
		return d.Done()
	}
	return nil
}
