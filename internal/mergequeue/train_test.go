package mergequeue

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

var trainClock = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

type trainRunner struct {
	mu    sync.Mutex
	runs  int
	git   int
	crash func(args []string) bool
}

var errCrash = errors.New("simulated crash")

func (f *trainRunner) Run(ctx context.Context, dir, name string, args ...string) (string, error) {
	if name == "git" {
		out, err := ExecRunner{}.Run(ctx, dir, name, args...)
		f.mu.Lock()
		f.git++
		crash := f.crash != nil && f.crash(args)
		f.mu.Unlock()
		if crash {
			panic(errCrash)
		}
		return out, err
	}
	f.mu.Lock()
	f.runs++
	f.mu.Unlock()
	if _, err := os.Stat(filepath.Join(dir, "culprit.txt")); err == nil {
		return "FAIL: culprit.txt breaks the build\n", errors.New("exit status 1")
	}
	return "ok\n", nil
}

func (f *trainRunner) testRuns() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs
}

type fakeDeps map[string]TaskInfo

func (d fakeDeps) Task(_ context.Context, id string) (TaskInfo, error) { return d[id], nil }

type fakeChecks map[string]*CheckStatus

func (f fakeChecks) Statuses(_ context.Context, c Candidate) (*CheckStatus, error) {
	return f[c.TaskID], nil
}

type trainOpts struct {
	max      int
	auto     bool
	required []string
	deps     Deps
	checks   Checks
	runner   *trainRunner
}

func (e *env) trainQueue(o trainOpts) *Queue {
	e.t.Helper()
	q, err := New(Config{
		Repos: map[string]RepoConfig{repo: {Remote: e.remote, Verify: []string{"verify"}, Auto: o.auto,
			MaxTrain: o.max, Required: o.required}},
		WorkDir:  filepath.Join(e.root, "work"),
		Store:    e.store,
		Runner:   o.runner,
		Notifier: e.rec,
		Gate:     e.rec,
		Tasks:    e.rec,
		Deps:     o.deps,
		Checks:   o.checks,
		Now:      func() time.Time { return trainClock },
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return q
}

func (e *env) train(q *Queue) *Train {
	e.t.Helper()
	tr, err := q.ProcessTrain(context.Background(), repo)
	if err != nil {
		e.t.Fatalf("train: %v", err)
	}
	if tr == nil {
		e.t.Fatal("train: nothing processed")
	}
	return tr
}

func (e *env) idle(q *Queue) {
	e.t.Helper()
	if tr, err := q.ProcessTrain(context.Background(), repo); tr != nil || err != nil {
		e.t.Fatalf("expected an idle queue, got %+v %v", tr, err)
	}
}

func (e *env) states() map[string]*Candidate {
	e.t.Helper()
	list, err := e.store.List(context.Background(), repo)
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]*Candidate{}
	for _, c := range list {
		out[c.TaskID] = c
	}
	return out
}

func (e *env) onMain(file string) bool {
	_, err := ExecRunner{}.Run(context.Background(), e.remote, "git", "cat-file", "-e", "refs/heads/main:"+file)
	return err == nil
}

func (e *env) note(task, substr string) bool {
	e.rec.mu.Lock()
	defer e.rec.mu.Unlock()
	return slices.ContainsFunc(e.rec.notes, func(n string) bool { return strings.HasPrefix(n, task+": ") && strings.Contains(n, substr) })
}

func members(tr *Train) []string {
	var out []string
	for _, m := range tr.Members {
		out = append(out, m.TaskID+"="+m.Result)
	}
	return out
}

func TestTrainMergesBatchWithOneTestRun(t *testing.T) {
	e := newEnv(t)
	e.rec.approved = true
	for i := 1; i <= 5; i++ {
		e.branch(fmt.Sprintf("pr-%d", i), fmt.Sprintf("f%d.txt", i), "x\n")
	}
	e.advanceMain("main.txt", "y\n")
	oldMain := e.ref("main")
	run := &trainRunner{}
	q := e.trainQueue(trainOpts{max: 4, runner: run})
	for i := 1; i <= 5; i++ {
		e.enqueue(q, i, fmt.Sprintf("pr-%d", i), fmt.Sprintf("T-%d", i))
	}

	tr := e.train(q)
	if tr.State != TrainMerged || tr.Runs != 1 || run.testRuns() != 1 || len(tr.Members) != 4 {
		t.Fatalf("train %s runs %d/%d members %v\n%s", tr.State, tr.Runs, run.testRuns(), members(tr), tr.Log)
	}
	if !tr.CreatedAt.Equal(trainClock) || tr.BaseSHA != oldMain || e.ref("main") != tr.Tip {
		t.Fatalf("train %+v, main %s", tr, e.ref("main"))
	}
	st := e.states()
	prev := oldMain
	for i := 1; i <= 4; i++ {
		c := st[fmt.Sprintf("T-%d", i)]
		if c.State != StateMerged || !e.isAncestor(prev, c.MergedSHA) || prev == c.MergedSHA {
			t.Fatalf("T-%d %s %s not merged after %s", i, c.State, c.MergedSHA, prev)
		}
		prev = c.MergedSHA
		if !e.note(c.TaskID, "merged into main") || !e.note(c.TaskID, tr.ID) {
			t.Fatalf("notes %q", e.rec.notes)
		}
	}
	if st["T-5"].State != StateQueued || e.onMain("f5.txt") {
		t.Fatalf("T-5 %s exceeded the train size", st["T-5"].State)
	}

	tr2 := e.train(q)
	if tr2.ID == tr.ID || tr2.State != TrainMerged || len(tr2.Members) != 1 || run.testRuns() != 2 {
		t.Fatalf("second train %+v", tr2)
	}
	e.idle(q)
	if len(e.rec.changes) != 0 || !e.onMain("f1.txt") || !e.onMain("f5.txt") || !e.onMain("main.txt") {
		t.Fatalf("changes %q", e.rec.changes)
	}
	stored, err := e.store.GetTrain(context.Background(), tr.ID)
	if err != nil || stored.State != TrainMerged || stored.Tip != tr.Tip {
		t.Fatalf("stored %+v %v", stored, err)
	}
}

func TestTrainBisectionIsolatesCulprit(t *testing.T) {
	const n = 8
	bound := int(math.Ceil(math.Log2(n))) + 1
	for _, culprit := range []int{1, 3, 6, 8} {
		t.Run(fmt.Sprintf("culprit-%d", culprit), func(t *testing.T) {
			e := newEnv(t)
			for i := 1; i <= n; i++ {
				file := fmt.Sprintf("f%d.txt", i)
				if i == culprit {
					file = "culprit.txt"
				}
				e.branch(fmt.Sprintf("pr-%d", i), file, "x\n")
			}
			run := &trainRunner{}
			q := e.trainQueue(trainOpts{max: n, auto: true, runner: run})
			for i := 1; i <= n; i++ {
				e.enqueue(q, i, fmt.Sprintf("pr-%d", i), fmt.Sprintf("T-%d", i))
			}

			tr := e.train(q)
			if run.testRuns() > bound || tr.Runs != run.testRuns() {
				t.Fatalf("%d test runs (train says %d), bound %d", run.testRuns(), tr.Runs, bound)
			}
			bad := fmt.Sprintf("T-%d", culprit)
			st := e.states()
			for i := 1; i <= n; i++ {
				id := fmt.Sprintf("T-%d", i)
				want := StateQueued
				switch {
				case i < culprit:
					want = StateMerged
				case i == culprit:
					want = StateEjected
				}
				if st[id].State != want {
					t.Fatalf("%s is %s, want %s; members %v", id, st[id].State, want, members(tr))
				}
			}
			if len(e.rec.changes) != 1 || !strings.HasPrefix(e.rec.changes[0], bad+": ") ||
				!e.note(bad, "bisection isolated") || !e.note(bad, "FAIL: culprit.txt") {
				t.Fatalf("changes %q notes %q", e.rec.changes, e.rec.notes)
			}
			if e.onMain("culprit.txt") {
				t.Fatal("culprit reached main")
			}
			if culprit < n {
				if !e.note(fmt.Sprintf("T-%d", culprit+1), "returned to the queue") {
					t.Fatalf("notes %q", e.rec.notes)
				}
				before := run.testRuns()
				rest := e.train(q)
				if rest.State != TrainMerged || run.testRuns() != before+1 || len(rest.Members) != n-culprit {
					t.Fatalf("rest train %s %v", rest.State, members(rest))
				}
			}
			e.idle(q)
			for i := 1; i <= n; i++ {
				if i != culprit && !e.onMain(fmt.Sprintf("f%d.txt", i)) {
					t.Fatalf("f%d.txt missing on main", i)
				}
			}
		})
	}
}

func TestTrainOrdersByDependencyAndPriority(t *testing.T) {
	e := newEnv(t)
	for i := 1; i <= 6; i++ {
		e.branch(fmt.Sprintf("pr-%d", i), fmt.Sprintf("f%d.txt", i), "x\n")
	}
	deps := fakeDeps{
		"T-2": {Priority: 5, DependsOn: []string{"T-3"}},
		"T-4": {DependsOn: []string{"T-9"}},
		"T-5": {DependsOn: []string{"T-8"}},
		"T-6": {Priority: 3},
		"T-8": {Done: true},
	}
	run := &trainRunner{}
	q := e.trainQueue(trainOpts{max: 10, auto: true, deps: deps, runner: run})
	for _, i := range []int{2, 1, 3, 4, 5, 6} {
		e.enqueue(q, i, fmt.Sprintf("pr-%d", i), fmt.Sprintf("T-%d", i))
	}

	tr := e.train(q)
	want := []string{"T-6=merged", "T-1=merged", "T-3=merged", "T-2=merged", "T-5=merged"}
	if got := members(tr); !slices.Equal(got, want) || tr.State != TrainMerged {
		t.Fatalf("members %v, want %v", got, want)
	}
	st := e.states()
	if !e.isAncestor(st["T-3"].MergedSHA, st["T-2"].MergedSHA) || e.isAncestor(st["T-2"].MergedSHA, st["T-3"].MergedSHA) {
		t.Fatal("T-2 merged before its dependency T-3")
	}
	if st["T-4"].State != StateQueued || e.onMain("f4.txt") {
		t.Fatalf("T-4 with an unmet dependency is %s", st["T-4"].State)
	}
	e.idle(q)
}

func TestTrainMergeConflictEvicts(t *testing.T) {
	e := newEnv(t)
	e.branch("stale", "README", "theirs\n")
	e.branch("one", "shared.txt", "one\n")
	e.branch("two", "shared.txt", "two\n")
	e.branch("needs-two", "f3.txt", "x\n")
	e.branch("free", "f4.txt", "x\n")
	e.advanceMain("README", "ours\n")
	run := &trainRunner{}
	q := e.trainQueue(trainOpts{max: 5, auto: true, runner: run, deps: fakeDeps{"T-3": {DependsOn: []string{"T-2"}}}})
	e.enqueue(q, 10, "stale", "T-0")
	e.enqueue(q, 11, "one", "T-1")
	e.enqueue(q, 12, "two", "T-2")
	e.enqueue(q, 13, "needs-two", "T-3")
	e.enqueue(q, 14, "free", "T-4")

	tr := e.train(q)
	want := []string{"T-0=ejected", "T-1=merged", "T-2=ejected", "T-3=requeued", "T-4=merged"}
	if got := members(tr); !slices.Equal(got, want) || tr.State != TrainFailed || run.testRuns() != 1 {
		t.Fatalf("members %v state %s runs %d\n%s", got, tr.State, run.testRuns(), tr.Log)
	}
	if !e.note("T-0", "merge conflict merging stale into main.") {
		t.Fatalf("notes %q", e.rec.notes)
	}
	if !e.note("T-2", "merge conflict merging two into main on top of #11") {
		t.Fatalf("notes %q", e.rec.notes)
	}
	if len(e.rec.changes) != 2 || e.onMain("f3.txt") || !e.onMain("f4.txt") {
		t.Fatalf("changes %q", e.rec.changes)
	}
	if st := e.states(); st["T-3"].State != StateQueued {
		t.Fatalf("T-3 is %s", st["T-3"].State)
	}
	e.idle(q)
}

func TestTrainHonoursOwnerApprovalAndRequiredChecks(t *testing.T) {
	e := newEnv(t)
	for i := 1; i <= 5; i++ {
		e.branch(fmt.Sprintf("pr-%d", i), fmt.Sprintf("f%d.txt", i), "x\n")
	}
	checks := fakeChecks{
		"T-1": {Contexts: map[string]string{"ci": "success", "lint": "success"}},
		"T-2": {Contexts: map[string]string{"ci": "failure", "lint": "success"}},
		"T-3": {Contexts: map[string]string{"ci": "pending", "lint": "success"}},
		"T-5": {Head: strings.Repeat("0", 40), Contexts: map[string]string{"ci": "success", "lint": "success"}},
	}
	run := &trainRunner{}
	q := e.trainQueue(trainOpts{max: 5, required: []string{"ci", "lint"}, checks: checks, runner: run})
	for i := 1; i <= 5; i++ {
		e.enqueue(q, i, fmt.Sprintf("pr-%d", i), fmt.Sprintf("T-%d", i))
	}

	e.idle(q)
	st := e.states()
	for id, c := range st {
		if c.State != StateWaiting || !e.note(id, "waiting for owner approval") {
			t.Fatalf("%s is %s without approval", id, c.State)
		}
	}
	e.idle(q)
	if len(e.rec.notes) != 5 || run.testRuns() != 0 {
		t.Fatalf("notes %q", e.rec.notes)
	}

	e.rec.approved = true
	tr := e.train(q)
	want := []string{"T-1=merged", "T-4=merged", "T-5=requeued"}
	if got := members(tr); !slices.Equal(got, want) {
		t.Fatalf("members %v, want %v", got, want)
	}
	st = e.states()
	if st["T-2"].State != StateEjected || !e.note("T-2", `required status check "ci" is failure`) {
		t.Fatalf("T-2 %s notes %q", st["T-2"].State, e.rec.notes)
	}
	if st["T-3"].State != StateWaiting || st["T-5"].State != StateQueued {
		t.Fatalf("T-3 %s T-5 %s", st["T-3"].State, st["T-5"].State)
	}
	e.idle(q)

	checks["T-3"].Contexts["ci"] = "success"
	checks["T-5"].Head = ""
	tr = e.train(q)
	if got := members(tr); !slices.Equal(got, []string{"T-3=merged", "T-5=merged"}) {
		t.Fatalf("members %v", got)
	}
}

func crashOn(t *testing.T, q *Queue) {
	t.Helper()
	defer func() {
		if r := recover(); r != errCrash {
			t.Fatalf("expected a simulated crash, got %v", r)
		}
	}()
	_, _ = q.ProcessTrain(context.Background(), repo)
}

func TestTrainRecoversAfterCrash(t *testing.T) {
	setup := func(t *testing.T) *env {
		e := newEnv(t)
		for i := 1; i <= 3; i++ {
			e.branch(fmt.Sprintf("pr-%d", i), fmt.Sprintf("f%d.txt", i), "x\n")
		}
		return e
	}
	enqueue := func(e *env, q *Queue) {
		for i := 1; i <= 3; i++ {
			e.enqueue(q, i, fmt.Sprintf("pr-%d", i), fmt.Sprintf("T-%d", i))
		}
	}

	t.Run("half-built", func(t *testing.T) {
		e := setup(t)
		merges := 0
		crashing := &trainRunner{crash: func(args []string) bool {
			if slices.Contains(args, "merge") && slices.Contains(args, "--no-ff") {
				merges++
			}
			return merges == 2
		}}
		oldMain := e.ref("main")
		q := e.trainQueue(trainOpts{max: 3, auto: true, runner: crashing})
		enqueue(e, q)
		crashOn(t, q)
		trains, _ := e.store.ListTrains(context.Background(), repo, 0)
		if len(trains) != 1 || trains[0].State != TrainBuilding || e.ref("main") != oldMain {
			t.Fatalf("after crash: %+v", trains)
		}

		run := &trainRunner{}
		q = e.trainQueue(trainOpts{max: 3, auto: true, runner: run})
		rec := e.train(q)
		if rec.ID != trains[0].ID || rec.State != TrainAborted || run.testRuns() != 0 {
			t.Fatalf("recovered %+v", rec)
		}
		for id, c := range e.states() {
			if c.State != StateQueued || !e.note(id, "was interrupted") {
				t.Fatalf("%s is %s", id, c.State)
			}
		}
		tr := e.train(q)
		if tr.State != TrainMerged || len(tr.Members) != 3 || run.testRuns() != 1 {
			t.Fatalf("rebuilt %+v", tr)
		}
		e.idle(q)
	})

	t.Run("pushed", func(t *testing.T) {
		e := setup(t)
		crashing := &trainRunner{crash: func(args []string) bool { return args[0] == "push" }}
		q := e.trainQueue(trainOpts{max: 3, auto: true, runner: crashing})
		enqueue(e, q)
		crashOn(t, q)
		trains, _ := e.store.ListTrains(context.Background(), repo, 0)
		if len(trains) != 1 || trains[0].State != TrainMerging || e.ref("main") != trains[0].Tip {
			t.Fatalf("after crash: %+v", trains)
		}
		for id, c := range e.states() {
			if c.State != StateRunning {
				t.Fatalf("%s is %s", id, c.State)
			}
		}

		run := &trainRunner{}
		q = e.trainQueue(trainOpts{max: 3, auto: true, runner: run})
		rec := e.train(q)
		if rec.State != TrainMerged || run.testRuns() != 0 || e.ref("main") != trains[0].Tip {
			t.Fatalf("recovered %+v", rec)
		}
		for _, c := range e.states() {
			if c.State != StateMerged || !e.note(c.TaskID, "merged into main") {
				t.Fatalf("%s is %s", c.TaskID, c.State)
			}
		}
		e.idle(q)
	})
}

func TestTrainRunDrainsRepo(t *testing.T) {
	e := newEnv(t)
	for i := 1; i <= 4; i++ {
		e.branch(fmt.Sprintf("pr-%d", i), fmt.Sprintf("f%d.txt", i), "x\n")
	}
	run := &trainRunner{}
	q := e.trainQueue(trainOpts{max: 2, auto: true, runner: run})
	for i := 1; i <= 4; i++ {
		e.enqueue(q, i, fmt.Sprintf("pr-%d", i), fmt.Sprintf("T-%d", i))
	}
	for range 2 {
		if c, err := q.Process(context.Background(), repo); c == nil || err != nil || c.State != StateMerged {
			t.Fatalf("process %+v %v", c, err)
		}
	}
	if c, err := q.Process(context.Background(), repo); c != nil || err != nil {
		t.Fatalf("queue not drained: %+v %v", c, err)
	}
	if run.testRuns() != 2 {
		t.Fatalf("%d test runs", run.testRuns())
	}
}
