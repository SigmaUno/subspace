package events

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"
)

var topicPool = []string{"board", "agents", "questions", "brief:B-1", "task:T-1", "thread:task:T-1"}

func randomTopics(r *rand.Rand, withAll bool) []string {
	var out []string
	for i := r.Intn(4); i > 0; i-- {
		out = append(out, topicPool[r.Intn(len(topicPool))])
	}
	if withAll && r.Intn(6) == 0 {
		out = append(out, TopicAll)
	}
	return out
}

func wants(subTopics, eventTopics []string) bool {
	if len(subTopics) == 0 {
		return true
	}
	for _, s := range subTopics {
		if s == TopicAll {
			return true
		}
		for _, e := range eventTopics {
			if s == e {
				return true
			}
		}
	}
	return false
}

func drain(s Subscription) []Event {
	var out []Event
	for {
		select {
		case ev := <-s.C():
			out = append(out, ev)
		default:
			return out
		}
	}
}

func TestEachSubscriberGetsMatchingEventsOnceInOrder(t *testing.T) {
	for seed := int64(1); seed <= 300; seed++ {
		r := rand.New(rand.NewSource(seed))
		h := NewHub()
		subTopics := make([][]string, 1+r.Intn(8))
		subs := make([]Subscription, len(subTopics))
		for i := range subs {
			subTopics[i] = randomTopics(r, true)
			subs[i] = h.Subscribe(subTopics[i])
		}

		published := make([][]string, 100+r.Intn(100))
		for i := range published {
			published[i] = randomTopics(r, false)
			h.Publish(published[i], "test", i)
		}

		for i, s := range subs {
			var want []int
			for n, topics := range published {
				if wants(subTopics[i], topics) {
					want = append(want, n)
				}
			}
			got := drain(s)
			if len(got) != len(want) {
				t.Fatalf("seed %d: subscriber %v got %d events, want %d", seed, subTopics[i], len(got), len(want))
			}
			var last int64
			for k, ev := range got {
				if ev.Seq <= last {
					t.Fatalf("seed %d: subscriber %v saw seq %d after %d", seed, subTopics[i], ev.Seq, last)
				}
				last = ev.Seq
				if ev.Data.(int) != want[k] {
					t.Fatalf("seed %d: subscriber %v event %d carried %v, want %d", seed, subTopics[i], k, ev.Data, want[k])
				}
			}
			s.Close()
		}
		if n := h.Subscribers(); n != 0 {
			t.Fatalf("seed %d: %d subscribers left after Close", seed, n)
		}
	}
}

func TestConcurrentPublishersKeepOrderAndAccounting(t *testing.T) {
	for seed := int64(1); seed <= 40; seed++ {
		r := rand.New(rand.NewSource(seed))
		h := NewHub()
		subTopics := make([][]string, 1+r.Intn(6))
		subs := make([]Subscription, len(subTopics))
		for i := range subs {
			subTopics[i] = randomTopics(r, true)
			subs[i] = h.Subscribe(subTopics[i])
		}

		publishers := 2 + r.Intn(4)
		published := make([][][]string, publishers)
		for p := range published {
			published[p] = make([][]string, SubscriberBuffer/2+r.Intn(SubscriberBuffer))
			for i := range published[p] {
				published[p][i] = randomTopics(r, false)
			}
		}

		var wg sync.WaitGroup
		for p := range published {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i, topics := range published[p] {
					h.Publish(topics, "test", fmt.Sprintf("%d/%d", p, i))
				}
			}()
		}
		wg.Wait()

		for i, s := range subs {
			matching := 0
			for _, events := range published {
				for _, topics := range events {
					if wants(subTopics[i], topics) {
						matching++
					}
				}
			}
			got := drain(s)
			if len(got)+s.TakeDropped() != matching {
				t.Fatalf("seed %d: subscriber %v received %d + dropped != %d matching", seed, subTopics[i], len(got), matching)
			}
			seen := map[string]bool{}
			var last int64
			for _, ev := range got {
				id := ev.Data.(string)
				if ev.Seq <= last || seen[id] {
					t.Fatalf("seed %d: subscriber %v saw seq %d after %d (event %s, duplicate=%v)", seed, subTopics[i], ev.Seq, last, id, seen[id])
				}
				last = ev.Seq
				seen[id] = true
			}
			s.Close()
		}
	}
}
