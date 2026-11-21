package notify

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/events"
)

const maxTracked = 4096

type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

type Config struct {
	Sinks     []Sink
	Queue     int
	Window    time.Duration
	Attempts  int
	BaseDelay time.Duration
	MaxDelay  time.Duration
	Clock     Clock
	Jitter    func(n int64) int64
	Log       *slog.Logger
	Mint      Minter
}

type Stats struct {
	Sent    int64 `json:"sent"`
	Failed  int64 `json:"failed"`
	Dropped int64 `json:"dropped"`
	Queued  int   `json:"queued"`
}

type Service struct {
	cfg     Config
	mu      sync.RWMutex
	lanes   []*lane
	ctx     context.Context
	hub     events.Hub
	looping bool
	wg      sync.WaitGroup
	sent    atomic.Int64
	failed  atomic.Int64
	dropped atomic.Int64
}

type lane struct {
	s      *Service
	sink   Sink
	q      chan Notification
	retire chan struct{}
}

func New(cfg Config) *Service {
	if cfg.Queue <= 0 {
		cfg.Queue = 64
	}
	if cfg.Window == 0 {
		cfg.Window = time.Second
	}
	if cfg.Attempts <= 0 {
		cfg.Attempts = 5
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = time.Second
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = 30 * time.Second
	}
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	if cfg.Jitter == nil {
		cfg.Jitter = rand.Int64N
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	s := &Service{cfg: cfg}
	s.lanes = s.newLanes(cfg.Sinks)
	return s
}

func (s *Service) newLanes(sinks []Sink) []*lane {
	lanes := make([]*lane, 0, len(sinks))
	for _, k := range sinks {
		lanes = append(lanes, &lane{s: s, sink: k, q: make(chan Notification, s.cfg.Queue), retire: make(chan struct{})})
	}
	return lanes
}

func (s *Service) snapshot() []*lane {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lanes
}

func (s *Service) startLanes(ctx context.Context, hub events.Hub) {
	s.mu.Lock()
	s.ctx, s.hub = ctx, hub
	lanes := s.lanes
	s.mu.Unlock()
	for _, l := range lanes {
		s.wg.Go(func() { l.run(ctx) })
	}
}

func (s *Service) ReplaceSinks(sinks []Sink) {
	next := s.newLanes(sinks)
	s.mu.Lock()
	old := s.lanes
	s.lanes = next
	ctx := s.ctx
	s.mu.Unlock()
	for _, l := range old {
		close(l.retire)
	}
	if ctx == nil {
		return
	}
	for _, l := range next {
		s.wg.Go(func() { l.run(ctx) })
	}
	if len(next) > 0 {
		s.startLoop(ctx)
	}
}

func (s *Service) startLoop(ctx context.Context) {
	s.mu.Lock()
	hub := s.hub
	if s.looping || hub == nil {
		s.mu.Unlock()
		return
	}
	s.looping = true
	s.mu.Unlock()
	sub := hub.Subscribe(Topics)
	s.wg.Go(func() {
		defer sub.Close()
		s.loop(ctx, sub)
	})
}

func (s *Service) Enabled() bool { return len(s.snapshot()) > 0 }

func (s *Service) SinkNames() []string {
	lanes := s.snapshot()
	names := make([]string, len(lanes))
	for i, l := range lanes {
		names[i] = l.sink.Name()
	}
	return names
}

func (s *Service) Stats() Stats {
	st := Stats{Sent: s.sent.Load(), Failed: s.failed.Load(), Dropped: s.dropped.Load()}
	for _, l := range s.snapshot() {
		st.Queued += len(l.q)
	}
	return st
}

func (s *Service) Start(ctx context.Context, hub events.Hub) {
	s.startLanes(ctx, hub)
	if s.Enabled() {
		s.startLoop(ctx)
	}
}

func (s *Service) Wait() { s.wg.Wait() }

func (s *Service) loop(ctx context.Context, sub events.Subscription) {
	last := make(map[string]string)
	pending := make(map[string]Notification)
	var timer <-chan time.Time
	flush := func() {
		batch := make([]Notification, 0, len(pending))
		for _, n := range pending {
			batch = append(batch, n)
		}
		clear(pending)
		slices.SortFunc(batch, func(a, b Notification) int {
			return cmp.Or(a.TS.Compare(b.TS), cmp.Compare(a.ID, b.ID))
		})
		for _, n := range batch {
			n = withActions(n, s.cfg.Mint)
			for _, l := range s.snapshot() {
				l.enqueue(n)
			}
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer:
			timer = nil
			flush()
		case ev, ok := <-sub.C():
			if !ok {
				return
			}
			id, sig, n := describe(ev)
			if id == "" || last[id] == sig {
				continue
			}
			if len(last) >= maxTracked {
				clear(last)
			}
			last[id] = sig
			if n == nil {
				delete(pending, id)
				continue
			}
			pending[id] = *n
			if s.cfg.Window < 0 {
				flush()
			} else if timer == nil {
				timer = s.cfg.Clock.After(s.cfg.Window)
			}
		}
	}
}

func (l *lane) enqueue(n Notification) {
	for {
		select {
		case l.q <- n:
			return
		default:
		}
		select {
		case <-l.q:
			l.s.dropped.Add(1)
		default:
		}
	}
}

func (l *lane) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.retire:
			l.drain(ctx)
			return
		case n := <-l.q:
			l.deliver(ctx, n)
		}
	}
}

func (l *lane) drain(ctx context.Context) {
	for {
		select {
		case n := <-l.q:
			l.deliver(ctx, n)
		default:
			return
		}
	}
}

func (l *lane) deliver(ctx context.Context, n Notification) {
	cfg := l.s.cfg
	for attempt := 1; ; attempt++ {
		err := l.sink.Send(ctx, n)
		if err == nil {
			l.s.sent.Add(1)
			return
		}
		var perm permanentError
		if ctx.Err() != nil || errors.As(err, &perm) || attempt >= cfg.Attempts {
			l.s.failed.Add(1)
			cfg.Log.Warn("notify delivery failed", "sink", l.sink.Name(), "id", n.ID, "attempts", attempt, "err", err)
			return
		}
		select {
		case <-cfg.Clock.After(l.s.backoff(attempt)):
		case <-ctx.Done():
			return
		}
	}
}

func (s *Service) backoff(attempt int) time.Duration {
	d := s.cfg.MaxDelay
	if attempt < 32 && s.cfg.BaseDelay<<(attempt-1) < d {
		d = s.cfg.BaseDelay << (attempt - 1)
	}
	half := d / 2
	return half + time.Duration(s.cfg.Jitter(int64(half)+1))
}
