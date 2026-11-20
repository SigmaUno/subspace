package events

import (
	"sync"
	"testing"
	"time"
)

func recvN(t *testing.T, s Subscription, n int) []Event {
	t.Helper()
	out := make([]Event, 0, n)
	timeout := time.After(5 * time.Second)
	for len(out) < n {
		select {
		case ev, ok := <-s.C():
			if !ok {
				t.Fatalf("channel closed after %d events", len(out))
			}
			out = append(out, ev)
		case <-timeout:
			t.Fatalf("timeout after %d/%d events", len(out), n)
		}
	}
	return out
}

func assertEmpty(t *testing.T, s Subscription) {
	t.Helper()
	select {
	case ev := <-s.C():
		t.Fatalf("unexpected event %+v", ev)
	default:
	}
}

func TestTopicFiltering(t *testing.T) {
	h := NewHub()
	all := h.Subscribe([]string{"all"})
	def := h.Subscribe(nil)
	board := h.Subscribe([]string{"board"})
	task := h.Subscribe([]string{"task:T-1", "board"})
	other := h.Subscribe([]string{"task:T-2"})
	defer all.Close()
	defer def.Close()
	defer board.Close()
	defer task.Close()
	defer other.Close()
	if h.Subscribers() != 5 {
		t.Fatalf("subscribers = %d", h.Subscribers())
	}

	h.Publish([]string{"board", "brief:B-1", "task:T-1"}, "task.updated", map[string]string{"id": "T-1"})
	h.Publish([]string{"control"}, "control.paused", nil)

	evs := recvN(t, all, 2)
	if evs[0].Type != "task.updated" || evs[1].Type != "control.paused" || evs[0].Seq >= evs[1].Seq {
		t.Fatalf("all got %+v", evs)
	}
	recvN(t, def, 2)
	if ev := recvN(t, board, 1)[0]; ev.Type != "task.updated" {
		t.Fatalf("board got %+v", ev)
	}
	// matched twice (board + task:T-1) but delivered once
	if ev := recvN(t, task, 1)[0]; ev.Type != "task.updated" {
		t.Fatalf("task got %+v", ev)
	}
	assertEmpty(t, board)
	assertEmpty(t, task)
	assertEmpty(t, other)
}

func TestCloseUnsubscribes(t *testing.T) {
	h := NewHub()
	s := h.Subscribe([]string{"board"})
	s.Close()
	s.Close() // idempotent
	if h.Subscribers() != 0 {
		t.Fatalf("subscribers = %d", h.Subscribers())
	}
	if _, ok := <-s.C(); ok {
		t.Fatal("channel not closed")
	}
	h.Publish([]string{"board"}, "x", nil) // must not panic
}

func TestSlowSubscriberDropsOldest(t *testing.T) {
	h := NewHub()
	slow := h.Subscribe([]string{"board"})
	defer slow.Close()
	const n = SubscriberBuffer + 44

	done := make(chan struct{})
	go func() {
		for i := 0; i < n; i++ {
			h.Publish([]string{"board"}, "task.updated", i)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publisher blocked by slow subscriber")
	}
	if d := slow.TakeDropped(); d != n-SubscriberBuffer {
		t.Fatalf("dropped = %d, want %d", d, n-SubscriberBuffer)
	}
	if d := slow.TakeDropped(); d != 0 {
		t.Fatalf("dropped not reset: %d", d)
	}
	evs := recvN(t, slow, SubscriberBuffer)
	if first := evs[0].Data.(int); first != n-SubscriberBuffer {
		t.Fatalf("oldest kept = %d, want %d", first, n-SubscriberBuffer)
	}
	for i := 1; i < len(evs); i++ {
		if evs[i].Seq != evs[i-1].Seq+1 {
			t.Fatalf("non-contiguous seq at %d", i)
		}
	}
}

func TestManySubscribersConcurrent(t *testing.T) {
	h := NewHub()
	const (
		subs       = 500
		publishers = 8
		perPub     = 100
		total      = publishers * perPub
	)
	// one slow subscriber that never reads
	slow := h.Subscribe([]string{"all"})
	defer slow.Close()

	var wg sync.WaitGroup
	ready := make(chan struct{})
	counts := make([]int, subs)
	for i := 0; i < subs; i++ {
		topics := []string{"board"}
		if i%2 == 1 {
			topics = []string{"all"}
		}
		s := h.Subscribe(topics)
		wg.Add(1)
		go func(i int, s Subscription) {
			defer wg.Done()
			defer s.Close()
			<-ready
			var last int64
			for counts[i]+s.TakeDropped() < total {
				select {
				case ev := <-s.C():
					if ev.Seq <= last {
						t.Errorf("sub %d: seq %d after %d", i, ev.Seq, last)
						return
					}
					last = ev.Seq
					counts[i]++
				case <-time.After(10 * time.Second):
					t.Errorf("sub %d: timeout at %d", i, counts[i])
					return
				}
			}
		}(i, s)
	}
	if got := h.Subscribers(); got != subs+1 {
		t.Fatalf("subscribers = %d", got)
	}
	close(ready)

	var pw sync.WaitGroup
	for p := 0; p < publishers; p++ {
		pw.Add(1)
		go func() {
			defer pw.Done()
			for i := 0; i < perPub; i++ {
				h.Publish([]string{"board", "task:T-1"}, "task.updated", i)
			}
		}()
	}
	pw.Wait()
	wg.Wait()
	if d := slow.TakeDropped(); d != total-SubscriberBuffer {
		t.Fatalf("slow dropped = %d, want %d", d, total-SubscriberBuffer)
	}
	if got := h.Subscribers(); got != 1 {
		t.Fatalf("subscribers after close = %d", got)
	}
	if s := h.(interface{ Seq() int64 }).Seq(); s != total {
		t.Fatalf("seq = %d, want %d", s, total)
	}
}
