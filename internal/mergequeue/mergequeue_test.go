package mergequeue

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const repo = "acme/app"

type recorder struct {
	mu       sync.Mutex
	approved bool
	notes    []string
	changes  []string
}

func (r *recorder) Notify(_ context.Context, taskID, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notes = append(r.notes, taskID+": "+body)
	return nil
}

func (r *recorder) Approved(context.Context, Candidate) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.approved, nil
}

func (r *recorder) RequestChanges(_ context.Context, taskID, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changes = append(r.changes, taskID+": "+reason)
	return nil
}

type env struct {
	t      *testing.T
	root   string
	remote string
	seed   string
	rec    *recorder
	store  *MemStore
}

func newEnv(t *testing.T) *env {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.invalid")
	root := t.TempDir()
	e := &env{t: t, root: root, remote: filepath.Join(root, "remote.git"), seed: filepath.Join(root, "seed"),
		rec: &recorder{}, store: NewMemStore()}
	e.git(root, "init", "-q", "--bare", "-b", "main", e.remote)
	e.git(root, "clone", "-q", e.remote, e.seed)
	e.git(e.seed, "symbolic-ref", "HEAD", "refs/heads/main")
	e.commit("README", "hello\n")
	e.git(e.seed, "push", "-q", "origin", "main")
	return e
}

func (e *env) git(dir string, args ...string) string {
	e.t.Helper()
	out, err := ExecRunner{}.Run(context.Background(), dir, "git", args...)
	if err != nil {
		e.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(out)
}

func (e *env) commit(file, content string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.seed, file), []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
	e.git(e.seed, "add", file)
	e.git(e.seed, "commit", "-q", "-m", "change "+file)
}

func (e *env) branch(name, file, content string) {
	e.t.Helper()
	e.git(e.seed, "checkout", "-q", "-b", name, "main")
	e.commit(file, content)
	e.git(e.seed, "push", "-q", "origin", name)
	e.git(e.seed, "checkout", "-q", "main")
}

func (e *env) advanceMain(file, content string) {
	e.t.Helper()
	e.git(e.seed, "checkout", "-q", "main")
	e.commit(file, content)
	e.git(e.seed, "push", "-q", "origin", "main")
}

func (e *env) ref(name string) string {
	return e.git(e.remote, "rev-parse", "refs/heads/"+name)
}

func (e *env) queue(verify string, auto bool) *Queue {
	e.t.Helper()
	q, err := New(Config{
		Repos:    map[string]RepoConfig{repo: {Remote: e.remote, Verify: []string{"sh", "-c", verify}, Auto: auto}},
		WorkDir:  filepath.Join(e.root, "work"),
		Store:    e.store,
		Notifier: e.rec,
		Gate:     e.rec,
		Tasks:    e.rec,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return q
}

func (e *env) enqueue(q *Queue, pr int, head, task string) Candidate {
	e.t.Helper()
	c, err := q.Enqueue(context.Background(), Candidate{Repo: repo, PR: pr, Head: head, Base: "main", TaskID: task})
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *env) process(q *Queue) *Candidate {
	e.t.Helper()
	c, err := q.Process(context.Background(), repo)
	if err != nil {
		e.t.Fatalf("process: %v", err)
	}
	if c == nil {
		e.t.Fatal("process: nothing processed")
	}
	return c
}

func (e *env) isAncestor(a, b string) bool {
	_, err := ExecRunner{}.Run(context.Background(), e.remote, "git", "merge-base", "--is-ancestor", a, b)
	return err == nil
}

func TestMergeRebasesAndFastForwardsBase(t *testing.T) {
	e := newEnv(t)
	e.branch("feature", "feature.txt", "x\n")
	e.advanceMain("main.txt", "y\n")
	oldMain := e.ref("main")
	q := e.queue("test -f feature.txt && test -f main.txt", true)
	e.enqueue(q, 7, "feature", "T-1")

	c := e.process(q)
	if c.State != StateMerged {
		t.Fatalf("state %s, log:\n%s", c.State, c.Log)
	}
	if got := e.ref("main"); got != c.MergedSHA || e.ref("feature") != got {
		t.Fatalf("main %s feature %s merged %s", got, e.ref("feature"), c.MergedSHA)
	}
	if !e.isAncestor(oldMain, c.MergedSHA) {
		t.Fatal("base was not fast-forwarded")
	}
	if len(e.rec.notes) != 1 || !strings.Contains(e.rec.notes[0], "T-1: ") || !strings.Contains(e.rec.notes[0], "merged") {
		t.Fatalf("notes %q", e.rec.notes)
	}
	if len(e.rec.changes) != 0 {
		t.Fatalf("changes %q", e.rec.changes)
	}
	if c, err := q.Process(context.Background(), repo); c != nil || err != nil {
		t.Fatalf("queue not empty: %+v %v", c, err)
	}
}

func TestVerifyFailureEjects(t *testing.T) {
	e := newEnv(t)
	e.branch("feature", "feature.txt", "x\n")
	oldMain := e.ref("main")
	q := e.queue("echo boom-from-verify; exit 3", true)
	e.enqueue(q, 7, "feature", "T-1")

	c := e.process(q)
	if c.State != StateEjected || !strings.Contains(c.Log, "boom-from-verify") {
		t.Fatalf("state %s, log:\n%s", c.State, c.Log)
	}
	if e.ref("main") != oldMain {
		t.Fatal("base moved after failed verify")
	}
	if len(e.rec.changes) != 1 || !strings.HasPrefix(e.rec.changes[0], "T-1: ") {
		t.Fatalf("changes %q", e.rec.changes)
	}
	if len(e.rec.notes) != 1 || !strings.Contains(e.rec.notes[0], "boom-from-verify") {
		t.Fatalf("notes %q", e.rec.notes)
	}
}

func TestRebaseConflictEjectsAndNextCandidateMerges(t *testing.T) {
	e := newEnv(t)
	e.branch("conflict", "README", "theirs\n")
	e.branch("good", "good.txt", "ok\n")
	e.advanceMain("README", "ours\n")
	oldMain := e.ref("main")
	q := e.queue("true", true)
	e.enqueue(q, 1, "conflict", "T-1")
	e.enqueue(q, 2, "good", "T-2")

	c := e.process(q)
	if c.State != StateEjected || c.TaskID != "T-1" {
		t.Fatalf("first %+v", c)
	}
	if e.ref("main") != oldMain {
		t.Fatal("base moved after conflict")
	}
	c = e.process(q)
	if c.State != StateMerged || c.TaskID != "T-2" {
		t.Fatalf("second state %s, log:\n%s", c.State, c.Log)
	}
	if len(e.rec.changes) != 1 || !strings.HasPrefix(e.rec.changes[0], "T-1: ") {
		t.Fatalf("changes %q", e.rec.changes)
	}
}

func TestGateHoldsMergeUntilApproved(t *testing.T) {
	e := newEnv(t)
	e.branch("feature", "feature.txt", "x\n")
	oldMain := e.ref("main")
	q := e.queue("true", false)
	e.enqueue(q, 7, "feature", "T-1")

	c := e.process(q)
	if c.State != StateWaiting || e.ref("main") != oldMain {
		t.Fatalf("state %s, main moved %v", c.State, e.ref("main") != oldMain)
	}
	if c, err := q.Process(context.Background(), repo); c != nil || err != nil {
		t.Fatalf("unapproved candidate processed: %+v %v", c, err)
	}

	e.advanceMain("main.txt", "y\n")
	e.rec.approved = true
	c = e.process(q)
	if c.State != StateMerged || e.ref("main") != c.MergedSHA {
		t.Fatalf("state %s, log:\n%s", c.State, c.Log)
	}
	if len(e.rec.notes) != 2 || !strings.Contains(e.rec.notes[0], "waiting for owner approval") {
		t.Fatalf("notes %q", e.rec.notes)
	}
}

func TestBaseMovedDuringVerifyIsNeverOverwritten(t *testing.T) {
	e := newEnv(t)
	e.branch("feature", "feature.txt", "x\n")
	other := filepath.Join(e.root, "other")
	e.git(e.root, "clone", "-q", e.remote, other)
	marker := filepath.Join(e.root, "raced")
	race := fmt.Sprintf("[ -e '%s' ] || { touch '%s' && git -C '%s' commit -q --allow-empty -m race && git -C '%s' push -q origin HEAD:main; }",
		marker, marker, other, other)
	q := e.queue(race, true)
	e.enqueue(q, 7, "feature", "T-1")

	c, err := q.Process(context.Background(), repo)
	if err == nil || c == nil || c.State != StateQueued {
		t.Fatalf("expected rejected push and requeue, got %+v %v", c, err)
	}
	raced := e.git(other, "rev-parse", "HEAD")
	if e.ref("main") != raced {
		t.Fatal("concurrent base commit was overwritten")
	}

	c = e.process(q)
	if c.State != StateMerged || !e.isAncestor(raced, e.ref("main")) {
		t.Fatalf("state %s, log:\n%s", c.State, c.Log)
	}
	if len(e.rec.changes) != 0 {
		t.Fatalf("changes %q", e.rec.changes)
	}
}

func TestRunDrainsQueueInOrder(t *testing.T) {
	e := newEnv(t)
	e.branch("one", "one.txt", "1\n")
	e.branch("two", "two.txt", "2\n")
	q := e.queue("true", true)
	e.enqueue(q, 1, "one", "T-1")
	e.enqueue(q, 2, "two", "T-2")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		q.Run(ctx, 10*time.Millisecond)
		close(done)
	}()
	deadline := time.Now().Add(30 * time.Second)
	for {
		list, err := e.store.List(context.Background(), repo)
		if err != nil {
			t.Fatal(err)
		}
		if list[0].State == StateMerged && list[1].State == StateMerged {
			if !e.isAncestor(list[0].MergedSHA, list[1].MergedSHA) || e.ref("main") != list[1].MergedSHA {
				t.Fatal("second candidate not rebased on first")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not drained: %s %s", list[0].State, list[1].State)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
}

func TestVerifyDoesNotSeeServerSecrets(t *testing.T) {
	e := newEnv(t)
	t.Setenv("SUBSPACE_OWNER_TOKEN", "owner-secret-0123456789abcdef")
	e.branch("feature", "feature.txt", "x\n")
	q := e.queue(`echo "owner=$SUBSPACE_OWNER_TOKEN"; exit 1`, true)
	e.enqueue(q, 7, "feature", "T-1")

	c := e.process(q)
	if c.State != StateEjected || !strings.Contains(c.Log, "owner=") {
		t.Fatalf("state %s, log:\n%s", c.State, c.Log)
	}
	for _, n := range append(e.rec.notes, c.Log) {
		if strings.Contains(n, "owner-secret") {
			t.Fatalf("owner token leaked: %s", n)
		}
	}
}

func TestRemoteCredentialsNotLogged(t *testing.T) {
	e := newEnv(t)
	q, err := New(Config{
		Repos:    map[string]RepoConfig{repo: {Remote: "https://bot:s3cret-pw@127.0.0.1:1/acme/app.git", Verify: []string{"true"}, Auto: true}},
		WorkDir:  filepath.Join(e.root, "work"),
		Store:    e.store,
		Notifier: e.rec,
		Gate:     e.rec,
		Tasks:    e.rec,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.enqueue(q, 7, "feature", "T-1")
	if _, err := q.Process(context.Background(), repo); err == nil {
		t.Fatal("expected fetch error")
	}
	list, err := e.store.List(context.Background(), repo)
	if err != nil || len(list) != 1 {
		t.Fatalf("%v %v", list, err)
	}
	if !strings.Contains(list[0].Log, "remote add origin") || strings.Contains(list[0].Log, "s3cret-pw") {
		t.Fatalf("log:\n%s", list[0].Log)
	}
}

func TestExecRunnerKeepsBoundedTail(t *testing.T) {
	out, err := ExecRunner{}.Run(context.Background(), t.TempDir(), "sh", "-c", `head -c 1000000 /dev/zero | tr '\000' x; echo END`)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > maxLog || !strings.HasSuffix(out, "xEND\n") {
		t.Fatalf("len %d, tail %q", len(out), out[max(len(out)-10, 0):])
	}
}

func TestExecRunnerReturnsWhenBackgroundChildKeepsOutputOpen(t *testing.T) {
	start := time.Now()
	out, err := ExecRunner{}.Run(context.Background(), t.TempDir(), "sh", "-c", "echo ok; sleep 10 &")
	if err != nil || strings.TrimSpace(out) != "ok" {
		t.Fatalf("%q %v", out, err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("run blocked %v on a detached child", d)
	}
}
