package dst

import (
	"fmt"
	"sync"

	"github.com/SigmaUno/subspace/internal/events"
)

type subscriber struct {
	id       int
	topics   []string
	sub      events.Subscription
	expected int
	received int
	dropped  int
	lastSeq  int64
	disorder string
}

type recorder struct {
	mu   sync.Mutex
	hub  events.Hub
	also events.Publisher
	open []*subscriber
	next int
}

func newRecorder(also events.Publisher) *recorder { return &recorder{hub: events.NewHub(), also: also} }

func matches(sub, topics []string) bool {
	for _, s := range sub {
		if s == events.TopicAll {
			return true
		}
		for _, t := range topics {
			if t == s && t != events.TopicAll {
				return true
			}
		}
	}
	return false
}

func (r *recorder) Publish(topics []string, typ string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hub.Publish(topics, typ, data)
	if r.also != nil {
		r.also.Publish(topics, typ, data)
	}
	for _, s := range r.open {
		if matches(s.topics, topics) {
			s.expected++
		}
	}
}

func (r *recorder) subscribe(topics []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	r.open = append(r.open, &subscriber{id: r.next, topics: topics, sub: r.hub.Subscribe(topics)})
}

func (r *recorder) pick(i int) int {
	if len(r.open) == 0 {
		return -1
	}
	return i % len(r.open)
}

func (s *subscriber) take(max int) {
	for range max {
		select {
		case ev, ok := <-s.sub.C():
			if !ok {
				s.disorder = "channel closed while subscribed"
				return
			}
			if ev.Seq <= s.lastSeq {
				s.disorder = fmt.Sprintf("seq %d after %d", ev.Seq, s.lastSeq)
			}
			s.lastSeq = ev.Seq
			s.received++
		default:
			return
		}
	}
}

func (r *recorder) drain(i, max int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i = r.pick(i); i >= 0 {
		r.open[i].take(max)
	}
}

func (r *recorder) unsubscribe(i int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i = r.pick(i); i < 0 {
		return nil
	}
	s := r.open[i]
	r.open = append(r.open[:i], r.open[i+1:]...)
	return closeSub(s)
}

func closeSub(s *subscriber) error {
	s.sub.Close()
	n := 0
	for range s.sub.C() {
		n++
	}
	if n > events.SubscriberBuffer {
		return fmt.Errorf("closed subscriber %d yielded %d buffered events", s.id, n)
	}
	return nil
}

func (r *recorder) reset() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var err error
	for _, s := range r.open {
		if cerr := closeSub(s); cerr != nil && err == nil {
			err = cerr
		}
	}
	if n := r.hub.Subscribers(); n != 0 && err == nil {
		err = fmt.Errorf("hub reports %d subscribers after closing all", n)
	}
	r.open = nil
	r.hub = events.NewHub()
	return err
}

func (r *recorder) stats() ([]SubStat, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]SubStat, 0, len(r.open))
	for _, s := range r.open {
		s.dropped += s.sub.TakeDropped()
		out = append(out, SubStat{ID: s.id, Topics: s.topics, Expected: s.expected, Received: s.received,
			Dropped: s.dropped, Pending: len(s.sub.C()), Disorder: s.disorder})
	}
	return out, r.hub.Subscribers()
}
