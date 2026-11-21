package events

import (
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

const TopicAll = "all"

const SubscriberBuffer = 256

type hub struct {
	mu     sync.Mutex
	seq    int64
	topics map[string][]*sub
	count  int
	closed bool
}

type sub struct {
	h       *hub
	ch      chan Event
	topics  []string
	all     bool
	mark    int64
	dropped atomic.Int64
	closed  bool
}

func NewHub() Hub {
	return &hub{topics: make(map[string][]*sub)}
}

func (h *hub) Seq() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seq
}

func (h *hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.count
}

func (h *hub) Subscribe(topics []string) Subscription {
	s := &sub{h: h, ch: make(chan Event, SubscriberBuffer)}
	for _, t := range topics {
		if t == "" || slices.Contains(s.topics, t) {
			continue
		}
		s.topics = append(s.topics, t)
	}
	if len(s.topics) == 0 {
		s.topics = []string{TopicAll}
	}
	s.all = slices.Contains(s.topics, TopicAll)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		s.closed = true
		close(s.ch)
		return s
	}
	for _, t := range s.topics {
		h.topics[t] = append(h.topics[t], s)
	}
	h.count++
	return s
}

func (h *hub) Publish(topics []string, typ string, data any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	ev := Event{Seq: h.seq, Type: typ, TS: time.Now().UTC(), Data: data, enc: &encoded{}}

	for _, s := range h.topics[TopicAll] {
		s.send(ev)
	}
	for _, t := range topics {
		if t == TopicAll {
			continue
		}
		for _, s := range h.topics[t] {
			if s.all || s.mark == ev.Seq {
				continue
			}
			s.mark = ev.Seq
			s.send(ev)
		}
	}
}

func (s *sub) send(ev Event) {
	for {
		select {
		case s.ch <- ev:
			return
		default:
		}
		select {
		case <-s.ch:
			s.dropped.Add(1)
		default:
		}
	}
}

func (s *sub) C() <-chan Event { return s.ch }

func (s *sub) TakeDropped() int { return int(s.dropped.Swap(0)) }

func (s *sub) Close() {
	h := s.h
	h.mu.Lock()
	defer h.mu.Unlock()
	h.removeLocked(s)
}

func (h *hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for _, set := range h.topics {
		for _, s := range slices.Clone(set) {
			h.removeLocked(s)
		}
	}
}

func (h *hub) removeLocked(s *sub) {
	if s.closed {
		return
	}
	s.closed = true
	for _, t := range s.topics {
		set := h.topics[t]
		if i := slices.Index(set, s); i >= 0 {
			last := len(set) - 1
			set[i] = set[last]
			set[last] = nil
			set = set[:last]
		}
		if len(set) == 0 {
			delete(h.topics, t)
		} else {
			h.topics[t] = set
		}
	}
	h.count--
	close(s.ch)
}
