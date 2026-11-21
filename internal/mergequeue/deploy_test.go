package mergequeue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type deployRunner struct {
	trainRunner
	fail    bool
	block   bool
	deploys []string
	files   []bool
}

func (d *deployRunner) Run(ctx context.Context, dir, name string, args ...string) (string, error) {
	if name != "deploy" {
		return d.trainRunner.Run(ctx, dir, name, args...)
	}
	_, err1 := os.Stat(filepath.Join(dir, "f1.txt"))
	_, err2 := os.Stat(filepath.Join(dir, "f2.txt"))
	d.mu.Lock()
	d.deploys = append(d.deploys, strings.Join(args, " "))
	d.files = append(d.files, err1 == nil && err2 == nil)
	d.mu.Unlock()
	switch {
	case d.block:
		<-ctx.Done()
		return "waiting for rollout\n", ctx.Err()
	case d.fail:
		return "rollout boom-deploy https://bot:hunter2@deploy.example/x\n", errors.New("exit status 2")
	}
	return "rolled out\n", nil
}

type deployRecorder struct {
	mu  sync.Mutex
	got []Deployment
}

func (r *deployRecorder) Deployed(_ context.Context, d Deployment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, d)
	return nil
}

func (e *env) deployQueue(run Runner, deploys Deploys, rc RepoConfig) *Queue {
	e.t.Helper()
	rc.Remote, rc.Verify, rc.Auto = e.remote, []string{"verify"}, true
	q, err := New(Config{
		Repos:    map[string]RepoConfig{repo: rc},
		WorkDir:  filepath.Join(e.root, "work"),
		Store:    e.store,
		Runner:   run,
		Notifier: e.rec,
		Gate:     e.rec,
		Tasks:    e.rec,
		Deploys:  deploys,
		Now:      func() time.Time { return trainClock },
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return q
}

func (e *env) twoPRTrain(run *deployRunner, deploys Deploys, deploy []string, timeout time.Duration) *Train {
	e.t.Helper()
	e.branch("pr-1", "f1.txt", "x\n")
	e.branch("pr-2", "f2.txt", "x\n")
	q := e.deployQueue(run, deploys, RepoConfig{MaxTrain: 4, Deploy: deploy, DeployTimeout: timeout})
	e.enqueue(q, 1, "pr-1", "T-1")
	e.enqueue(q, 2, "pr-2", "T-2")
	tr := e.train(q)
	if tr.State != TrainMerged || !e.onMain("f1.txt") || !e.onMain("f2.txt") {
		e.t.Fatalf("train %s members %v\n%s", tr.State, members(tr), tr.Log)
	}
	return tr
}

func TestTrainDeploySucceeds(t *testing.T) {
	e := newEnv(t)
	run, rec := &deployRunner{}, &deployRecorder{}
	tr := e.twoPRTrain(run, rec, []string{"deploy", "prod"}, 0)

	if tr.DeployState != DeploySucceeded || !strings.Contains(tr.DeployLog, "rolled out") {
		t.Fatalf("deploy %s\n%s", tr.DeployState, tr.DeployLog)
	}
	if !slices.Equal(run.deploys, []string{"prod"}) || !run.files[0] {
		t.Fatalf("deploys %v files %v", run.deploys, run.files)
	}
	if len(rec.got) != 1 {
		t.Fatalf("deployments %+v", rec.got)
	}
	d := rec.got[0]
	if d.State != DeploySucceeded || d.TrainID != tr.ID || d.SHA != tr.Tip || !slices.Equal(d.Tasks, []string{"T-1", "T-2"}) || !slices.Equal(d.PRs, []int{1, 2}) {
		t.Fatalf("deployment %+v", d)
	}
	for _, task := range []string{"T-1", "T-2"} {
		if !e.note(task, "deployed merge train "+tr.ID) {
			t.Fatalf("notes %q", e.rec.notes)
		}
	}
	stored, err := e.store.GetTrain(context.Background(), tr.ID)
	if err != nil || stored.DeployState != DeploySucceeded {
		t.Fatalf("stored %+v %v", stored, err)
	}
}

func TestTrainDeployFailurePostsLogTailAndReports(t *testing.T) {
	e := newEnv(t)
	run, rec := &deployRunner{fail: true}, &deployRecorder{}
	tr := e.twoPRTrain(run, rec, []string{"deploy", "prod"}, 0)

	if tr.DeployState != DeployFailed || !strings.Contains(tr.DeployLog, "boom-deploy") || strings.Contains(tr.DeployLog, "hunter2") {
		t.Fatalf("deploy %s\n%s", tr.DeployState, tr.DeployLog)
	}
	for _, task := range []string{"T-1", "T-2"} {
		if !e.note(task, "failed; the merge stays") || !e.note(task, "boom-deploy") {
			t.Fatalf("notes %q", e.rec.notes)
		}
	}
	if slices.ContainsFunc(e.rec.notes, func(n string) bool { return strings.Contains(n, "hunter2") }) {
		t.Fatalf("credentials leaked: %q", e.rec.notes)
	}
	if len(rec.got) != 1 || rec.got[0].State != DeployFailed || !strings.Contains(rec.got[0].Log, "boom-deploy") {
		t.Fatalf("deployments %+v", rec.got)
	}
	if st := e.states(); st["T-1"].State != StateMerged || st["T-2"].State != StateMerged || len(e.rec.changes) != 0 {
		t.Fatalf("states %v changes %q", st, e.rec.changes)
	}
}

func TestTrainDeployTimesOut(t *testing.T) {
	e := newEnv(t)
	rec := &deployRecorder{}
	tr := e.twoPRTrain(&deployRunner{block: true}, rec, []string{"deploy", "prod"}, 50*time.Millisecond)

	if tr.DeployState != DeployFailed || !strings.Contains(tr.DeployLog, "timed out after 50ms") {
		t.Fatalf("deploy %s\n%s", tr.DeployState, tr.DeployLog)
	}
	if len(rec.got) != 1 || rec.got[0].State != DeployFailed {
		t.Fatalf("deployments %+v", rec.got)
	}
}

func TestReposWithoutDeployCommandAreUnaffected(t *testing.T) {
	e := newEnv(t)
	run, rec := &deployRunner{}, &deployRecorder{}
	tr := e.twoPRTrain(run, rec, nil, 0)

	if tr.DeployState != "" || tr.DeployLog != "" || len(run.deploys) != 0 || len(rec.got) != 0 {
		t.Fatalf("train %+v deploys %v reported %+v", tr, run.deploys, rec.got)
	}
	if e.note("T-1", "deploy") {
		t.Fatalf("notes %q", e.rec.notes)
	}
}

func TestSingleCandidateDeploy(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint("fail=", fail), func(t *testing.T) {
			e := newEnv(t)
			e.branch("feature", "f1.txt", "x\n")
			run, rec := &deployRunner{fail: fail}, &deployRecorder{}
			q := e.deployQueue(run, rec, RepoConfig{Deploy: []string{"deploy", "prod"}})
			e.enqueue(q, 7, "feature", "T-1")

			c := e.process(q)
			want := DeploySucceeded
			if fail {
				want = DeployFailed
			}
			if c.State != StateMerged || c.DeployState != want || e.ref("main") != c.MergedSHA {
				t.Fatalf("candidate %+v", c)
			}
			stored, err := e.store.Get(context.Background(), c.ID)
			if err != nil || stored.DeployState != want {
				t.Fatalf("stored %+v %v", stored, err)
			}
			if len(rec.got) != 1 || rec.got[0].State != want || rec.got[0].SHA != c.MergedSHA || !slices.Equal(rec.got[0].Candidates, []string{c.ID}) {
				t.Fatalf("deployments %+v", rec.got)
			}
			if fail != e.note("T-1", "boom-deploy") {
				t.Fatalf("notes %q", e.rec.notes)
			}
		})
	}
}
