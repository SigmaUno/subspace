package schedule_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/schedule"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type call struct {
	schedule string
	at       time.Time
}

type fakeExec struct {
	mu      sync.Mutex
	calls   []call
	fail    error
	onCall  func()
	counter int
}

func (f *fakeExec) Validate(context.Context, *schedule.Schedule) error { return nil }

func (f *fakeExec) CreateBrief(_ context.Context, s *schedule.Schedule, at time.Time) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{s.ID, at})
	if f.onCall != nil {
		f.onCall()
	}
	if f.fail != nil {
		return "", f.fail
	}
	f.counter++
	return fmt.Sprintf("B-%d", f.counter), nil
}

func (f *fakeExec) AddTasks(context.Context, *schedule.Schedule, time.Time) ([]string, error) {
	return nil, errors.New("not used")
}

func (f *fakeExec) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type rig struct {
	t     *testing.T
	ctx   context.Context
	clock *clock
	exec  *fakeExec
	svc   *schedule.Service
	run   *schedule.Runner
	path  string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	c := &clock{t: time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)}
	r := &rig{t: t, ctx: context.Background(), clock: c, exec: &fakeExec{}, path: filepath.Join(t.TempDir(), "s.db")}
	r.open()
	return r
}

func (r *rig) open() {
	r.t.Helper()
	st, err := sqlite.Open(r.path)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = st.Close() })
	r.svc = schedule.NewService(st, r.exec, r.clock.now)
	r.run = schedule.NewRunner(schedule.Options{Store: st, Exec: r.exec, Now: r.clock.now, Grace: 5 * time.Minute})
}

func (r *rig) create(in schedule.Input) *schedule.Schedule {
	r.t.Helper()
	if in.Action == "" {
		in.Action = schedule.ActionCreateBrief
		in.Brief = &schedule.BriefSpec{TemplateID: "feature"}
	}
	s, err := r.svc.Create(r.ctx, in)
	if err != nil {
		r.t.Fatal(err)
	}
	return s
}

func (r *rig) tick() {
	r.t.Helper()
	if err := r.run.Tick(r.ctx); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) runs(id string) []schedule.Run {
	r.t.Helper()
	rs, err := r.svc.Runs(r.ctx, id, 100)
	if err != nil {
		r.t.Fatal(err)
	}
	return rs
}

func TestRunnerRunsEachInstantOnce(t *testing.T) {
	r := newRig(t)
	s := r.create(schedule.Input{Name: "daily", Cron: "0 9 * * *"})
	r.tick()
	if r.exec.count() != 0 {
		t.Fatal("ran before the first instant")
	}
	r.clock.advance(23 * time.Hour)
	r.tick()
	r.tick()
	if r.exec.count() != 1 || !r.exec.calls[0].at.Equal(time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("calls = %+v", r.exec.calls)
	}
	rs := r.runs(s.ID)
	if len(rs) != 1 || rs[0].Status != schedule.RunCreated || rs[0].BriefID != "B-1" || rs[0].Missed != 0 {
		t.Fatalf("runs = %+v", rs)
	}

	r.clock.advance(24 * time.Hour)
	r.tick()
	if r.exec.count() != 2 || len(r.runs(s.ID)) != 2 {
		t.Fatalf("second day: %d calls, %d runs", r.exec.count(), len(r.runs(s.ID)))
	}
}

func TestRunnerSurvivesRestartsWithoutDuplicates(t *testing.T) {
	r := newRig(t)
	s := r.create(schedule.Input{Name: "hourly", Cron: "0 * * * *"})
	r.clock.advance(time.Hour)
	r.tick()
	if r.exec.count() != 1 {
		t.Fatalf("calls = %d", r.exec.count())
	}

	r.open()
	if err := r.run.Recover(r.ctx); err != nil {
		t.Fatal(err)
	}
	r.tick()
	if r.exec.count() != 1 {
		t.Fatalf("a restart repeated the run: %d calls", r.exec.count())
	}

	r.clock.advance(time.Hour)
	r.tick()
	if r.exec.count() != 2 || len(r.runs(s.ID)) != 2 {
		t.Fatalf("calls = %d, runs = %d", r.exec.count(), len(r.runs(s.ID)))
	}
}

func TestRunnerNeverRepeatsAnInstantWhenTheCursorIsLost(t *testing.T) {
	r := newRig(t)
	s := r.create(schedule.Input{Name: "daily", Cron: "0 9 * * *", CatchUp: schedule.CatchUpRunOnce})
	r.clock.advance(23 * time.Hour)
	r.tick()

	db, err := sql.Open("sqlite", r.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`UPDATE schedules SET cursor_at=0 WHERE id=?`, s.ID); err != nil {
		t.Fatal(err)
	}
	r.tick()
	if r.exec.count() != 1 || len(r.runs(s.ID)) != 1 {
		t.Fatalf("calls = %d, runs = %+v", r.exec.count(), r.runs(s.ID))
	}
}

func TestRunnerInterruptedRunIsNotRetried(t *testing.T) {
	r := newRig(t)
	s := r.create(schedule.Input{Name: "daily", Cron: "0 9 * * *"})
	r.clock.advance(23 * time.Hour)
	ctx, crash := context.WithCancel(r.ctx)
	r.exec.onCall = crash
	if err := r.run.Tick(ctx); err == nil {
		t.Fatal("tick survived the crash")
	}
	if rs := r.runs(s.ID); len(rs) != 1 || rs[0].Status != schedule.RunRunning {
		t.Fatalf("runs after crash = %+v", rs)
	}

	r.exec.onCall = nil
	r.open()
	if err := r.run.Recover(r.ctx); err != nil {
		t.Fatal(err)
	}
	r.tick()
	rs := r.runs(s.ID)
	if r.exec.count() != 1 || len(rs) != 1 || rs[0].Status != schedule.RunInterrupted {
		t.Fatalf("calls = %d, runs = %+v", r.exec.count(), rs)
	}

	r.clock.advance(24 * time.Hour)
	r.tick()
	if r.exec.count() != 2 {
		t.Fatalf("the next instant did not run: %d calls", r.exec.count())
	}
}

func TestRunnerMissedRunsAfterDowntime(t *testing.T) {
	r := newRig(t)
	skip := r.create(schedule.Input{Name: "skip", Cron: "0 9 * * *", CatchUp: schedule.CatchUpSkip})
	once := r.create(schedule.Input{Name: "once", Cron: "0 9 * * *", CatchUp: schedule.CatchUpRunOnce})

	r.clock.advance(3*24*time.Hour + 5*time.Hour)
	r.tick()

	rs := r.runs(skip.ID)
	if len(rs) != 1 || rs[0].Status != schedule.RunSkipped || rs[0].Detail != schedule.ReasonMissed || rs[0].Missed != 3 {
		t.Fatalf("skip runs = %+v", rs)
	}
	rs = r.runs(once.ID)
	if len(rs) != 1 || rs[0].Status != schedule.RunCreated || rs[0].Missed != 2 || !rs[0].ScheduledFor.Equal(time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("run-once runs = %+v", rs)
	}
	if r.exec.count() != 1 || r.exec.calls[0].schedule != once.ID {
		t.Fatalf("calls = %+v, want one run for the run-once schedule", r.exec.calls)
	}
}

func TestRunnerSkipPolicyStillRunsWhenTheLatestInstantIsFresh(t *testing.T) {
	r := newRig(t)
	s := r.create(schedule.Input{Name: "skip", Cron: "0 9 * * *"})
	r.clock.advance(2*24*time.Hour + 23*time.Hour + 3*time.Minute)
	r.tick()
	rs := r.runs(s.ID)
	if len(rs) != 1 || rs[0].Status != schedule.RunCreated || rs[0].Missed != 2 {
		t.Fatalf("runs = %+v", rs)
	}
}

func TestRunnerSkipPolicyResumesAfterAMissedInstant(t *testing.T) {
	r := newRig(t)
	s := r.create(schedule.Input{Name: "skip", Cron: "0 9 * * *"})
	r.clock.advance(23*time.Hour + 2*time.Hour)
	r.tick()
	if r.exec.count() != 0 || len(r.runs(s.ID)) != 1 {
		t.Fatalf("calls = %d, runs = %+v", r.exec.count(), r.runs(s.ID))
	}
	r.tick()
	if len(r.runs(s.ID)) != 1 {
		t.Fatal("the skipped instant was recorded twice")
	}
	r.clock.advance(22 * time.Hour)
	r.tick()
	if r.exec.count() != 1 {
		t.Fatalf("calls = %d", r.exec.count())
	}
}

func TestRunnerFailedRunIsRecordedAndTimetableContinues(t *testing.T) {
	r := newRig(t)
	s := r.create(schedule.Input{Name: "flaky", Cron: "0 * * * *"})
	r.exec.fail = errors.New("template exploded")
	r.clock.advance(time.Hour)
	r.tick()
	rs := r.runs(s.ID)
	if len(rs) != 1 || rs[0].Status != schedule.RunFailed || rs[0].Detail != "template exploded" {
		t.Fatalf("runs = %+v", rs)
	}
	r.tick()
	if r.exec.count() != 1 {
		t.Fatalf("a failed instant was retried: %d calls", r.exec.count())
	}
	r.exec.fail = nil
	r.clock.advance(time.Hour)
	r.tick()
	if got := r.runs(s.ID); len(got) != 2 || got[0].Status != schedule.RunCreated {
		t.Fatalf("runs = %+v", got)
	}
}

func TestRunnerDisabledSchedulesDoNotCatchUp(t *testing.T) {
	r := newRig(t)
	s := r.create(schedule.Input{Name: "paused", Cron: "0 * * * *", Enabled: ptr(false), CatchUp: schedule.CatchUpRunOnce})
	r.clock.advance(5 * time.Hour)
	r.tick()
	if r.exec.count() != 0 {
		t.Fatalf("disabled schedule ran %d times", r.exec.count())
	}
	if _, err := r.svc.Update(r.ctx, s.ID, schedule.Patch{Enabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	r.tick()
	if r.exec.count() != 0 {
		t.Fatal("enabling replayed the disabled period")
	}
	r.clock.advance(time.Hour)
	r.tick()
	if r.exec.count() != 1 {
		t.Fatalf("calls = %d", r.exec.count())
	}
}

func TestRunnerFollowsTheScheduleTimeZone(t *testing.T) {
	r := newRig(t)
	r.clock.t = time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)
	s := r.create(schedule.Input{Name: "gap", Cron: "30 2 * * *", Timezone: "Europe/Berlin"})
	r.clock.advance(13 * time.Hour)
	r.tick()
	rs := r.runs(s.ID)
	if len(rs) != 1 || !rs[0].ScheduledFor.Equal(time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC)) {
		t.Fatalf("runs = %+v", rs)
	}
}

func ptr[T any](v T) *T { return &v }
