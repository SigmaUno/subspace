package spendguard_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/spendguard"
)

type failingTasks struct{ err error }

func (f failingTasks) Tasks(context.Context) ([]spendguard.TaskCost, error) { return nil, f.err }

func (f failingTasks) Task(context.Context, string) (spendguard.TaskCost, error) {
	return spendguard.TaskCost{}, f.err
}

type flakyKV struct {
	memKV
	fail bool
}

func (k *flakyKV) Set(ctx context.Context, key, value string) error {
	if k.fail {
		return errors.New("kv down")
	}
	return k.memKV.Set(ctx, key, value)
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func call(t *testing.T, h http.Handler, method, body string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, "/limits", strings.NewReader(body)))
	return rec, rec.Body.String()
}

func TestLimitsEndpoint(t *testing.T) {
	f := newFixture(t, "2026-11-25T09:00:00Z", spendguard.Limits{})
	var audits [][2]spendguard.Limits
	f.opts.Audit = func(_ context.Context, before, after spendguard.Limits) error {
		audits = append(audits, [2]spendguard.Limits{before, after})
		return nil
	}
	f.g = f.reopen(t)
	h := spendguard.Handler(f.g)

	rec, body := call(t, h, "PUT", `{"daily_cost_eur":10,"weekly_cost_eur":50}`)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" || !strings.Contains(body, `"daily_cost_eur":10`) || !strings.Contains(body, `"org_budget_paused":false`) {
		t.Fatalf("put: %d %s", rec.Code, body)
	}
	if _, err := f.g.SetLimits(context.Background(), spendguard.Limits{DailyCostEUR: 10, WeeklyCostEUR: 50}); err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 || audits[0][0] != (spendguard.Limits{}) || audits[0][1] != (spendguard.Limits{DailyCostEUR: 10, WeeklyCostEUR: 50}) {
		t.Fatalf("audits %v", audits)
	}
	for _, bad := range []string{`{"daily_cost_eur":-1}`, `{"weekly_cost_eur":-0.5}`, `{"daily":1}`, `{`, `{"daily_cost_eur":1,"pad":"` + strings.Repeat("x", 4096) + `"}`} {
		rec, body := call(t, h, "PUT", bad)
		if rec.Code != 400 || rec.Header().Get("Content-Type") != "application/json" || !strings.Contains(body, `"code":"bad_request"`) {
			t.Errorf("put %.40s: %d %s", bad, rec.Code, body)
		}
	}
	if f.g.Limits() != (spendguard.Limits{DailyCostEUR: 10, WeeklyCostEUR: 50}) {
		t.Fatalf("rejected update applied: %+v", f.g.Limits())
	}

	f.spend(t, "T-1", 11)
	rec, body = call(t, h, "GET", "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" || !strings.Contains(body, `"org_budget_paused":true`) || !strings.Contains(body, `"level":"exceeded"`) {
		t.Fatalf("get: %d %s", rec.Code, body)
	}
	f.ctl.reason = "paused by owner"
	if _, body = call(t, h, "GET", ""); !strings.Contains(body, `"org_budget_paused":false`) {
		t.Fatalf("owner pause reported as budget pause: %s", body)
	}

	f.opts.Tasks = failingTasks{errors.New("db gone")}
	h = spendguard.Handler(f.reopen(t))
	if rec, body := call(t, h, "GET", ""); rec.Code != 500 || !strings.Contains(body, `"code":"internal"`) {
		t.Fatalf("get with failing source: %d %s", rec.Code, body)
	}

	f.opts.Audit = func(context.Context, spendguard.Limits, spendguard.Limits) error { return errors.New("audit down") }
	f.opts.Tasks = nil
	if _, err := f.reopen(t).SetLimits(context.Background(), spendguard.Limits{DailyCostEUR: 99}); err == nil {
		t.Fatal("audit failure swallowed")
	}
}

func TestValidateRejectsNonFiniteCaps(t *testing.T) {
	for _, l := range []spendguard.Limits{{DailyCostEUR: math.NaN()}, {WeeklyCostEUR: math.Inf(1)}, {DailyCostEUR: math.Inf(-1)}, {DailyCostEUR: -0.01}} {
		if l.Validate() == nil {
			t.Errorf("%+v accepted", l)
		}
	}
	if err := (spendguard.Limits{DailyCostEUR: 0, WeeklyCostEUR: 1e9}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestStatusDetails(t *testing.T) {
	d := spendguard.Evaluate(spendguard.Limits{DailyCostEUR: 0.5}, nil, []spendguard.Entry{{At: at("2026-11-21T01:00:00Z"), EUR: 0.25}}, at("2026-11-21T06:00:00Z"))[0]
	if d.Level != spendguard.LevelOK || d.Percent != 50 || !d.ResetsAt.Equal(at("2026-11-22T00:00:00Z")) || !d.StartsAt.Equal(at("2026-11-21T00:00:00Z")) {
		t.Fatalf("daily %+v", d)
	}
	if d.ProjectedExhaustionAt == nil || !d.ProjectedExhaustionAt.Equal(at("2026-11-21T12:00:00Z")) {
		t.Fatalf("projection %v", d.ProjectedExhaustionAt)
	}
	start := spendguard.Evaluate(spendguard.Limits{DailyCostEUR: 10}, nil, []spendguard.Entry{{At: at("2026-11-21T00:00:00Z"), EUR: 9}}, at("2026-11-21T00:00:00Z"))[0]
	if start.ProjectedExhaustionAt != nil || start.Level != spendguard.LevelWarning {
		t.Fatalf("at window start %+v", start)
	}
	full := spendguard.Evaluate(spendguard.Limits{DailyCostEUR: 10}, nil, []spendguard.Entry{{At: at("2026-11-21T01:00:00Z"), EUR: 10}}, at("2026-11-21T06:00:00Z"))[0]
	if full.ProjectedExhaustionAt != nil || full.Percent != 100 {
		t.Fatalf("exhausted %+v", full)
	}
	idle := spendguard.Evaluate(spendguard.Limits{DailyCostEUR: 10}, nil, nil, at("2026-11-21T06:00:00Z"))[0]
	if idle.ProjectedExhaustionAt != nil {
		t.Fatalf("idle projection %v", idle.ProjectedExhaustionAt)
	}
}

func TestDecisionsDescribeTheWindow(t *testing.T) {
	f := newFixture(t, "2026-11-25T06:00:00Z", spendguard.Limits{DailyCostEUR: 10, WeeklyCostEUR: 100})
	f.spend(t, "T-1", 8.5)
	f.c.advance(time.Hour)
	ds, _ := f.g.Decisions(context.Background())
	if len(ds) != 1 || !ds[0].CreatedAt.Equal(at("2026-11-25T06:00:00Z")) || ds[0].Title != "Organization daily spend is at 85% of its cap" ||
		!strings.Contains(ds[0].Summary, "€8.50 of the €10.00 daily cap is spent; the window resets 2026-11-26T00:00:00Z; projected to run out ") {
		t.Fatalf("warning decision %+v", ds)
	}
	f.spend(t, "T-1", 10)
	ds, _ = f.g.Decisions(context.Background())
	if len(ds) != 1 || !strings.HasSuffix(ds[0].Summary, "; spawns and claims are paused until then or until the cap is raised") {
		t.Fatalf("exceeded decision %+v", ds)
	}
}

func TestReconcileDatesAndCurrency(t *testing.T) {
	f := newFixture(t, "2026-11-25T09:00:00Z", spendguard.Limits{})
	f.opts.Tasks = fakeTasks{
		{ID: "T-undated", CostEUR: 1},
		{ID: "T-future", CostEUR: 2, UpdatedAt: at("2027-01-01T00:00:00Z")},
	}
	g := f.reopen(t)
	v, err := g.SetLimits(context.Background(), spendguard.Limits{DailyCostEUR: 100})
	if err != nil {
		t.Fatal(err)
	}
	if v.Windows[0].SpentEUR != 3 {
		t.Fatalf("daily spent %v, want undated and future-dated cost counted now", v.Windows[0].SpentEUR)
	}

	f.opts.Tasks = nil
	f.opts.Usage = fakeUsage{{TS: at("2026-11-25T07:00:00Z"), CostUSD: 7}}
	f.opts.EURToUSD = 0
	v, err = f.reopen(t).SetLimits(context.Background(), spendguard.Limits{DailyCostEUR: 50})
	if err != nil {
		t.Fatal(err)
	}
	if v.Windows[0].SpentEUR != 7 {
		t.Fatalf("spent %v with default rate, want 7", v.Windows[0].SpentEUR)
	}
	f.opts.EURToUSD = 0.5
	v, err = f.reopen(t).SetLimits(context.Background(), spendguard.Limits{DailyCostEUR: 50})
	if err != nil {
		t.Fatal(err)
	}
	if v.Windows[0].SpentEUR != 14 {
		t.Fatalf("spent %v at 0.5 USD per EUR, want 14", v.Windows[0].SpentEUR)
	}
}

func TestCheckTask(t *testing.T) {
	f := newFixture(t, "2026-11-25T09:00:00Z", spendguard.Limits{DailyCostEUR: 10})
	tasks := fakeTasks{{ID: "T-1", CostEUR: 12}}
	f.opts.Tasks = tasks
	f.g = f.reopen(t)
	if err := f.g.CheckTask(context.Background(), "T-1"); err != nil || f.ctl.pauses != 1 {
		t.Fatalf("check: pauses %d err %v", f.ctl.pauses, err)
	}
	if f.g.Observe(tasks[0]) {
		t.Fatal("unchanged cost observed twice")
	}
	f.opts.Tasks = failingTasks{errors.New("gone")}
	if err := f.reopen(t).CheckTask(context.Background(), "T-1"); err == nil {
		t.Fatal("task lookup error swallowed")
	}
}

func TestSameMinuteCostSharesOneLedgerEntry(t *testing.T) {
	f := newFixture(t, "2026-11-25T09:00:10Z", spendguard.Limits{DailyCostEUR: 100})
	f.spend(t, "T-1", 1)
	f.c.advance(20 * time.Second)
	f.spend(t, "T-2", 2)
	f.c.advance(time.Minute)
	f.spend(t, "T-1", 4)
	if n := strings.Count(f.kv.m["spendguard.state"], `"at":`); n != 2 {
		t.Fatalf("%d ledger entries: %s", n, f.kv.m["spendguard.state"])
	}
	if v := f.reopen(t).View(); v.Windows[0].SpentEUR != 6 {
		t.Fatalf("spent after reopen %v", v.Windows[0].SpentEUR)
	}
}

func TestStateSurvivesKVOutage(t *testing.T) {
	kv := &flakyKV{memKV: memKV{m: map[string]string{}}}
	f := &fixture{c: &clock{at("2026-11-25T09:00:00Z")}, ctl: &fakeControl{}, pub: &recorder{}}
	f.opts = spendguard.Options{Control: f.ctl, KV: kv, Pub: f.pub, Now: f.c.Now}
	g := f.reopen(t)
	if _, err := g.SetLimits(context.Background(), spendguard.Limits{DailyCostEUR: 10}); err != nil {
		t.Fatal(err)
	}
	kv.fail = true
	g.Observe(spendguard.TaskCost{ID: "T-1", CostEUR: 5})
	if err := g.Evaluate(context.Background()); err == nil {
		t.Fatal("persist failure swallowed")
	}
	kv.fail = false
	if err := g.Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := f.reopen(t).View(); v.Windows[0].SpentEUR != 5 {
		t.Fatalf("spend lost across the outage: %v", v.Windows[0].SpentEUR)
	}
}

func TestOldLedgerEntriesArePruned(t *testing.T) {
	f := newFixture(t, "2026-11-25T09:00:00Z", spendguard.Limits{DailyCostEUR: 100})
	f.spend(t, "T-1", 5)
	f.c.t = at("2026-11-30T00:00:00Z")
	if err := f.g.Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.kv.m["spendguard.state"], `"at":`) {
		t.Fatalf("last week's ledger kept: %s", f.kv.m["spendguard.state"])
	}
	f.spend(t, "T-1", 7)
	f.c.advance(time.Hour)
	if err := f.g.Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := f.reopen(t).View(); v.Windows[1].SpentEUR != 2 {
		t.Fatalf("cost at the first instant of the week pruned: %+v", v.Windows[1])
	}
}

func TestReloadPicksUpAnotherInstancesState(t *testing.T) {
	f := newFixture(t, "2026-11-25T09:00:00Z", spendguard.Limits{DailyCostEUR: 10})
	other := f.reopen(t)
	if _, err := other.SetLimits(context.Background(), spendguard.Limits{DailyCostEUR: 40}); err != nil {
		t.Fatal(err)
	}
	other.Observe(spendguard.TaskCost{ID: "T-1", CostEUR: 3})
	if err := other.Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.g.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := f.g.View(); v.DailyCostEUR != 40 || v.Windows[0].SpentEUR != 3 {
		t.Fatalf("view after reload %+v", v)
	}
	f.kv.m["spendguard.state"] = "{"
	if err := f.g.Reload(context.Background()); err == nil {
		t.Fatal("corrupt state accepted")
	}
	if _, err := spendguard.New(context.Background(), f.opts); err == nil {
		t.Fatal("New accepted corrupt state")
	}
	f.kv.m["spendguard.state"] = ""
	f.kv.m["spendguard.limits"] = "{"
	if err := f.g.Reload(context.Background()); err == nil {
		t.Fatal("corrupt limits accepted")
	}
	if _, err := spendguard.New(context.Background(), f.opts); err == nil {
		t.Fatal("New accepted corrupt limits")
	}
}

func TestRunLogsSweepFailures(t *testing.T) {
	logs := &lockedBuffer{}
	f := newFixture(t, "2026-11-25T09:00:00Z", spendguard.Limits{DailyCostEUR: 10})
	f.opts.Tasks = failingTasks{errors.New("db gone")}
	f.opts.Log = slog.New(slog.NewTextHandler(logs, nil))
	g := f.reopen(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.Run(ctx, time.Hour); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), "spendguard sweep") {
		if time.Now().After(deadline) {
			t.Fatal("sweep failure not logged")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	logs = &lockedBuffer{}
	f.opts.Tasks = fakeTasks{{ID: "T-1", CostEUR: 1}}
	f.opts.Usage = fakeUsage{}
	f.opts.Log = slog.New(slog.NewTextHandler(logs, nil))
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	g = f.reopen(t)
	go func() { g.Run(ctx, time.Millisecond); close(done) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done
	if logs.String() != "" {
		t.Fatalf("healthy sweep logged: %s", logs)
	}
	if v := g.View(); v.Windows[0].SpentEUR != 1 {
		t.Fatalf("sweep did not reconcile: %+v", v.Windows[0])
	}
}
