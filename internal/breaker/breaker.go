package breaker

import (
	"sort"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

type State string

const (
	Closed   State = "closed"
	Open     State = "open"
	HalfOpen State = "half_open"
)

type Config struct {
	Threshold  float64
	Window     time.Duration
	MinSamples int
	Cooldown   time.Duration
}

const (
	DefaultThreshold  = 0.5
	DefaultWindow     = 5 * time.Minute
	DefaultMinSamples = 10
	DefaultCooldown   = time.Minute
)

func (c Config) withDefaults() Config {
	if c.Threshold <= 0 {
		c.Threshold = DefaultThreshold
	}
	if c.Window <= 0 {
		c.Window = DefaultWindow
	}
	if c.MinSamples <= 0 {
		c.MinSamples = DefaultMinSamples
	}
	if c.Cooldown <= 0 {
		c.Cooldown = DefaultCooldown
	}
	return c
}

type Key struct{ Provider, Model string }

type Status struct {
	Provider   string     `json:"provider"`
	Model      string     `json:"model"`
	State      State      `json:"state"`
	ErrorRate  float64    `json:"error_rate"`
	Samples    int        `json:"samples"`
	Errors     int        `json:"errors"`
	Threshold  float64    `json:"threshold"`
	OpenedAt   *time.Time `json:"opened_at"`
	HalfOpenAt *time.Time `json:"half_open_at"`
}

type sample struct {
	at     time.Time
	failed bool
}

type entry struct {
	state     State
	samples   []sample
	openedAt  time.Time
	probing   bool
	probeAt   time.Time
	samplesAt int
	errorsAt  int
}

type Set struct {
	cfg      Config
	now      func() time.Time
	mu       sync.Mutex
	items    map[Key]*entry
	onChange func(Status)
}

func New(cfg Config, now func() time.Time, onChange func(Status)) *Set {
	if now == nil {
		now = time.Now
	}
	return &Set{cfg: cfg.withDefaults(), now: now, items: map[Key]*entry{}, onChange: onChange}
}

func (s *Set) entry(k Key) *entry {
	e, ok := s.items[k]
	if !ok {
		e = &entry{state: Closed}
		s.items[k] = e
	}
	return e
}

func (s *Set) advance(e *entry, now time.Time) {
	if e.state == Open && !now.Before(e.openedAt.Add(s.cfg.Cooldown)) {
		e.state = HalfOpen
		e.probing = false
		e.samples = nil
	}
	if e.state == HalfOpen && e.probing && !now.Before(e.probeAt.Add(s.cfg.Cooldown)) {
		e.probing = false
	}
}

func (s *Set) prune(e *entry, now time.Time) {
	cut := now.Add(-s.cfg.Window)
	i := 0
	for i < len(e.samples) && e.samples[i].at.Before(cut) {
		i++
	}
	e.samples = e.samples[i:]
}

func (s *Set) status(k Key, e *entry, now time.Time) Status {
	st := Status{Provider: k.Provider, Model: k.Model, State: e.state, Threshold: s.cfg.Threshold}
	if e.state == Closed {
		s.prune(e, now)
		st.Samples, st.Errors = count(e.samples)
	} else {
		st.Samples, st.Errors = e.samplesAt, e.errorsAt
	}
	if st.Samples > 0 {
		st.ErrorRate = float64(st.Errors) / float64(st.Samples)
	}
	if e.state != Closed {
		o := e.openedAt
		st.OpenedAt = &o
		if e.state == Open {
			h := o.Add(s.cfg.Cooldown)
			st.HalfOpenAt = &h
		}
	}
	return st
}

func (s *Set) Record(provider, mdl string, failed bool) {
	k := Key{provider, mdl}
	now := s.now()
	s.mu.Lock()
	e := s.entry(k)
	s.advance(e, now)
	prev := e.state
	switch e.state {
	case Closed:
		s.prune(e, now)
		e.samples = append(e.samples, sample{now, failed})
		if s.exceeded(e) {
			s.open(e, now)
		}
	case HalfOpen:
		if failed {
			s.open(e, now)
		} else {
			e.state = Closed
			e.probing = false
			e.samples = nil
		}
	}
	changed := e.state != prev
	st := s.status(k, e, now)
	s.mu.Unlock()
	if changed && s.onChange != nil {
		s.onChange(st)
	}
}

func count(samples []sample) (n, failed int) {
	for _, x := range samples {
		if x.failed {
			failed++
		}
	}
	return len(samples), failed
}

func (s *Set) exceeded(e *entry) bool {
	n, failed := count(e.samples)
	return n >= s.cfg.MinSamples && float64(failed)/float64(n) > s.cfg.Threshold
}

func (s *Set) open(e *entry, now time.Time) {
	if len(e.samples) > 0 {
		e.samplesAt, e.errorsAt = count(e.samples)
	}
	e.state = Open
	e.openedAt = now
	e.probing = false
	e.samples = nil
}

func (s *Set) State(provider, mdl string) State {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.items[Key{provider, mdl}]
	if !ok {
		return Closed
	}
	s.advance(e, s.now())
	return e.state
}

func (s *Set) Admit(provider, mdl string, n int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.items[Key{provider, mdl}]
	if !ok {
		return nil
	}
	now := s.now()
	s.advance(e, now)
	switch e.state {
	case Open:
		return model.Errf("capacity", "circuit breaker for %s/%s is open: no new agents until it half-opens", provider, mdl)
	case HalfOpen:
		if n != 1 || e.probing {
			return model.Errf("capacity", "circuit breaker for %s/%s is half-open: one probe agent at a time", provider, mdl)
		}
		e.probing = true
		e.probeAt = now
	}
	return nil
}

func (s *Set) List() []Status {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Status, 0, len(s.items))
	for k, e := range s.items {
		s.advance(e, now)
		out = append(out, s.status(k, e, now))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Model < out[j].Model
	})
	return out
}

func (s *Set) Reset(provider string) []Status {
	now := s.now()
	s.mu.Lock()
	var out, changed []Status
	for k, e := range s.items {
		if k.Provider != provider {
			continue
		}
		s.advance(e, now)
		was := e.state
		e.state = Closed
		e.probing = false
		e.samples = nil
		st := s.status(k, e, now)
		out = append(out, st)
		if was != Closed {
			changed = append(changed, st)
		}
	}
	s.mu.Unlock()
	if s.onChange != nil {
		for _, st := range changed {
			s.onChange(st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}
