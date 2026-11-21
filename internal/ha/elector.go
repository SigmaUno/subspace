package ha

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/events"
)

const (
	DefaultInterval = time.Second
	DefaultTTL      = 5 * time.Second
)

var errLeaseExpired = errors.New("leader lease expired before it could be renewed")

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

	clock func() time.Time

	mu       sync.Mutex
	leader   bool
	term     int64
	since    time.Time
	holder   string
	deadline time.Time
	watch    *time.Timer
	termCtx  context.Context
	end      context.CancelFunc
	changed  chan struct{}
}

func New(o Options) *Elector {
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	if o.TTL <= 0 {
		o.TTL = DefaultTTL
	}
	if o.TTL < 3*o.Interval {
		o.TTL = 3 * o.Interval
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
	e := &Elector{o: o, clock: time.Now, changed: make(chan struct{})}
	if o.Lock == nil {
		e.promote(e.clock())
		e.holder = o.ID
	}
	return e
}

func (e *Elector) ID() string { return e.o.ID }

func (e *Elector) IsLeader() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.leadingLocked()
}

func (e *Elector) leadingLocked() bool {
	return e.leader && (e.o.Lock == nil || e.clock().Before(e.deadline))
}

func (e *Elector) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	leading := e.leadingLocked()
	s := Status{Instance: e.o.ID, Backend: e.o.Backend, Leader: leading, Term: e.term, LeaseSeconds: e.o.TTL.Seconds()}
	if e.holder != "" {
		h := e.holder
		s.LeaderInstance = &h
	}
	if leading {
		since := e.since
		s.Since = &since
	}
	return s
}

func (e *Elector) current() (context.Context, <-chan struct{}) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.leadingLocked() {
		return e.termCtx, nil
	}
	return nil, e.changed
}

func (e *Elector) promote(start time.Time) {
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
	e.extendLocked(start)
	close(e.changed)
	e.changed = make(chan struct{})
}

func (e *Elector) extendLocked(start time.Time) {
	if e.o.Lock == nil {
		return
	}
	e.deadline = start.Add(e.o.TTL - e.o.Interval)
	if e.watch != nil {
		e.watch.Stop()
	}
	term := e.term
	e.watch = time.AfterFunc(e.deadline.Sub(e.clock()), func() { e.expire(term) })
}

func (e *Elector) expire(term int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.leader && e.term == term && !e.clock().Before(e.deadline) {
		e.demoteLocked()
	}
}

func (e *Elector) demote() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.demoteLocked()
}

func (e *Elector) demoteLocked() {
	if !e.leader {
		return
	}
	e.leader = false
	if e.holder == e.o.ID {
		e.holder = ""
	}
	if e.watch != nil {
		e.watch.Stop()
		e.watch = nil
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
	start := e.clock()
	e.mu.Lock()
	leader, deadline := e.leader, e.deadline
	e.mu.Unlock()
	if leader {
		e.renew(ctx, start, deadline.Sub(start))
		return
	}
	sctx, cancel := context.WithTimeout(ctx, e.o.TTL)
	defer cancel()
	ok, err := e.o.Lock.Acquire(sctx)
	if err != nil {
		if ctx.Err() == nil {
			e.o.Log.Debug("leader election", "instance", e.o.ID, "err", err)
		}
		e.setHolder("")
		return
	}
	if ok {
		e.promote(start)
		e.o.Log.Info("became leader", "instance", e.o.ID, "term", e.Status().Term)
		return
	}
	h, err := e.o.Lock.Holder(sctx)
	if err == nil {
		e.setHolder(h)
	}
}

func (e *Elector) renew(ctx context.Context, start time.Time, left time.Duration) {
	err := errLeaseExpired
	if left > 0 {
		rctx, cancel := context.WithTimeout(ctx, min(e.o.TTL, left))
		err = e.o.Lock.Renew(rctx)
		cancel()
	}
	if err == nil {
		e.mu.Lock()
		if e.leader {
			e.extendLocked(start)
		}
		e.mu.Unlock()
		return
	}
	if ctx.Err() == nil {
		e.o.Log.Warn("leader lease lost, stepping down", "instance", e.o.ID, "err", err)
	}
	e.demote()
	e.o.Lock.Release()
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
