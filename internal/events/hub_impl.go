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
	queue  []pending
	spare  []pending

	dmu       sync.Mutex
	delivered int64
	touched   []*sub
}

type pending struct {
	ev     Event
	topics []string
	subs   map[string][]*sub
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
	removed bool
	closed  bool
	out     []Event
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
		s.removed, s.closed = true, true
		close(s.ch)
		return s
	}
	next := h.cloneTopics()
	for _, t := range s.topics {
		next[t] = append(slices.Clip(next[t]), s)
	}
	h.topics = next
	h.count++
	return s
}

func (h *hub) cloneTopics() map[string][]*sub {
	next := make(map[string][]*sub, len(h.topics)+1)
	for t, set := range h.topics {
		next[t] = set
	}
	return next
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
	h.seq++
	ev := Event{Seq: h.seq, Type: typ, TS: ts, Data: data, enc: &encoded{}}
	ev.enc.once.Do(func() { ev.enc.b = frameOf(ev.Seq, typ, payload) })
	h.record(ev, topics)
	if !h.closed {
		h.queue = append(h.queue, pending{ev: ev, topics: topics, subs: h.topics})
	}
	seq := h.seq
	h.mu.Unlock()
	h.dmu.Lock()
	if h.delivered < seq {
		h.deliver()
	}
	h.dmu.Unlock()
}

func (h *hub) deliver() {
	h.mu.Lock()
	batch := h.queue
	h.queue, h.spare = h.spare, nil
	h.mu.Unlock()
	touched := h.touched[:0]
	for i := range batch {
		touched = collect(&batch[i], touched)
		h.delivered = batch[i].ev.Seq
		batch[i] = pending{}
	}
	for i, s := range touched {
		for j := range s.out {
			s.send(s.out[j])
			s.out[j] = Event{}
		}
		s.out = s.out[:0]
		if cap(s.out) > SubscriberBuffer {
			s.out = nil
		}
		touched[i] = nil
	}
	h.touched = touched[:0]
	h.mu.Lock()
	if h.spare == nil {
		h.spare = batch[:0]
	}
	h.mu.Unlock()
}

func collect(p *pending, touched []*sub) []*sub {
	add := func(s *sub) {
		if len(s.out) == 0 {
			touched = append(touched, s)
		}
		s.out = append(s.out, p.ev)
	}
	for _, s := range p.subs[TopicAll] {
		add(s)
	}
	for _, t := range p.topics {
		if t == TopicAll {
			continue
		}
		for _, s := range p.subs[t] {
			if s.all || s.mark == p.ev.Seq {
				continue
			}
			s.mark = p.ev.Seq
			add(s)
		}
	}
	return touched
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
	if s.closed {
		return
	}
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
	removed := h.removeLocked(s)
	h.mu.Unlock()
	if removed {
		h.closeSubs([]*sub{s})
	}
}

func (h *hub) Close() {
	h.mu.Lock()
	h.closed = true
	var all []*sub
	seen := map[*sub]bool{}
	for _, set := range h.topics {
		for _, s := range set {
			if !seen[s] {
				seen[s] = true
				s.removed = true
				all = append(all, s)
			}
		}
	}
	h.topics = map[string][]*sub{}
	h.count = 0
	h.mu.Unlock()
	h.closeSubs(all)
}

func (h *hub) closeSubs(subs []*sub) {
	h.dmu.Lock()
	defer h.dmu.Unlock()
	for _, s := range subs {
		if !s.closed {
			s.closed = true
			close(s.ch)
		}
	}
}

func (h *hub) removeLocked(s *sub) bool {
	if s.removed {
		return false
	}
	s.removed = true
	next := h.cloneTopics()
	for _, t := range s.topics {
		set := slices.DeleteFunc(slices.Clone(next[t]), func(x *sub) bool { return x == s })
		if len(set) == 0 {
			delete(next, t)
		} else {
			next[t] = set
		}
	}
	h.topics = next
	h.count--
	return true
}
