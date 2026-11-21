package mergequeue

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

type finisherCall struct {
	repo string
	pr   int
	sha  string
}

type recordingFinisher struct {
	calls []finisherCall
	err   error
}

func (f *recordingFinisher) Merged(_ context.Context, repo string, pr int, sha string) error {
	f.calls = append(f.calls, finisherCall{repo, pr, sha})
	return f.err
}

func (e *env) queueWithFinisher(f Finisher) *Queue {
	e.t.Helper()
	q, err := New(Config{
		Repos:    map[string]RepoConfig{repo: {Remote: e.remote, Verify: []string{"true"}, Auto: true}},
		WorkDir:  filepath.Join(e.root, "work"),
		Store:    e.store,
		Notifier: e.rec,
		Gate:     e.rec,
		Tasks:    e.rec,
		Finisher: f,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return q
}

func TestFinisherRunsAfterThePush(t *testing.T) {
	e := newEnv(t)
	e.branch("feature", "feature.txt", "x\n")
	f := &recordingFinisher{}
	q := e.queueWithFinisher(f)
	e.enqueue(q, 7, "feature", "T-1")

	c := e.process(q)
	if c.State != StateMerged || len(f.calls) != 1 || f.calls[0] != (finisherCall{repo, 7, e.ref("main")}) {
		t.Fatalf("state %s, finisher calls %+v", c.State, f.calls)
	}
	if strings.Contains(e.rec.notes[0], "Warning") {
		t.Fatalf("note: %s", e.rec.notes[0])
	}
}

func TestFinisherFailureKeepsTheMergeAndWarns(t *testing.T) {
	e := newEnv(t)
	e.branch("feature", "feature.txt", "x\n")
	f := &recordingFinisher{err: errors.New("host said no via https://bot:secret@forge.example/x")}
	q := e.queueWithFinisher(f)
	e.enqueue(q, 7, "feature", "T-1")

	c := e.process(q)
	if c.State != StateMerged || e.ref("main") != c.MergedSHA {
		t.Fatalf("state %s main %s merged %s", c.State, e.ref("main"), c.MergedSHA)
	}
	note := e.rec.notes[0]
	if !strings.Contains(note, "could not be marked merged") || !strings.Contains(note, "host said no") || strings.Contains(note, "secret") {
		t.Fatalf("note: %s", note)
	}
}
