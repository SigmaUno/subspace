package events

import (
	"sync"
	"sync/atomic"
	"time"
)

const TopicAll = "all"

const SubscriberBuffer = 256

type hub struct {
	mu     sync.Mutex
	seq    int64
	topics map[string]map[*sub]struct{}
	count  int
}

type sub struct {
	h       *hub
	ch      chan Event
	topics  []string
	dropped atomic.Int64
	once    sync.Once
}

func NewHub() Hub {
	return &hub{topics: make(map[string]map[*sub]struct{})}
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
	seen := make(map[string]struct{}, len(topics))
	for _, t := range topics {
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		s.topics = append(s.topics, t)
	}
	if len(s.topics) == 0 {
		s.topics = []string{TopicAll}
	}
	h.mu.Lock()
	for _, t := range s.topics {
		set := h.topics[t]
		if set == nil {
			set = make(map[*sub]struct{})
			h.topics[t] = set
		}
		set[s] = struct{}{}
	}
	h.count++
	h.mu.Unlock()
	return s
}

func (h *hub) Publish(topics []string, typ string, data any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	ev := Event{Seq: h.seq, Type: typ, TS: time.Now().UTC(), Data: data, enc: &encoded{}}

	var delivered map[*sub]struct{}
	if len(topics) > 0 {
		delivered = make(map[*sub]struct{})
	}
	for s := range h.topics[TopicAll] {
		s.send(ev)
		if delivered != nil {
			delivered[s] = struct{}{}
		}
	}
	for _, t := range topics {
		if t == TopicAll {
			continue
		}
		for s := range h.topics[t] {
			if _, ok := delivered[s]; ok {
				continue
			}
			delivered[s] = struct{}{}
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
	s.once.Do(func() {
		h := s.h
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, t := range s.topics {
			if set := h.topics[t]; set != nil {
				delete(set, s)
				if len(set) == 0 {
					delete(h.topics, t)
				}
			}
		}
		h.count--
		close(s.ch)
	})
}
