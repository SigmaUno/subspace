package spendguard_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/spendguard"
	"github.com/SigmaUno/subspace/internal/usage"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestWindowsRollOverAtUTCBoundaries(t *testing.T) {
	entries := []spendguard.Entry{
		{At: at("2026-11-22T23:59:59Z"), EUR: 4},
		{At: at("2026-11-23T00:00:00Z"), EUR: 1},
		{At: at("2026-11-23T10:00:00Z"), EUR: 2},
	}
	limits := spendguard.Limits{DailyCostEUR: 10, WeeklyCostEUR: 10}
	for _, tc := range []struct {
		now           string
		daily, weekly float64
	}{
		{"2026-11-22T23:59:59Z", 4, 4},
		{"2026-11-23T00:00:00Z", 1, 1},
		{"2026-11-23T12:00:00Z", 3, 3},
		{"2026-11-29T23:59:59Z", 0, 3},
		{"2026-11-30T00:00:00Z", 0, 0},
	} {
		got := spendguard.Evaluate(limits, nil, entries, at(tc.now))
		if got[0].SpentEUR != tc.daily || got[1].SpentEUR != tc.weekly {
			t.Errorf("%s: daily %v weekly %v, want %v and %v", tc.now, got[0].SpentEUR, got[1].SpentEUR, tc.daily, tc.weekly)
		}
	}
	w := spendguard.Evaluate(limits, nil, entries, at("2026-11-26T15:00:00Z"))[1]
	if !w.StartsAt.Equal(at("2026-11-23T00:00:00Z")) || !w.ResetsAt.Equal(at("2026-11-30T00:00:00Z")) {
		t.Fatalf("weekly window %v..%v", w.StartsAt, w.ResetsAt)
	}
}

func TestLevelsAndProjection(t *testing.T) {
	limits := spendguard.Limits{DailyCostEUR: 10}
	for _, tc := range []struct {
		spent float64
		level spendguard.Level
	}{{0, spendguard.LevelOK}, {7.99, spendguard.LevelOK}, {8, spendguard.LevelWarning}, {9.99, spendguard.LevelWarning}, {10, spendguard.LevelExceeded}, {12, spendguard.LevelExceeded}} {
		s := spendguard.Evaluate(limits, nil, []spendguard.Entry{{At: at("2026-11-21T01:00:00Z"), EUR: tc.spent}}, at("2026-11-21T06:00:00Z"))
		if s[0].Level != tc.level {
			t.Errorf("spent %v: level %s, want %s", tc.spent, s[0].Level, tc.level)
		}
		if s[1].Level != spendguard.LevelOff {
			t.Errorf("weekly cap 0 should be off, got %s", s[1].Level)
		}
	}
	s := spendguard.Evaluate(limits, nil, []spendguard.Entry{{At: at("2026-11-21T01:00:00Z"), EUR: 6}}, at("2026-11-21T06:00:00Z"))[0]
	if s.ProjectedExhaustionAt == nil || !s.ProjectedExhaustionAt.Equal(at("2026-11-21T10:00:00Z")) {
		t.Fatalf("projection %v", s.ProjectedExhaustionAt)
	}
	s = spendguard.Evaluate(limits, nil, []spendguard.Entry{{At: at("2026-11-21T01:00:00Z"), EUR: 1}}, at("2026-11-21T06:00:00Z"))[0]
	if s.ProjectedExhaustionAt != nil {
		t.Fatalf("slow burn must not project exhaustion inside the window: %v", s.ProjectedExhaustionAt)
	}
}

func TestUsageAndTaskCostAreNotAdded(t *testing.T) {
	u := []spendguard.Entry{{At: at("2026-11-21T01:00:00Z"), EUR: 3}}
	task := []spendguard.Entry{{At: at("2026-11-21T02:00:00Z"), EUR: 5}}
	s := spendguard.Evaluate(spendguard.Limits{DailyCostEUR: 10}, u, task, at("2026-11-21T06:00:00Z"))[0]
	if s.SpentEUR != 5 {
		t.Fatalf("spent %v, want the larger source (5)", s.SpentEUR)
	}
}

type clock struct{ t time.Time }

func (c *clock) Now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

type fakeControl struct {
	mu              sync.Mutex
	paused          bool
	reason          string
	pauses, resumes int
}

func (f *fakeControl) PauseAll(_ context.Context, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paused, f.reason = true, reason
	f.pauses++
	return nil
}

func (f *fakeControl) ResumeAll(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paused, f.reason = false, ""
	f.resumes++
	return nil
}

func (f *fakeControl) Paused() (bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paused, f.reason
}

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
	k.m[key] = value
	return nil
}

type recorder struct {
	mu    sync.Mutex
	types []string
}

func (r *recorder) Publish(_ []string, typ string, _ any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.types = append(r.types, typ)
}

func (r *recorder) count(typ string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, x := range r.types {
		if x == typ {
			n++
		}
	}
	return n
}

type fixture struct {
	g    *spendguard.Guard
	c    *clock
	ctl  *fakeControl
	kv   *memKV
	pub  *recorder
	opts spendguard.Options
}

func newFixture(t *testing.T, now string, limits spendguard.Limits) *fixture {
	t.Helper()
	f := &fixture{c: &clock{at(now)}, ctl: &fakeControl{}, kv: &memKV{m: map[string]string{}}, pub: &recorder{}}
	f.opts = spendguard.Options{Control: f.ctl, KV: f.kv, Pub: f.pub, Now: f.c.Now}
	f.g = f.reopen(t)
	if _, err := f.g.SetLimits(context.Background(), limits); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) reopen(t *testing.T) *spendguard.Guard {
	t.Helper()
	g, err := spendguard.New(context.Background(), f.opts)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func (f *fixture) spend(t *testing.T, id string, total float64) {
	t.Helper()
	f.g.Observe(spendguard.TaskCost{ID: id, CostEUR: total})
	if err := f.g.Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPausesExactlyOncePerWindowAndResumesOnRollover(t *testing.T) {
	f := newFixture(t, "2026-11-25T09:00:00Z", spendguard.Limits{DailyCostEUR: 10})

	f.spend(t, "T-1", 7)
	if f.ctl.pauses != 0 || f.pub.count("spend.warning") != 0 {
		t.Fatalf("70%%: pauses %d warnings %d", f.ctl.pauses, f.pub.count("spend.warning"))
	}
	f.spend(t, "T-1", 8)
	f.spend(t, "T-1", 9)
	if f.ctl.pauses != 0 || f.pub.count("spend.warning") != 1 {
		t.Fatalf("80-90%%: pauses %d warnings %d, want 0 and 1", f.ctl.pauses, f.pub.count("spend.warning"))
	}
	ds, _ := f.g.Decisions(context.Background())
	if len(ds) != 1 || ds[0].ID != "budget:org-daily" {
		t.Fatalf("decisions %+v", ds)
	}

	f.spend(t, "T-1", 10)
	for range 5 {
		f.spend(t, "T-2", 3)
		f.c.advance(time.Minute)
	}
	if f.ctl.pauses != 1 || f.ctl.reason != spendguard.PauseReason {
		t.Fatalf("pauses %d reason %q, want exactly one org_budget pause", f.ctl.pauses, f.ctl.reason)
	}
	if f.pub.count("spend.warning") != 1 {
		t.Fatalf("warning repeated: %d", f.pub.count("spend.warning"))
	}

	f.g = f.reopen(t)
	if err := f.g.Evaluate(context.Background()); err != nil || f.ctl.pauses != 1 {
		t.Fatalf("restart re-paused: pauses %d err %v", f.ctl.pauses, err)
	}

	f.c.t = at("2026-11-25T23:59:59Z")
	if err := f.g.Evaluate(context.Background()); err != nil || f.ctl.resumes != 0 {
		t.Fatalf("resumed before rollover: %d %v", f.ctl.resumes, err)
	}
	f.c.t = at("2026-11-26T00:00:00Z")
	if err := f.g.Evaluate(context.Background()); err != nil || f.ctl.resumes != 1 || f.ctl.paused {
		t.Fatalf("not resumed at rollover: resumes %d paused %v err %v", f.ctl.resumes, f.ctl.paused, err)
	}

	f.spend(t, "T-1", 20)
	if f.ctl.pauses != 2 {
		t.Fatalf("second window: pauses %d, want 2", f.ctl.pauses)
	}
}

func TestOwnerResumeAndPauseAreRespected(t *testing.T) {
	f := newFixture(t, "2026-11-25T09:00:00Z", spendguard.Limits{DailyCostEUR: 10})
	f.spend(t, "T-1", 11)
	if f.ctl.pauses != 1 {
		t.Fatalf("pauses %d", f.ctl.pauses)
	}
	if err := f.ctl.ResumeAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.spend(t, "T-1", 12)
	if f.ctl.pauses != 1 {
		t.Fatalf("re-paused after the owner resumed in the same window: %d", f.ctl.pauses)
	}

	if _, err := f.g.SetLimits(context.Background(), spendguard.Limits{DailyCostEUR: 12}); err != nil {
		t.Fatal(err)
	}
	if f.ctl.pauses != 2 {
		t.Fatalf("a new cap that is already exceeded must pause again: %d", f.ctl.pauses)
	}
	if _, err := f.g.SetLimits(context.Background(), spendguard.Limits{DailyCostEUR: 100}); err != nil {
		t.Fatal(err)
	}
	if f.ctl.paused {
		t.Fatal("raising the cap must lift the org_budget pause")
	}

	f.ctl.paused, f.ctl.reason = true, "paused by owner"
	f.c.advance(48 * time.Hour)
	if err := f.g.Evaluate(context.Background()); err != nil || !f.ctl.paused {
		t.Fatalf("owner pause was lifted: paused %v err %v", f.ctl.paused, err)
	}
}

func TestWeeklyCapOutlivesDailyRollover(t *testing.T) {
	f := newFixture(t, "2026-11-25T09:00:00Z", spendguard.Limits{DailyCostEUR: 100, WeeklyCostEUR: 10})
	f.spend(t, "T-1", 10)
	if f.ctl.pauses != 1 {
		t.Fatalf("pauses %d", f.ctl.pauses)
	}
	f.c.advance(24 * time.Hour)
	if err := f.g.Evaluate(context.Background()); err != nil || !f.ctl.paused {
		t.Fatalf("daily rollover lifted a weekly pause: %v", err)
	}
	f.c.t = at("2026-11-30T00:00:00Z")
	if err := f.g.Evaluate(context.Background()); err != nil || f.ctl.paused {
		t.Fatalf("weekly rollover did not resume: paused %v err %v", f.ctl.paused, err)
	}
}

type fakeTasks []spendguard.TaskCost

func (f fakeTasks) Tasks(context.Context) ([]spendguard.TaskCost, error) { return f, nil }

func (f fakeTasks) Task(_ context.Context, id string) (spendguard.TaskCost, error) {
	for _, t := range f {
		if t.ID == id {
			return t, nil
		}
	}
	return spendguard.TaskCost{}, nil
}

type fakeUsage []usage.Record

func (f fakeUsage) Query(_ context.Context, q usage.Query) ([]usage.Record, error) {
	var out []usage.Record
	for _, r := range f {
		if !r.TS.Before(q.From) && r.TS.Before(q.To) {
			out = append(out, r)
		}
	}
	return out, nil
}

func TestReconcileAttributesCostToItsWindowAndConvertsUsage(t *testing.T) {
	f := newFixture(t, "2026-11-25T09:00:00Z", spendguard.Limits{})
	f.opts.Tasks = fakeTasks{
		{ID: "T-old", CostEUR: 50, UpdatedAt: at("2026-11-20T10:00:00Z")},
		{ID: "T-new", CostEUR: 2, UpdatedAt: at("2026-11-25T08:00:00Z")},
	}
	f.opts.Usage = fakeUsage{{TS: at("2026-11-25T07:00:00Z"), CostUSD: 11}, {TS: at("2026-11-10T07:00:00Z"), CostUSD: 99}}
	f.opts.EURToUSD = 1.1
	g := f.reopen(t)
	v, err := g.SetLimits(context.Background(), spendguard.Limits{DailyCostEUR: 100, WeeklyCostEUR: 100})
	if err != nil {
		t.Fatal(err)
	}
	if v.Windows[0].SpentEUR != 10 || v.Windows[1].SpentEUR != 10 {
		t.Fatalf("daily %v weekly %v, want 10 each (usage 11 USD at 1.1, larger than tasks 2)", v.Windows[0].SpentEUR, v.Windows[1].SpentEUR)
	}
}
