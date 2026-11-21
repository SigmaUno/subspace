package digest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/notify"
)

type memKV struct {
	mu sync.Mutex
	m  map[string]string
}

func (k *memKV) Get(_ context.Context, key string) (string, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.m[key]
	return v, ok, nil
}

func (k *memKV) Set(_ context.Context, key, value string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.m == nil {
		k.m = map[string]string{}
	}
	k.m[key] = value
	return nil
}

type fakeSource struct {
	mu    sync.Mutex
	snap  Snapshot
	err   error
	calls []window
}

type window struct{ since, until time.Time }

func (f *fakeSource) set(s Snapshot, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snap, f.err = s, err
}

func (f *fakeSource) Snapshot(_ context.Context, since, until time.Time) (Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, window{since, until})
	s := f.snap
	s.Since, s.Until = since, until
	return s, f.err
}

type fakeSink struct {
	mu   sync.Mutex
	sent []notify.Notification
}

func (f *fakeSink) Publish(n notify.Notification) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, n)
}

func (f *fakeSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []fakeTimer
}

type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.timers = append(c.timers, fakeTimer{c.now.Add(d), ch})
	return ch
}

func (c *fakeClock) waiters() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

func (c *fakeClock) advance(to time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = to
	keep := c.timers[:0]
	for _, t := range c.timers {
		if !t.at.After(to) {
			t.ch <- to
		} else {
			keep = append(keep, t)
		}
	}
	c.timers = keep
}

func newScheduler(rules ...Rule) (*Scheduler, *fakeSource, *fakeSink, *memKV) {
	src, sink, kv := &fakeSource{}, &fakeSink{}, &memKV{}
	return NewScheduler(Options{Rules: rules, Source: src, Sink: sink, KV: kv}), src, sink, kv
}

func TestStepFiresOncePerSlotWithContiguousWindows(t *testing.T) {
	ctx := context.Background()
	s, src, sink, _ := newScheduler(daily(time.UTC, "08:00", "18:00"))
	src.set(Snapshot{Done: 2}, nil)

	s.Step(ctx, utc(2026, 11, 21, 7, 0))
	s.Step(ctx, utc(2026, 11, 21, 7, 30))
	if sink.count() != 0 || len(src.calls) != 0 {
		t.Fatalf("baseline must not send: %d sent, %d snapshots", sink.count(), len(src.calls))
	}

	s.Step(ctx, utc(2026, 11, 21, 8, 0))
	s.Step(ctx, utc(2026, 11, 21, 8, 0))
	s.Step(ctx, utc(2026, 11, 21, 12, 0))
	if sink.count() != 1 {
		t.Fatalf("08:00 slot sent %d digests", sink.count())
	}
	s.Step(ctx, utc(2026, 11, 21, 18, 0))
	s.Step(ctx, utc(2026, 11, 21, 23, 0))
	if sink.count() != 2 {
		t.Fatalf("18:00 slot: %d digests", sink.count())
	}

	want := []window{
		{utc(2026, 11, 20, 18, 0), utc(2026, 11, 21, 8, 0)},
		{utc(2026, 11, 21, 8, 0), utc(2026, 11, 21, 18, 0)},
	}
	for i, w := range want {
		if !src.calls[i].since.Equal(w.since) || !src.calls[i].until.Equal(w.until) {
			t.Errorf("window %d = %v, want %v", i, src.calls[i], w)
		}
	}
	if id, want := sink.sent[0].ID, fmt.Sprintf("digest:daily:%d", utc(2026, 11, 21, 8, 0).Unix()); id != want {
		t.Errorf("notification id %q, want %q", id, want)
	}
}

func TestStepSkipsUnchangedAndResumesOnChange(t *testing.T) {
	ctx := context.Background()
	s, src, sink, _ := newScheduler(daily(time.UTC, "08:00"))
	s.Step(ctx, utc(2026, 11, 21, 9, 0))

	pending := Snapshot{Decisions: []Pending{{ID: "q:Q-1", Title: "Which?"}}}
	src.set(pending, nil)
	s.Step(ctx, utc(2026, 11, 22, 8, 0))
	if sink.count() != 1 {
		t.Fatalf("new decision must be sent, got %d", sink.count())
	}
	s.Step(ctx, utc(2026, 11, 23, 8, 0))
	s.Step(ctx, utc(2026, 11, 24, 8, 0))
	if sink.count() != 1 {
		t.Fatalf("unchanged state must be skipped, got %d", sink.count())
	}

	pending.Done = 1
	src.set(pending, nil)
	s.Step(ctx, utc(2026, 11, 25, 8, 0))
	if sink.count() != 2 {
		t.Fatalf("activity must be sent, got %d", sink.count())
	}

	src.set(Snapshot{}, nil)
	s.Step(ctx, utc(2026, 11, 26, 8, 0))
	if sink.count() != 3 {
		t.Fatalf("cleared state must be sent once, got %d", sink.count())
	}
	s.Step(ctx, utc(2026, 11, 27, 8, 0))
	if sink.count() != 3 {
		t.Fatalf("quiet state must be skipped, got %d", sink.count())
	}
}

func TestStepRetriesAfterSourceFailure(t *testing.T) {
	ctx := context.Background()
	s, src, sink, _ := newScheduler(daily(time.UTC, "08:00"))
	s.Step(ctx, utc(2026, 11, 21, 9, 0))

	src.set(Snapshot{Done: 1}, errors.New("db down"))
	s.Step(ctx, utc(2026, 11, 22, 8, 0))
	if sink.count() != 0 {
		t.Fatal("sent despite a failing source")
	}
	src.set(Snapshot{Done: 1}, nil)
	s.Step(ctx, utc(2026, 11, 22, 8, 1))
	if sink.count() != 1 {
		t.Fatalf("retry sent %d", sink.count())
	}
	last := src.calls[len(src.calls)-1]
	if !last.since.Equal(utc(2026, 11, 21, 8, 0)) {
		t.Fatalf("retry window starts %v", last.since)
	}
}

func TestStepStateSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	rule := daily(time.UTC, "08:00")
	s, src, sink, kv := newScheduler(rule)
	src.set(Snapshot{Done: 1}, nil)
	s.Step(ctx, utc(2026, 11, 21, 9, 0))
	s.Step(ctx, utc(2026, 11, 22, 8, 0))

	again := NewScheduler(Options{Rules: []Rule{rule}, Source: src, Sink: sink, KV: kv})
	again.Step(ctx, utc(2026, 11, 22, 8, 5))
	if sink.count() != 1 {
		t.Fatalf("restart re-sent the slot: %d", sink.count())
	}
	again.Step(ctx, utc(2026, 11, 23, 8, 0))
	if sink.count() != 2 {
		t.Fatalf("next slot after restart: %d", sink.count())
	}
}

func TestStepCatchesUpOnceAfterDowntimeAndCapsWindow(t *testing.T) {
	ctx := context.Background()
	s, src, sink, _ := newScheduler(daily(time.UTC, "08:00", "18:00"))
	src.set(Snapshot{Done: 1}, nil)
	s.Step(ctx, utc(2026, 11, 1, 9, 0))

	now := utc(2026, 11, 21, 12, 0)
	s.Step(ctx, now)
	if sink.count() != 1 {
		t.Fatalf("sent %d digests after downtime", sink.count())
	}
	w := src.calls[0]
	if !w.until.Equal(now) || !w.since.Equal(now.Add(-maxWindow)) {
		t.Fatalf("window %v", w)
	}
}

func TestStepWindowAcrossDST(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	ctx := context.Background()
	s, src, sink, _ := newScheduler(daily(ny, "08:00"))
	src.set(Snapshot{Done: 1}, nil)
	s.Step(ctx, utc(2026, 11, 1, 5, 0))
	s.Step(ctx, utc(2026, 11, 1, 13, 0))
	s.Step(ctx, utc(2026, 11, 2, 13, 0))
	if sink.count() != 2 {
		t.Fatalf("sent %d", sink.count())
	}
	if got := src.calls[0].until.Sub(src.calls[0].since); got != 25*time.Hour {
		t.Fatalf("fall-back day window = %v, want 25h", got)
	}
}

func TestWeeklyAndDailyKeepSeparateState(t *testing.T) {
	ctx := context.Background()
	weekly := Rule{Name: RuleWeekly, Loc: time.UTC, Slots: []Slot{{Day: int(time.Monday), At: 9 * time.Hour}}}
	s, src, sink, _ := newScheduler(daily(time.UTC, "08:00"), weekly)
	src.set(Snapshot{Decisions: []Pending{{ID: "q:Q-1", Title: "Which?"}}}, nil)
	s.Step(ctx, utc(2026, 11, 21, 12, 0))
	s.Step(ctx, utc(2026, 11, 22, 8, 0))
	s.Step(ctx, utc(2026, 11, 23, 8, 0))
	s.Step(ctx, utc(2026, 11, 23, 9, 0))
	if sink.count() != 2 {
		t.Fatalf("daily and weekly must each send once for the new decision, got %d", sink.count())
	}
	if !strings.HasPrefix(sink.sent[0].ID, "digest:daily:") || !strings.HasPrefix(sink.sent[1].ID, "digest:weekly:") {
		t.Fatalf("ids: %q %q", sink.sent[0].ID, sink.sent[1].ID)
	}
}

func TestRunWakesOnSlotsWithInjectedClock(t *testing.T) {
	clock := &fakeClock{now: utc(2026, 11, 21, 7, 0)}
	src, sink, kv := &fakeSource{}, &fakeSink{}, &memKV{}
	src.set(Snapshot{Done: 1}, nil)
	s := NewScheduler(Options{Rules: []Rule{daily(time.UTC, "08:00")}, Source: src, Sink: sink, KV: kv, Clock: clock})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	waitFor(t, func() bool { return clock.waiters() == 1 })
	clock.advance(utc(2026, 11, 21, 7, 59))
	waitFor(t, func() bool { return clock.waiters() == 1 })
	if sink.count() != 0 {
		t.Fatal("sent before the slot")
	}
	clock.advance(utc(2026, 11, 21, 8, 0))
	waitFor(t, func() bool { return sink.count() == 1 })
	waitFor(t, func() bool { return clock.waiters() == 1 })
	clock.advance(utc(2026, 11, 22, 8, 0))
	waitFor(t, func() bool { return sink.count() == 2 })
	cancel()
	<-done
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}
