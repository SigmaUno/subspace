package digest

import (
	"context"
	"testing"
	"time"
)

func mapEnv(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestReloadRejectsInvalidTimesAndKeepsRules(t *testing.T) {
	s, _, _, _ := newScheduler(daily(time.UTC, "08:00"))
	for _, bad := range []map[string]string{
		{EnvAt: "8am"},
		{EnvWeekly: "someday@09:00"},
		{EnvAt: "08:00", EnvTZ: "Mars/Olympus"},
	} {
		if _, err := s.PrepareReload(mapEnv(bad)); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
	if rules := s.Rules(); len(rules) != 1 || rules[0].Name != RuleDaily {
		t.Fatalf("rules = %+v", rules)
	}
}

func TestReloadSwitchesSlotsAtTheNextStep(t *testing.T) {
	ctx := context.Background()
	s, src, sink, _ := newScheduler(daily(time.UTC, "08:00"))
	src.set(Snapshot{Done: 1}, nil)
	s.Step(ctx, utc(2026, 11, 21, 7, 0))

	plan, err := s.PrepareReload(mapEnv(map[string]string{EnvAt: "09:00,10:00", EnvWeekly: "sat@11:00"}))
	if err != nil {
		t.Fatal(err)
	}
	plan.Commit()
	if len(s.Rules()) != 2 {
		t.Fatalf("rules = %+v", s.Rules())
	}
	s.Step(ctx, utc(2026, 11, 21, 8, 30))
	if sink.count() != 0 {
		t.Fatalf("reload fired a digest for a slot that was not reached, sent %d", sink.count())
	}
	s.Step(ctx, utc(2026, 11, 21, 9, 0))
	s.Step(ctx, utc(2026, 11, 21, 10, 0))
	if sink.count() != 2 {
		t.Fatalf("09:00 and 10:00 slots sent %d digests", sink.count())
	}
}

func TestSetRulesWakesRunLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clock := &fakeClock{now: utc(2026, 11, 21, 7, 0)}
	src, sink := &fakeSource{}, &fakeSink{}
	s := NewScheduler(Options{Source: src, Sink: sink, KV: &memKV{}, Clock: clock})
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for clock.waiters() < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	s.SetRules([]Rule{daily(time.UTC, "08:00")})
	for clock.waiters() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if clock.waiters() < 2 {
		t.Fatal("run loop did not recompute its wait after SetRules")
	}
	cancel()
	<-done
}
