package events

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

const (
	soakPublishers   = 50
	soakPerPublisher = 4000
	soakSubscribers  = 1000
	soakSlow         = 20
	soakChurn        = 100
	soakTasks        = 48
	soakBriefs       = 8
	soakBoardBit     = soakTasks + soakBriefs
	soakControlBit   = soakBoardBit + 1
	soakAllMask      = ^uint64(0)
)

type soakPayload struct {
	Pub, N int
}

func soakEventMask(pub, n int) uint64 {
	m := uint64(1) << ((pub + n) % soakTasks)
	m |= 1 << (soakTasks + n%soakBriefs)
	if n%8 == 0 {
		m |= 1 << soakBoardBit
	}
	if n%1000 == 0 {
		m |= 1 << soakControlBit
	}
	return m
}

func soakBitName(bit int) string {
	switch {
	case bit < soakTasks:
		return fmt.Sprintf("task:T-%d", bit)
	case bit < soakBoardBit:
		return fmt.Sprintf("brief:B-%d", bit-soakTasks)
	case bit == soakBoardBit:
		return "board"
	default:
		return "control"
	}
}

func soakNames(mask uint64) []string {
	var out []string
	for bit := 0; bit <= soakControlBit; bit++ {
		if mask&(1<<bit) != 0 {
			out = append(out, soakBitName(bit))
		}
	}
	return out
}

func soakSubMask(i int) uint64 {
	if i%25 == 24 {
		return soakAllMask
	}
	switch i % 4 {
	case 0:
		return 1 << soakBoardBit
	case 1:
		return 1 << ((i / 4) % soakTasks)
	case 2:
		return 1<<(soakTasks+(i/4)%soakBriefs) | 1<<soakControlBit
	default:
		return 1<<(i%soakTasks) | 1<<(soakTasks+i%soakBriefs)
	}
}

func soakSubTopics(i int) []string {
	if i%25 == 24 {
		return []string{TopicAll}
	}
	return soakNames(soakSubMask(i))
}

func soakExpected(hist map[uint64]int, mask uint64) int {
	n := 0
	for m, c := range hist {
		if m&mask != 0 {
			n += c
		}
	}
	return n
}

func soakDrain(ctx context.Context, t *testing.T, s Subscription, mask uint64, want int) (got, dropped int) {
	t.Helper()
	var lastSeq int64
	lastN := make([]int, soakPublishers)
	for i := range lastN {
		lastN[i] = -1
	}
	for got+dropped < want {
		dropped += s.TakeDropped()
		if got+dropped >= want {
			break
		}
		select {
		case ev := <-s.C():
			p, ok := ev.Data.(soakPayload)
			if !ok {
				t.Errorf("unexpected payload %T", ev.Data)
				return
			}
			if ev.Seq <= lastSeq {
				t.Errorf("seq %d after %d", ev.Seq, lastSeq)
				return
			}
			if p.N <= lastN[p.Pub] {
				t.Errorf("publisher %d: event %d after %d", p.Pub, p.N, lastN[p.Pub])
				return
			}
			if soakEventMask(p.Pub, p.N)&mask == 0 {
				t.Errorf("event %d/%d does not match filter", p.Pub, p.N)
				return
			}
			lastSeq = ev.Seq
			lastN[p.Pub] = p.N
			got++
		case <-ctx.Done():
			t.Errorf("timeout: received %d, dropped %d, want %d", got, dropped, want)
			return
		}
	}
	return got, dropped
}

func TestSoakFanout(t *testing.T) {
	if testing.Short() {
		t.Skip("soak test")
	}
	baseline := runtime.NumGoroutine()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	hist := make(map[uint64]int)
	for p := 0; p < soakPublishers; p++ {
		for n := 0; n < soakPerPublisher; n++ {
			hist[soakEventMask(p, n)]++
		}
	}

	h := NewHub()
	subs := make([]Subscription, soakSubscribers)
	for i := range subs {
		subs[i] = h.Subscribe(soakSubTopics(i))
	}
	if got := h.Subscribers(); got != soakSubscribers {
		t.Fatalf("subscribers = %d", got)
	}

	isSlow := func(i int) bool { return i < soakSlow }
	isChurn := func(i int) bool { return i >= soakSlow && i < soakSlow+soakChurn }

	got := make([]int, soakSubscribers)
	dropped := make([]int, soakSubscribers)
	want := make([]int, soakSubscribers)
	publishDone := make(chan struct{})
	halfway := make(chan struct{})
	var readers sync.WaitGroup

	for i, s := range subs {
		if isChurn(i) {
			readers.Add(1)
			go func() {
				defer readers.Done()
				for range s.C() {
				}
			}()
			continue
		}
		want[i] = soakExpected(hist, soakSubMask(i))
		readers.Add(1)
		go func() {
			defer readers.Done()
			if isSlow(i) {
				<-publishDone
			}
			got[i], dropped[i] = soakDrain(ctx, t, s, soakSubMask(i), want[i])
		}()
	}

	readers.Add(1)
	go func() {
		defer readers.Done()
		<-halfway
		for _, s := range subs[soakSlow : soakSlow+soakChurn] {
			s.Close()
		}
	}()

	var pw sync.WaitGroup
	for p := 0; p < soakPublishers; p++ {
		pw.Add(1)
		go func() {
			defer pw.Done()
			for n := 0; n < soakPerPublisher; n++ {
				h.Publish(soakNames(soakEventMask(p, n)), "task.updated", soakPayload{p, n})
				if p == 0 && n == soakPerPublisher/2 {
					close(halfway)
				}
			}
		}()
	}
	go func() {
		pw.Wait()
		close(publishDone)
	}()

	select {
	case <-publishDone:
	case <-time.After(5 * time.Minute):
		t.Fatal("publishers did not finish in bounded time")
	}
	readers.Wait()

	for i, s := range subs {
		if isChurn(i) {
			continue
		}
		if got[i]+dropped[i] != want[i] {
			t.Errorf("sub %d: received %d + dropped %d != %d", i, got[i], dropped[i], want[i])
		}
		if isSlow(i) && (got[i] > SubscriberBuffer || dropped[i] == 0) {
			t.Errorf("slow sub %d: received %d, dropped %d", i, got[i], dropped[i])
		}
		s.Close()
	}
	if got := h.Subscribers(); got != 0 {
		t.Fatalf("subscribers after close = %d", got)
	}

	settle := time.Now().Add(10 * time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(settle) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > baseline {
		t.Fatalf("goroutines = %d, baseline %d", n, baseline)
	}
}
