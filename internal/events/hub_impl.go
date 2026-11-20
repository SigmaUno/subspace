package events

import (
	"sync"
	"sync/atomic"
	"time"
)

const subBuffer = 256

type sub struct {
	h       *hub
	ch      chan Event
	topics  []string
	mu      sync.Mutex
	dropped int
	closed  bool
}

type hub struct {
	mu    sync.RWMutex
	byTop map[string]map[*sub]struct{}
	seq   atomic.Int64
	nsubs atomic.Int64
	nowFn func() time.Time
}

// NewHub returns an in-process topic-filtered hub.
func NewHub() Hub {
	return &hub{byTop: map[string]map[*sub]struct{}{}, nowFn: func() time.Time { return time.Now().UTC() }}
}

func (h *hub) Publish(topics []string, typ string, data any) {
	ev := Event{Seq: h.seq.Add(1), Type: typ, TS: h.nowFn(), Data: data}
	h.mu.RLock()
	seen := make(map[*sub]struct{}, 8)
	deliver := func(t string) {
		for s := range h.byTop[t] {
			if _, dup := seen[s]; dup {
				continue
			}
			seen[s] = struct{}{}
			s.send(ev)
		}
	}
	deliver("all")
	for _, t := range topics {
		deliver(t)
	}
	h.mu.RUnlock()
}

func (s *sub) send(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	for {
		select {
		case s.ch <- ev:
			return
		default:
			// full: drop the oldest event, count it, retry
			select {
			case <-s.ch:
				s.dropped++
			default:
			}
		}
	}
}

func (h *hub) Subscribe(topics []string) Subscription {
	if len(topics) == 0 {
		topics = []string{"all"}
	}
	s := &sub{h: h, ch: make(chan Event, subBuffer), topics: topics}
	h.mu.Lock()
	for _, t := range topics {
		if h.byTop[t] == nil {
			h.byTop[t] = map[*sub]struct{}{}
		}
		h.byTop[t][s] = struct{}{}
	}
	h.mu.Unlock()
	h.nsubs.Add(1)
	return s
}

func (h *hub) Subscribers() int { return int(h.nsubs.Load()) }

// Seq returns the latest sequence number.
func (h *hub) Seq() int64 { return h.seq.Load() }

func (s *sub) C() <-chan Event { return s.ch }

func (s *sub) TakeDropped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.dropped
	s.dropped = 0
	return d
}

func (s *sub) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	h := s.h
	h.mu.Lock()
	for _, t := range s.topics {
		delete(h.byTop[t], s)
		if len(h.byTop[t]) == 0 {
			delete(h.byTop, t)
		}
	}
	h.mu.Unlock()
	h.nsubs.Add(-1)
}
