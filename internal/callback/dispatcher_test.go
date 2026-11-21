package callback

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var t0 = time.Unix(1_800_000_000, 0).UTC()

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []waiter
}

type waiter struct {
	at time.Time
	ch chan time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) At(t time.Time) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if !t.After(c.now) {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, waiter{t, ch})
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	keep := c.waiters[:0]
	for _, w := range c.waiters {
		if w.at.After(c.now) {
			keep = append(keep, w)
			continue
		}
		w.ch <- c.now
	}
	c.waiters = keep
}

func (c *fakeClock) waiting(at time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, w := range c.waiters {
		if w.at.Equal(at) {
			return true
		}
	}
	return false
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

type fakeTargets map[string]Target

func (f fakeTargets) Lookup(_ context.Context, id string) (Target, bool, error) {
	t, ok := f[id]
	return t, ok, nil
}

type request struct {
	header http.Header
	body   []byte
	event  Event
}

type receiver struct {
	srv    *httptest.Server
	got    chan request
	status func(n int) int
	hold   chan struct{}
	n      atomic.Int32
}

func newReceiver(t *testing.T, status func(n int) int) *receiver {
	t.Helper()
	r := &receiver{got: make(chan request, 64), status: status}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var ev Event
		_ = json.Unmarshal(body, &ev)
		n := int(r.n.Add(1))
		r.got <- request{req.Header.Clone(), body, ev}
		if r.hold != nil && n == 1 {
			<-r.hold
		}
		w.WriteHeader(r.status(n))
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) next(t *testing.T) request {
	t.Helper()
	select {
	case q := <-r.got:
		return q
	case <-time.After(10 * time.Second):
		t.Fatal("no request received")
		return request{}
	}
}

func (r *receiver) none(t *testing.T) {
	t.Helper()
	select {
	case q := <-r.got:
		t.Fatalf("unexpected request %s", q.body)
	case <-time.After(30 * time.Millisecond):
	}
}

type rig struct {
	d   *Dispatcher
	clk *fakeClock
}

func newRig(t *testing.T, targets Targets, mod func(*Options)) *rig {
	t.Helper()
	clk := &fakeClock{now: t0}
	var ids atomic.Int32
	o := Options{
		Targets:     targets,
		Client:      Policy{AllowPrivate: true}.NewClient(10 * time.Second),
		Clock:       clk,
		Coalesce:    time.Second,
		Backoff:     time.Second,
		MaxBackoff:  time.Minute,
		MaxAttempts: 4,
		Jitter:      func() float64 { return 0.5 },
		NewID:       func() string { return fmt.Sprintf("cb_%d", ids.Add(1)) },
	}
	if mod != nil {
		mod(&o)
	}
	d := NewDispatcher(o)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return &rig{d, clk}
}

func (g *rig) fire(t *testing.T, at time.Duration) {
	t.Helper()
	due := g.clk.Now().Add(at)
	eventually(t, "timer at "+due.String(), func() bool { return g.clk.waiting(due) })
	g.clk.Advance(at)
}

func TestCoalescesBurstsIntoOneSignedEventPerAgent(t *testing.T) {
	rcv := newReceiver(t, func(int) int { return 204 })
	targets := fakeTargets{
		"w1": {URL: rcv.srv.URL, Secret: "sscb_w1"},
		"w2": {URL: rcv.srv.URL, Secret: "sscb_w2"},
	}
	g := newRig(t, targets, nil)
	ctx := context.Background()
	g.d.Enqueue(ctx, "w1", Reason{Type: ReasonAssigned, TaskID: "T-1"})
	g.d.Enqueue(ctx, "w1", Reason{Type: ReasonAssigned, TaskID: "T-1"})
	g.d.Enqueue(ctx, "w1", Reason{Type: ReasonMessage, Thread: "agent:w1"})
	g.d.Enqueue(ctx, "w2", Reason{Type: ReasonAnswer, QuestionID: "Q-1"})
	g.d.Enqueue(ctx, "ghost", Reason{Type: ReasonAssigned, TaskID: "T-9"})
	g.fire(t, time.Second)

	byAgent := map[string]request{}
	for range 2 {
		q := rcv.next(t)
		byAgent[q.event.AgentID] = q
	}
	rcv.none(t)
	w1 := byAgent["w1"]
	if len(w1.event.Reasons) != 2 || w1.event.Type != EventWake || w1.event.ID == "" {
		t.Fatalf("w1 event %+v", w1.event)
	}
	ts, _ := strconv.ParseInt(w1.header.Get(HeaderTimestamp), 10, 64)
	if err := Verify("sscb_w1", ts, w1.body, w1.header.Get(HeaderSignature), g.clk.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := Verify("sscb_w2", ts, w1.body, w1.header.Get(HeaderSignature), g.clk.Now(), time.Minute); err == nil {
		t.Fatal("signature verified with another agent's secret")
	}
	if w1.header.Get(HeaderEventID) != w1.event.ID || w1.header.Get(HeaderEvent) != EventWake {
		t.Fatalf("headers %v", w1.header)
	}
	var generic map[string]any
	_ = json.Unmarshal(w1.body, &generic)
	for k := range generic {
		switch k {
		case "id", "type", "agent_id", "created_at", "reasons":
		default:
			t.Errorf("unexpected field %q in payload", k)
		}
	}
	eventually(t, "stats", func() bool { s := g.d.Stats(); return s.Sent == 2 && s.Pending == 0 })
}

func TestRetriesWithBackoffKeepingTheEventID(t *testing.T) {
	rcv := newReceiver(t, func(n int) int {
		if n < 3 {
			return 503
		}
		return 200
	})
	g := newRig(t, fakeTargets{"w1": {URL: rcv.srv.URL, Secret: "s"}}, nil)
	g.d.Enqueue(context.Background(), "w1", Reason{Type: ReasonAssigned, TaskID: "T-1"})

	g.fire(t, time.Second)
	first := rcv.next(t)
	g.fire(t, time.Second)
	second := rcv.next(t)
	rcv.none(t)
	g.fire(t, 2*time.Second)
	third := rcv.next(t)

	if first.event.ID != second.event.ID || second.event.ID != third.event.ID {
		t.Fatalf("event ids differ: %s %s %s", first.event.ID, second.event.ID, third.event.ID)
	}
	for i, q := range []request{first, second, third} {
		want := t0.Add([]time.Duration{time.Second, 2 * time.Second, 4 * time.Second}[i]).Unix()
		if got, _ := strconv.ParseInt(q.header.Get(HeaderTimestamp), 10, 64); got != want {
			t.Errorf("attempt %d at %d, want %d", i+1, got, want)
		}
	}
	eventually(t, "sent", func() bool { s := g.d.Stats(); return s.Sent == 1 && s.Failed == 0 && s.Pending == 0 })
}

func TestPermanentFailureIsNotRetriedAndExhaustionDrops(t *testing.T) {
	rcv := newReceiver(t, func(int) int { return 400 })
	g := newRig(t, fakeTargets{"w1": {URL: rcv.srv.URL, Secret: "s"}}, nil)
	g.d.Enqueue(context.Background(), "w1", Reason{Type: ReasonAssigned, TaskID: "T-1"})
	g.fire(t, time.Second)
	rcv.next(t)
	eventually(t, "failed", func() bool { s := g.d.Stats(); return s.Failed == 1 && s.Pending == 0 })
	g.clk.Advance(time.Hour)
	rcv.none(t)

	flaky := newReceiver(t, func(int) int { return 500 })
	g2 := newRig(t, fakeTargets{"w1": {URL: flaky.srv.URL, Secret: "s"}}, func(o *Options) { o.MaxAttempts = 3 })
	g2.d.Enqueue(context.Background(), "w1", Reason{Type: ReasonAssigned, TaskID: "T-1"})
	g2.fire(t, time.Second)
	flaky.next(t)
	g2.fire(t, time.Second)
	flaky.next(t)
	g2.fire(t, 2*time.Second)
	flaky.next(t)
	eventually(t, "exhausted", func() bool { s := g2.d.Stats(); return s.Failed == 1 && s.Sent == 0 && s.Pending == 0 })
	g2.clk.Advance(time.Hour)
	flaky.none(t)
}

func TestUnreachableReceiverNeverBlocksEnqueue(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	g := newRig(t, fakeTargets{"w1": {URL: url, Secret: "s"}}, nil)
	done := make(chan struct{})
	go func() {
		for i := range 500 {
			g.d.Enqueue(context.Background(), "w1", Reason{Type: ReasonAssigned, TaskID: fmt.Sprint("T-", i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Enqueue blocked")
	}
	g.fire(t, time.Second)
	eventually(t, "retry scheduled", func() bool { return g.d.Stats().Pending == 1 })
	if s := g.d.Stats(); s.Pending != 1 {
		t.Fatalf("stats %+v", s)
	}
}

func TestQueueIsBounded(t *testing.T) {
	targets := fakeTargets{"a": {URL: "http://x", Secret: "s"}, "b": {URL: "http://x", Secret: "s"}, "c": {URL: "http://x", Secret: "s"}}
	d := NewDispatcher(Options{Targets: targets, Clock: &fakeClock{now: t0}, QueueSize: 2})
	ctx := context.Background()
	d.Enqueue(ctx, "a", Reason{Type: ReasonAssigned, TaskID: "T-1"})
	d.Enqueue(ctx, "b", Reason{Type: ReasonAssigned, TaskID: "T-2"})
	d.Enqueue(ctx, "c", Reason{Type: ReasonAssigned, TaskID: "T-3"})
	d.Enqueue(ctx, "a", Reason{Type: ReasonAssigned, TaskID: "T-4"})
	if s := d.Stats(); s.Pending != 2 || s.Dropped != 1 {
		t.Fatalf("stats %+v", s)
	}
}

func TestEventsDuringDeliveryWaitForItAndAreNotLost(t *testing.T) {
	rcv := newReceiver(t, func(int) int { return 200 })
	rcv.hold = make(chan struct{})
	g := newRig(t, fakeTargets{"w1": {URL: rcv.srv.URL, Secret: "s"}}, nil)
	ctx := context.Background()
	g.d.Enqueue(ctx, "w1", Reason{Type: ReasonAssigned, TaskID: "T-1"})
	g.fire(t, time.Second)
	first := rcv.next(t)

	g.d.Enqueue(ctx, "w1", Reason{Type: ReasonChangesRequested, TaskID: "T-2"})
	g.clk.Advance(time.Hour)
	rcv.none(t)
	close(rcv.hold)

	eventually(t, "second delivery", func() bool { return rcv.n.Load() == 2 })
	second := rcv.next(t)
	if second.event.ID == first.event.ID || len(second.event.Reasons) != 1 || second.event.Reasons[0].TaskID != "T-2" {
		t.Fatalf("second %+v first %+v", second.event, first.event)
	}
}

type revocable struct {
	fakeTargets
	revoked atomic.Bool
}

func (r *revocable) Lookup(ctx context.Context, id string) (Target, bool, error) {
	if r.revoked.Load() {
		return Target{}, false, nil
	}
	return r.fakeTargets.Lookup(ctx, id)
}

func TestRevokedOrClearedTargetIsNotContacted(t *testing.T) {
	rcv := newReceiver(t, func(int) int { return 200 })
	targets := &revocable{fakeTargets: fakeTargets{"w1": {URL: rcv.srv.URL, Secret: "s"}}}
	g := newRig(t, targets, nil)
	g.d.Enqueue(context.Background(), "w1", Reason{Type: ReasonAssigned, TaskID: "T-1"})
	targets.revoked.Store(true)
	g.fire(t, time.Second)
	rcv.none(t)
	eventually(t, "drained", func() bool { return g.d.Stats().Pending == 0 })
}
