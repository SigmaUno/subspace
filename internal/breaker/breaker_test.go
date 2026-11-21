package breaker

import (
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newSet(c *clock, changes *[]Status) *Set {
	cfg := Config{Threshold: 0.5, Window: 5 * time.Minute, MinSamples: 10, Cooldown: time.Minute}
	return New(cfg, c.now, func(s Status) { *changes = append(*changes, s) })
}

func feed(s *Set, c *clock, failed, ok int) {
	for range failed {
		s.Record("anthropic", "m", true)
		c.advance(time.Second)
	}
	for range ok {
		s.Record("anthropic", "m", false)
		c.advance(time.Second)
	}
}

func TestNeedsMinSamplesToOpen(t *testing.T) {
	c := &clock{time.Unix(1_000_000, 0)}
	var ch []Status
	s := newSet(c, &ch)
	feed(s, c, 9, 0)
	if got := s.State("anthropic", "m"); got != Closed {
		t.Fatalf("9 samples must not open: %s", got)
	}
	feed(s, c, 0, 1)
	if got := s.State("anthropic", "m"); got != Open {
		t.Fatalf("10th sample with 9 errors must open: %s", got)
	}
}

func TestExactlyAtThresholdStaysClosed(t *testing.T) {
	c := &clock{time.Unix(1_000_000, 0)}
	var ch []Status
	s := newSet(c, &ch)
	feed(s, c, 0, 5)
	feed(s, c, 5, 0)
	if got := s.State("anthropic", "m"); got != Closed {
		t.Fatalf("5/10 is not above 0.5: %s", got)
	}
	feed(s, c, 1, 0)
	if got := s.State("anthropic", "m"); got != Open {
		t.Fatalf("6/11 should open: %s", got)
	}
	if len(ch) != 1 || ch[0].State != Open {
		t.Fatalf("changes: %+v", ch)
	}
}

func TestOldSamplesLeaveTheWindow(t *testing.T) {
	c := &clock{time.Unix(1_000_000, 0)}
	var ch []Status
	s := newSet(c, &ch)
	feed(s, c, 0, 8)
	c.advance(6 * time.Minute)
	feed(s, c, 9, 0)
	if got := s.State("anthropic", "m"); got != Closed {
		t.Fatalf("only 9 samples are inside the window: %s", got)
	}
	feed(s, c, 1, 0)
	if got := s.State("anthropic", "m"); got != Open {
		t.Fatalf("10 errors in window: %s", got)
	}
}

func TestOpenRefusesThenHalfOpensForOneProbe(t *testing.T) {
	c := &clock{time.Unix(1_000_000, 0)}
	var ch []Status
	s := newSet(c, &ch)
	feed(s, c, 10, 0)
	if err := s.Admit("anthropic", "m", 1); err == nil {
		t.Fatal("open breaker must refuse")
	}
	if err := s.Admit("anthropic", "other", 1); err != nil {
		t.Fatalf("other model unaffected: %v", err)
	}
	c.advance(time.Minute)
	if got := s.State("anthropic", "m"); got != HalfOpen {
		t.Fatalf("after cooldown: %s", got)
	}
	if err := s.Admit("anthropic", "m", 2); err == nil {
		t.Fatal("half-open must refuse more than one agent")
	}
	if err := s.Admit("anthropic", "m", 1); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if err := s.Admit("anthropic", "m", 1); err == nil {
		t.Fatal("second probe must be refused")
	}
}

func TestProbeOutcomeClosesOrReopens(t *testing.T) {
	c := &clock{time.Unix(1_000_000, 0)}
	var ch []Status
	s := newSet(c, &ch)
	feed(s, c, 10, 0)
	c.advance(time.Minute)
	s.Record("anthropic", "m", true)
	if got := s.State("anthropic", "m"); got != Open {
		t.Fatalf("failed probe reopens: %s", got)
	}
	c.advance(time.Minute)
	if got := s.State("anthropic", "m"); got != HalfOpen {
		t.Fatalf("second cooldown: %s", got)
	}
	s.Record("anthropic", "m", false)
	if got := s.State("anthropic", "m"); got != Closed {
		t.Fatalf("successful probe closes: %s", got)
	}
	if err := s.Admit("anthropic", "m", 5); err != nil {
		t.Fatal(err)
	}
	var states []State
	for _, x := range ch {
		states = append(states, x.State)
	}
	want := []State{Open, Open, Closed}
	if len(states) != len(want) {
		t.Fatalf("changes %v want %v", states, want)
	}
}

func TestLostProbeIsGrantedAgainAfterCooldown(t *testing.T) {
	c := &clock{time.Unix(1_000_000, 0)}
	var ch []Status
	s := newSet(c, &ch)
	feed(s, c, 10, 0)
	c.advance(time.Minute)
	if err := s.Admit("anthropic", "m", 1); err != nil {
		t.Fatal(err)
	}
	c.advance(time.Minute)
	if err := s.Admit("anthropic", "m", 1); err != nil {
		t.Fatalf("probe grant should expire: %v", err)
	}
}

func TestResetClosesEveryModelOfProvider(t *testing.T) {
	c := &clock{time.Unix(1_000_000, 0)}
	var ch []Status
	s := newSet(c, &ch)
	for _, m := range []string{"a", "b"} {
		for range 10 {
			s.Record("anthropic", m, true)
		}
	}
	for range 10 {
		s.Record("openai", "x", true)
	}
	out := s.Reset("anthropic")
	if len(out) != 2 || out[0].State != Closed || out[1].State != Closed {
		t.Fatalf("reset: %+v", out)
	}
	if s.State("openai", "x") != Open {
		t.Fatal("other provider must stay open")
	}
	if len(s.Reset("nobody")) != 0 {
		t.Fatal("unknown provider")
	}
}

func TestListReportsRateAndSchedule(t *testing.T) {
	c := &clock{time.Unix(1_000_000, 0)}
	var ch []Status
	s := newSet(c, &ch)
	feed(s, c, 3, 1)
	l := s.List()
	if len(l) != 1 || l[0].Samples != 4 || l[0].Errors != 3 || l[0].ErrorRate != 0.75 || l[0].OpenedAt != nil {
		t.Fatalf("closed: %+v", l)
	}
	feed(s, c, 6, 0)
	l = s.List()
	if l[0].State != Open || l[0].OpenedAt == nil || l[0].HalfOpenAt == nil || !l[0].HalfOpenAt.After(*l[0].OpenedAt) {
		t.Fatalf("open: %+v", l[0])
	}
}
