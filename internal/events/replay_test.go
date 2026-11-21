package events

import (
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

func seqs(evs []Event) []int64 {
	out := make([]int64, len(evs))
	for i, e := range evs {
		out[i] = e.Seq
	}
	return out
}

func equalSeqs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestResumeReplaysMatchingEventsThenLive(t *testing.T) {
	h := NewHub().(*hub)
	h.Publish([]string{"board", "task:T-1"}, "task.updated", 1)
	h.Publish([]string{"board", "task:T-2"}, "task.updated", 2)
	h.Publish([]string{"control"}, "control.paused", 3)
	h.Publish([]string{"board", "task:T-1"}, "task.updated", 4)
	h.Publish([]string{"board", "task:T-1"}, "task.updated", 5)

	r := h.Resume([]string{"task:T-1"}, 1)
	defer r.Sub.Close()
	if r.Resync || r.Seq != 5 || !equalSeqs(seqs(r.Replay), []int64{4, 5}) {
		t.Fatalf("resume %v resync=%v seq=%d", seqs(r.Replay), r.Resync, r.Seq)
	}
	h.Publish([]string{"task:T-1"}, "task.updated", 6)
	if ev := recvN(t, r.Sub, 1)[0]; ev.Seq != 6 {
		t.Fatalf("live seq %d", ev.Seq)
	}

	all := h.Resume(nil, 3)
	defer all.Sub.Close()
	if !equalSeqs(seqs(all.Replay), []int64{4, 5, 6}) {
		t.Fatalf("all replay %v", seqs(all.Replay))
	}

	caughtUp := h.Resume([]string{"board"}, 6)
	defer caughtUp.Sub.Close()
	if caughtUp.Resync || len(caughtUp.Replay) != 0 {
		t.Fatalf("caught up %+v", caughtUp)
	}
}

func TestResumeResyncWhenBufferOverflowedOrIDUnknown(t *testing.T) {
	h := NewHub().(*hub)
	h.SetReplay(5)
	for i := 1; i <= 8; i++ {
		h.Publish([]string{"board"}, "task.updated", i)
	}
	cases := []struct {
		after  int64
		resync bool
		replay []int64
	}{
		{0, true, nil},
		{2, true, nil},
		{3, false, []int64{4, 5, 6, 7, 8}},
		{7, false, []int64{8}},
		{8, false, nil},
		{9, true, nil},
	}
	for _, c := range cases {
		r := h.Resume([]string{"board"}, c.after)
		r.Sub.Close()
		if r.Resync != c.resync || !equalSeqs(seqs(r.Replay), c.replay) {
			t.Errorf("after=%d: resync=%v replay=%v", c.after, r.Resync, seqs(r.Replay))
		}
	}
}

func TestReplayDisabledAlwaysResyncsWhenBehind(t *testing.T) {
	h := NewHub().(*hub)
	h.SetReplay(0)
	h.Publish([]string{"board"}, "task.updated", nil)
	if r := h.Resume(nil, 0); !r.Resync {
		t.Fatal("expected resync")
	} else {
		r.Sub.Close()
	}
	if r := h.Resume(nil, 1); r.Resync {
		t.Fatal("unexpected resync")
	} else {
		r.Sub.Close()
	}
}

func TestSetReplayKeepsNewest(t *testing.T) {
	h := NewHub().(*hub)
	for i := 1; i <= 6; i++ {
		h.Publish([]string{"board"}, "task.updated", i)
	}
	h.SetReplay(3)
	h.Publish([]string{"board"}, "task.updated", 7)
	r := h.Resume(nil, 4)
	defer r.Sub.Close()
	if r.Resync || !equalSeqs(seqs(r.Replay), []int64{5, 6, 7}) {
		t.Fatalf("resync=%v replay=%v", r.Resync, seqs(r.Replay))
	}
}

func TestResumeUnderConcurrentPublishLosesNothing(t *testing.T) {
	const publishers, perPublisher = 4, 60
	const total = publishers * perPublisher
	h := NewHub().(*hub)
	rng := rand.New(rand.NewSource(7))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for p := 0; p < publishers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < perPublisher; i++ {
				h.Publish([]string{"board"}, "task.updated", i)
			}
		}()
	}
	type result struct {
		after int64
		got   []int64
	}
	results := make(chan result, 16)
	for i := 0; i < 16; i++ {
		after := int64(rng.Intn(total / 2))
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for h.Seq() < after {
				time.Sleep(time.Millisecond)
			}
			r := h.Resume([]string{"board"}, after)
			defer r.Sub.Close()
			got := seqs(r.Replay)
			for len(got) == 0 || got[len(got)-1] < total {
				select {
				case ev := <-r.Sub.C():
					got = append(got, ev.Seq)
				case <-time.After(5 * time.Second):
					results <- result{after, got}
					return
				}
			}
			results <- result{after, got}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for r := range results {
		if int64(len(r.got)) != total-r.after {
			t.Fatalf("after=%d got %d events", r.after, len(r.got))
		}
		for i, s := range r.got {
			if s != r.after+int64(i)+1 {
				t.Fatalf("after=%d: position %d has seq %d", r.after, i, s)
			}
		}
	}
}

func TestSSEResumeFromHeaderAndQuery(t *testing.T) {
	h := NewHub()
	for i := 1; i <= 4; i++ {
		h.Publish([]string{"board", "task:T-" + strconv.Itoa(i%2)}, "task.updated", map[string]int{"n": i})
	}
	srv := httptest.NewServer(NewSSEHandler(h, nil, time.Hour))
	defer srv.Close()

	ch, stop := open(t, srv, "?topics=task:T-1&last_event_id=1")
	defer stop()
	if f := next(t, ch); f.event != "hello" || f.id != "" {
		t.Fatalf("hello %+v", f)
	}
	if f := next(t, ch); f.event != "task.updated" || f.id != "3" {
		t.Fatalf("first replayed %+v", f)
	}
	h.Publish([]string{"board", "task:T-1"}, "task.updated", nil)
	if f := next(t, ch); f.id != "5" {
		t.Fatalf("live %+v", f)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/events?topics=board", nil)
	req.Header.Set("Last-Event-ID", "4")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	hc := make(chan frame, 16)
	go readFrames(newScanner(resp.Body), hc)
	if f := next(t, hc); f.event != "hello" || f.id != "" {
		t.Fatalf("header hello %+v", f)
	}
	if f := next(t, hc); f.event != "task.updated" || f.id != "5" {
		t.Fatalf("header replay %+v", f)
	}
}

func TestSSEResyncAndBadID(t *testing.T) {
	h := NewHub().(*hub)
	h.SetReplay(2)
	for i := 0; i < 5; i++ {
		h.Publish([]string{"board"}, "task.updated", i)
	}
	srv := httptest.NewServer(NewSSEHandler(h, nil, time.Hour))
	defer srv.Close()

	ch, stop := open(t, srv, "?last_event_id=1")
	defer stop()
	if f := next(t, ch); f.event != "hello" || f.id != "5" {
		t.Fatalf("hello %+v", f)
	}
	if f := next(t, ch); f.event != "resync" || f.id != "5" {
		t.Fatalf("resync %+v", f)
	}
	h.Publish([]string{"board"}, "task.updated", nil)
	if f := next(t, ch); f.event != "task.updated" || f.id != "6" {
		t.Fatalf("live after resync %+v", f)
	}

	for _, bad := range []string{"abc", "-1"} {
		resp, err := srv.Client().Get(srv.URL + "/events?last_event_id=" + bad)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%q: status %d", bad, resp.StatusCode)
		}
	}
}

func TestSSEResyncAfterRestartWithLargerLastID(t *testing.T) {
	for _, published := range []int{0, 3} {
		h := NewHub()
		for i := 0; i < published; i++ {
			h.Publish([]string{"board"}, "task.updated", i)
		}
		srv := httptest.NewServer(NewSSEHandler(h, nil, time.Hour))
		defer srv.Close()

		ch, stop := open(t, srv, "?topics=board&last_event_id=40")
		hello := next(t, ch)
		resync := next(t, ch)
		want := strconv.Itoa(published)
		if hello.event != "hello" || hello.id != want || resync.event != "resync" || resync.id != want {
			t.Fatalf("published %d: hello %+v resync %+v", published, hello, resync)
		}
		h.Publish([]string{"board"}, "task.updated", nil)
		if f := next(t, ch); f.event != "task.updated" || f.id != strconv.Itoa(published+1) {
			t.Fatalf("published %d: live after resync %+v", published, f)
		}
		stop()
	}
}
