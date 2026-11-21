package ha

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/events"
)

const (
	DefaultInterval = time.Second
	DefaultTTL      = 5 * time.Second
)

type Lock interface {
	Acquire(ctx context.Context) (bool, error)
	Renew(ctx context.Context) error
	Holder(ctx context.Context) (string, error)
	Release()
}

type Options struct {
	ID       string
	Backend  string
	Lock     Lock
	Interval time.Duration
	TTL      time.Duration
	Log      *slog.Logger
	Now      func() time.Time
}

type Status struct {
	Instance       string     `json:"instance"`
	Backend        string     `json:"backend"`
	Leader         bool       `json:"leader"`
	LeaderInstance *string    `json:"leader_instance"`
	Term           int64      `json:"term"`
	Since          *time.Time `json:"since"`
	LeaseSeconds   float64    `json:"lease_seconds"`
	Events         EventStats `json:"events"`
}

type EventStats struct {
	Received int64 `json:"received"`
	Dropped  int64 `json:"dropped"`
}

type Elector struct {
	o Options

	mu      sync.Mutex
	leader  bool
	term    int64
	since   time.Time
	holder  string
	termCtx context.Context
	end     context.CancelFunc
	changed chan struct{}
}

func New(o Options) *Elector {
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	if o.TTL <= 0 {
		o.TTL = DefaultTTL
	}
	if o.TTL < 2*o.Interval {
		o.TTL = 2 * o.Interval
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	if o.Backend == "" {
		o.Backend = "single"
	}
	e := &Elector{o: o, changed: make(chan struct{})}
	if o.Lock == nil {
		e.promote()
		e.holder = o.ID
	}
	return e
}

func (e *Elector) ID() string { return e.o.ID }

func (e *Elector) IsLeader() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.leader
}

func (e *Elector) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := Status{Instance: e.o.ID, Backend: e.o.Backend, Leader: e.leader, Term: e.term, LeaseSeconds: e.o.TTL.Seconds()}
	if e.holder != "" {
		h := e.holder
		s.LeaderInstance = &h
	}
	if e.leader {
		since := e.since
		s.Since = &since
	}
	return s
}

func (e *Elector) current() (context.Context, <-chan struct{}) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.leader {
		return e.termCtx, nil
	}
	return nil, e.changed
}

func (e *Elector) promote() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.leader {
		return
	}
	e.leader = true
	e.term++
	e.since = e.o.Now()
	e.holder = e.o.ID
	e.termCtx, e.end = context.WithCancel(context.Background())
	close(e.changed)
	e.changed = make(chan struct{})
}

func (e *Elector) demote() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.leader {
		return
	}
	e.leader = false
	if e.holder == e.o.ID {
		e.holder = ""
	}
	e.end()
	close(e.changed)
	e.changed = make(chan struct{})
}

func (e *Elector) setHolder(h string) {
	e.mu.Lock()
	e.holder = h
	e.mu.Unlock()
}

func (e *Elector) Run(ctx context.Context) {
	if e.o.Lock == nil {
		<-ctx.Done()
		e.demote()
		return
	}
	defer func() {
		e.demote()
		e.o.Lock.Release()
	}()
	t := time.NewTicker(e.o.Interval)
	defer t.Stop()
	for {
		e.step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (e *Elector) step(ctx context.Context) {
	sctx, cancel := context.WithTimeout(ctx, e.o.TTL)
	defer cancel()
	if e.IsLeader() {
		if err := e.o.Lock.Renew(sctx); err != nil {
			if ctx.Err() == nil {
				e.o.Log.Warn("leader lease lost, stepping down", "instance", e.o.ID, "err", err)
			}
			e.demote()
			e.o.Lock.Release()
		}
		return
	}
	ok, err := e.o.Lock.Acquire(sctx)
	if err != nil {
		if ctx.Err() == nil {
			e.o.Log.Debug("leader election", "instance", e.o.ID, "err", err)
		}
		e.setHolder("")
		return
	}
	if ok {
		e.promote()
		e.o.Log.Info("became leader", "instance", e.o.ID, "term", e.Status().Term)
		return
	}
	h, err := e.o.Lock.Holder(sctx)
	if err == nil {
		e.setHolder(h)
	}
}

func (e *Elector) Lead(ctx context.Context, fn func(context.Context)) {
	for {
		term, wait := e.current()
		if term == nil {
			select {
			case <-ctx.Done():
				return
			case <-wait:
				continue
			}
		}
		run, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(term, cancel)
		fn(run)
		<-run.Done()
		stop()
		cancel()
		if ctx.Err() != nil {
			return
		}
	}
}

func (e *Elector) Go(ctx context.Context, fn func(context.Context)) {
	go e.Lead(ctx, fn)
}

func (e *Elector) Gate(h events.Hub) events.Hub { return gated{Hub: h, e: e} }

type gated struct {
	events.Hub
	e *Elector
}

func (g gated) Subscribe(topics []string) events.Subscription {
	inner := g.Hub.Subscribe(topics)
	s := &gatedSub{inner: inner, ch: make(chan events.Event, events.SubscriberBuffer)}
	go s.pump(g.e)
	return s
}

type gatedSub struct {
	inner events.Subscription
	ch    chan events.Event
}

func (s *gatedSub) pump(e *Elector) {
	defer close(s.ch)
	for ev := range s.inner.C() {
		if !e.IsLeader() {
			continue
		}
		select {
		case s.ch <- ev:
			continue
		default:
		}
		select {
		case <-s.ch:
		default:
		}
		select {
		case s.ch <- ev:
		default:
		}
	}
}

func (s *gatedSub) C() <-chan events.Event { return s.ch }
func (s *gatedSub) TakeDropped() int       { return s.inner.TakeDropped() }
func (s *gatedSub) Close()                 { s.inner.Close() }
