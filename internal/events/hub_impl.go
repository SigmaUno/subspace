package events

import (
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

const TopicAll = "all"

const SubscriberBuffer = 256

const DefaultReplay = 10000

type hub struct {
	mu     sync.Mutex
	seq    int64
	topics map[string][]*sub
	count  int
	ring   []ringEntry
	head   int
	size   int
	closed bool
}

type ringEntry struct {
	ev     Event
	topics []string
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
	return &hub{topics: make(map[string][]*sub), ring: make([]ringEntry, DefaultReplay)}
}

func (h *hub) SetReplay(n int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n = max(n, 0)
	keep := min(h.size, n)
	next := make([]ringEntry, n)
	for i := range keep {
		next[i] = h.at(h.size - keep + i)
	}
	h.ring, h.head, h.size = next, keep%max(n, 1), keep
}

func (h *hub) at(i int) ringEntry {
	return h.ring[(h.head-h.size+i+len(h.ring))%len(h.ring)]
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
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.subscribe(topics)
}

func (h *hub) subscribe(topics []string) *sub {
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

func (h *hub) Resume(topics []string, after int64) Resumed {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.subscribe(topics)
	r := Resumed{Sub: s, Seq: h.seq}
	if after > h.seq || after < h.seq-int64(h.size) {
		r.Resync = true
		return r
	}
	for i := h.size - int(h.seq-after); i < h.size; i++ {
		if e := h.at(i); s.wants(e.topics) {
			r.Replay = append(r.Replay, e.ev)
		}
	}
	return r
}

func (s *sub) wants(topics []string) bool {
	if s.all {
		return true
	}
	for _, t := range topics {
		if t != TopicAll && slices.Contains(s.topics, t) {
			return true
		}
	}
	return false
}

func (h *hub) Publish(topics []string, typ string, data any) {
	ts := time.Now().UTC()
	payload := encodePayload(typ, ts, data)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	ev := Event{Seq: h.seq, Type: typ, TS: ts, Data: data, enc: &encoded{}}
	ev.enc.once.Do(func() { ev.enc.b = frameOf(ev.Seq, typ, payload) })
	h.record(ev, topics)

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

func (h *hub) record(ev Event, topics []string) {
	if len(h.ring) == 0 {
		return
	}
	h.ring[h.head] = ringEntry{ev: ev, topics: slices.Clone(topics)}
	h.head = (h.head + 1) % len(h.ring)
	h.size = min(h.size+1, len(h.ring))
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
