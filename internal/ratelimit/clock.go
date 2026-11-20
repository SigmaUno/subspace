package ratelimit

import (
	"sort"
	"sync"
	"time"
)

// Clock abstracts time so tests can drive the limiter deterministically.
type Clock interface {
	Now() time.Time
	// Timer returns a channel that receives once after d, and a stop func
	// that releases the timer (reporting whether it was still pending).
	Timer(d time.Duration) (<-chan time.Time, func() bool)
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Timer(d time.Duration) (<-chan time.Time, func() bool) {
	t := time.NewTimer(d)
	return t.C, t.Stop
}

// FakeClock is a manually advanced Clock for tests.
type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*fakeTimer]struct{}
}

type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

// NewFakeClock returns a FakeClock starting at start.
func NewFakeClock(start time.Time) *FakeClock {
	return &FakeClock{now: start, timers: map[*fakeTimer]struct{}{}}
}

// Now returns the fake current time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Timer implements Clock.
func (c *FakeClock) Timer(d time.Duration) (<-chan time.Time, func() bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	if d <= 0 {
		t.ch <- c.now
		return t.ch, func() bool { return false }
	}
	c.timers[t] = struct{}{}
	return t.ch, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		_, pending := c.timers[t]
		delete(c.timers, t)
		return pending
	}
}

// Advance moves the clock forward and fires every timer that is now due.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	due := make([]*fakeTimer, 0, len(c.timers))
	for t := range c.timers {
		if !t.at.After(c.now) {
			due = append(due, t)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].at.Before(due[j].at) })
	for _, t := range due {
		delete(c.timers, t)
		t.ch <- c.now
	}
}

// Pending returns the number of timers that have not fired yet.
func (c *FakeClock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}
