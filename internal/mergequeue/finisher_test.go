package mergequeue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/githost/github"
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

type fakeGitHub struct {
	mu       sync.Mutex
	e        *env
	detect   bool
	state    string
	closed   int
	requests []string
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	if r.URL.Path != "/repos/"+repo+"/pulls/7" || r.Header.Get("Authorization") != "Bearer gh-test" {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPatch {
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"state":"closed"}` {
			http.Error(w, "bad body", http.StatusUnprocessableEntity)
			return
		}
		f.state = "closed"
		f.closed++
		_, _ = w.Write([]byte(`{}`))
		return
	}
	merged := false
	if f.detect && f.state == "open" {
		merged = f.e.isAncestor(f.e.ref("feature"), "refs/heads/main")
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"number": 7, "state": f.state, "merged": merged,
		"head": map[string]string{"ref": "feature"}, "base": map[string]string{"ref": "main"}})
}

func TestGitHubFinisherEndsWithThePRMergedOrClosed(t *testing.T) {
	for _, detect := range []bool{true, false} {
		t.Run(fmt.Sprint("detect=", detect), func(t *testing.T) {
			e := newEnv(t)
			e.branch("feature", "feature.txt", "x\n")
			gh := &fakeGitHub{e: e, detect: detect, state: "open"}
			srv := httptest.NewServer(gh)
			defer srv.Close()
			client := github.New(github.Config{APIURL: srv.URL, Token: "gh-test"})
			q := e.queueWithFinisher(Finishers{repo: GitHubFinisher{Client: client, Settle: 50 * time.Millisecond, Poll: 10 * time.Millisecond}})
			e.enqueue(q, 7, "feature", "T-1")

			c := e.process(q)
			if c.State != StateMerged || strings.Contains(e.rec.notes[0], "Warning") {
				t.Fatalf("state %s notes %q", c.State, e.rec.notes)
			}
			pr, err := client.GetPR(context.Background(), github.PRRef{Repo: repo, Number: 7})
			if err != nil {
				t.Fatal(err)
			}
			if detect && (!pr.Merged || gh.closed != 0) || !detect && (pr.State != "closed" || gh.closed != 1) {
				t.Fatalf("pr %+v closed %d requests %v", pr, gh.closed, gh.requests)
			}
		})
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
