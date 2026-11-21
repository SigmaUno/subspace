package reviewsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/githost"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/ratelimit"
)

type comment struct {
	repo   string
	number int64
	body   string
}

type fakeHost struct {
	mu       sync.Mutex
	next     int64
	comments map[int64]*comment
	creates  int
	updates  int
	failNext []error
}

func newFakeHost() *fakeHost { return &fakeHost{next: 100, comments: map[int64]*comment{}} }

func (f *fakeHost) fail() error {
	if len(f.failNext) == 0 {
		return nil
	}
	err := f.failNext[0]
	f.failNext = f.failNext[1:]
	return err
}

func (f *fakeHost) CreateComment(_ context.Context, repo string, number int64, body string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(); err != nil {
		return 0, err
	}
	f.next++
	f.creates++
	f.comments[f.next] = &comment{repo, number, body}
	return f.next, nil
}

func (f *fakeHost) UpdateComment(_ context.Context, repo string, id int64, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(); err != nil {
		return err
	}
	c, ok := f.comments[id]
	if !ok || c.repo != repo {
		return githost.ErrNotFound
	}
	f.updates++
	c.body = body
	return nil
}

func (f *fakeHost) only(t *testing.T) (int64, *comment) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.comments) != 1 {
		t.Fatalf("comments = %d, want 1", len(f.comments))
	}
	for id, c := range f.comments {
		return id, c
	}
	return 0, nil
}

type memStore struct {
	mu      sync.Mutex
	rows    map[string]Record
	setErrs int
}

func (m *memStore) ReviewComment(_ context.Context, id string) (Record, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	return r, ok, nil
}

func (m *memStore) SetReviewComment(_ context.Context, id string, r Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.setErrs > 0 {
		m.setErrs--
		return errors.New("disk full")
	}
	m.rows[id] = r
	return nil
}

type taskSource map[string]*model.Task

func (ts taskSource) GetTask(_ context.Context, _ model.Actor, id string) (*model.Task, error) {
	if t, ok := ts[id]; ok {
		return t, nil
	}
	return nil, model.NotFound("not found")
}

type rig struct {
	t     *testing.T
	host  *fakeHost
	store *memStore
	tasks taskSource
	clock *ratelimit.FakeClock
	svc   *Service
	msgID int64
}

func prURL(n int) string { return fmt.Sprintf("https://git.test/o/r/pull/%d", n) }

func newRig(t *testing.T, mode Mode) *rig {
	t.Helper()
	url := prURL(7)
	r := &rig{
		t: t, host: newFakeHost(), store: &memStore{rows: map[string]Record{}},
		tasks: taskSource{"T-1": {ID: "T-1", Title: "Add login", Repo: "o/r", PRURL: &url, AcceptanceCriteria: []string{"logs in", "logs out"}, ReviewRounds: 1}},
		clock: ratelimit.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	}
	r.svc = r.service(mode, r.store)
	return r
}

func (r *rig) service(mode Mode, st Store) *Service {
	r.t.Helper()
	s, err := New(Options{
		Tasks: r.tasks, Store: st, Mode: mode, Clock: r.clock, PublicURL: "https://sub.test/",
		Hosts: map[string]Host{"git": {Web: "git.test", Commenter: r.host}}, Repos: map[string]string{"o/r": "git"},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Attempts: 3, BaseDelay: time.Second, MaxDelay: 8 * time.Second,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return s
}

func (r *rig) review(verdict, comments string) {
	r.t.Helper()
	r.msgID++
	ok := r.svc.Enqueue(model.Message{
		ID: r.msgID, ThreadKind: model.ThreadTask, ThreadID: "T-1", AuthorID: "rev", AuthorRole: model.RoleReviewer,
		Kind: model.MsgNote, Body: "Review (" + verdict + "): " + comments,
	})
	if !ok {
		r.t.Fatalf("review message was not accepted")
	}
}

func (r *rig) run() {
	r.t.Helper()
	r.svc.Process(context.Background())
}

func TestFirstReviewCreatesAndLaterRoundsUpdateSameComment(t *testing.T) {
	r := newRig(t, ModeChangesRequested)
	r.review(VerdictChangesRequested, "criterion 1: pass\nlogout is broken")
	r.run()
	id, c := r.host.only(t)
	if c.repo != "o/r" || c.number != 7 || !strings.HasPrefix(c.body, Marker("T-1")+"\n") ||
		!strings.Contains(c.body, "[T-1](https://sub.test/tasks/T-1)") || !strings.Contains(c.body, "logout is broken") {
		t.Fatalf("comment: %+v", c)
	}
	if rec := r.store.rows["T-1"]; rec.CommentID != id || rec.PRURL != prURL(7) || rec.MessageID != 1 {
		t.Fatalf("record: %+v", rec)
	}

	r.tasks["T-1"].ReviewRounds = 2
	r.review(VerdictChangesRequested, "still broken")
	r.run()
	_, c = r.host.only(t)
	if r.host.creates != 1 || r.host.updates != 1 || !strings.Contains(c.body, "Round 2") || !strings.Contains(c.body, "still broken") ||
		strings.Contains(c.body, "logout is broken") {
		t.Fatalf("creates=%d updates=%d body=%s", r.host.creates, r.host.updates, c.body)
	}
	if strings.Count(c.body, "<!--") != 1 {
		t.Fatalf("marker duplicated: %s", c.body)
	}
}

func TestRepeatedAndUnchangedReviewsDoNotTouchTheHost(t *testing.T) {
	r := newRig(t, ModeAll)
	r.review(VerdictChangesRequested, "fix it")
	r.run()
	r.svc.Enqueue(model.Message{ID: 1, ThreadKind: model.ThreadTask, ThreadID: "T-1", AuthorRole: model.RoleReviewer, Kind: model.MsgNote, Body: "Review (changes_requested): fix it"})
	r.run()
	r.review(VerdictChangesRequested, "fix it")
	r.run()
	if r.host.creates != 1 || r.host.updates != 0 {
		t.Fatalf("creates=%d updates=%d", r.host.creates, r.host.updates)
	}
	if rec := r.store.rows["T-1"]; rec.MessageID != 2 {
		t.Fatalf("message id not advanced: %+v", rec)
	}
}

func TestDeletedCommentIsRecreatedAndNewPRGetsItsOwn(t *testing.T) {
	r := newRig(t, ModeAll)
	r.review(VerdictChangesRequested, "one")
	r.run()
	first, _ := r.host.only(t)
	r.host.mu.Lock()
	delete(r.host.comments, first)
	r.host.mu.Unlock()
	r.review(VerdictChangesRequested, "two")
	r.run()
	second, _ := r.host.only(t)
	if second == first || r.store.rows["T-1"].CommentID != second || r.host.creates != 2 {
		t.Fatalf("recreate: first=%d second=%d creates=%d", first, second, r.host.creates)
	}

	url := prURL(8)
	r.tasks["T-1"].PRURL = &url
	r.review(VerdictChangesRequested, "three")
	r.run()
	r.host.mu.Lock()
	defer r.host.mu.Unlock()
	if len(r.host.comments) != 2 || r.host.creates != 3 || r.host.comments[second].number != 7 {
		t.Fatalf("new pr: %d comments, %d creates", len(r.host.comments), r.host.creates)
	}
	if rec := r.store.rows["T-1"]; rec.PRURL != url || rec.CommentID == second {
		t.Fatalf("record not moved to the new pr: %+v", rec)
	}
}

func TestApproveRespectsMode(t *testing.T) {
	r := newRig(t, ModeChangesRequested)
	r.review(VerdictApprove, "lgtm")
	r.run()
	if r.host.creates != 0 || len(r.store.rows) != 0 {
		t.Fatalf("approve alone created a comment")
	}
	r.review(VerdictChangesRequested, "nope")
	r.run()
	r.review(VerdictApprove, "now fine")
	r.run()
	_, c := r.host.only(t)
	if r.host.updates != 1 || !strings.Contains(c.body, "approved") || strings.Contains(c.body, "changes requested") {
		t.Fatalf("stale comment after approval: %s", c.body)
	}

	all := newRig(t, ModeAll)
	all.review(VerdictApprove, "lgtm")
	all.run()
	if _, c := all.host.only(t); !strings.Contains(c.body, "approved") {
		t.Fatalf("mode all: %s", c.body)
	}
}

func TestRetryBacksOffWithInjectedClockAndGivesUp(t *testing.T) {
	r := newRig(t, ModeAll)
	r.host.failNext = []error{&githost.StatusError{Status: 502}, &githost.StatusError{Status: 429, RetryAfter: 5 * time.Second}}
	r.review(VerdictChangesRequested, "retry me")
	next, ok := r.svc.Process(context.Background())
	if !ok || !next.Equal(r.clock.Now().Add(time.Second)) || r.host.creates != 0 {
		t.Fatalf("after first failure: next=%v ok=%v creates=%d", next, ok, r.host.creates)
	}
	r.run()
	if r.host.creates != 0 {
		t.Fatalf("retried before the delay")
	}
	r.clock.Advance(time.Second)
	next, ok = r.svc.Process(context.Background())
	if !ok || !next.Equal(r.clock.Now().Add(5*time.Second)) {
		t.Fatalf("Retry-After ignored: next=%v", next)
	}
	r.clock.Advance(5 * time.Second)
	if _, ok := r.svc.Process(context.Background()); ok || r.host.creates != 1 || r.svc.Pending() != 0 {
		t.Fatalf("not delivered: creates=%d pending=%d", r.host.creates, r.svc.Pending())
	}

	g := newRig(t, ModeAll)
	g.host.failNext = []error{&githost.StatusError{Status: 500}, &githost.StatusError{Status: 500}, &githost.StatusError{Status: 500}}
	g.review(VerdictChangesRequested, "x")
	for range 5 {
		g.run()
		g.clock.Advance(10 * time.Second)
	}
	if g.host.creates != 0 || g.svc.Pending() != 0 {
		t.Fatalf("did not give up: creates=%d pending=%d", g.host.creates, g.svc.Pending())
	}

	p := newRig(t, ModeAll)
	p.host.failNext = []error{&githost.StatusError{Status: 403}}
	p.review(VerdictChangesRequested, "x")
	p.run()
	if p.svc.Pending() != 0 {
		t.Fatalf("permanent error retried")
	}
}

func TestStoreFailureAfterPostingDoesNotDuplicateTheComment(t *testing.T) {
	r := newRig(t, ModeAll)
	r.store.setErrs = 1
	r.review(VerdictChangesRequested, "x")
	r.run()
	if r.svc.Pending() != 1 || r.host.creates != 1 {
		t.Fatalf("pending=%d creates=%d", r.svc.Pending(), r.host.creates)
	}
	r.clock.Advance(time.Second)
	r.run()
	id, _ := r.host.only(t)
	if r.host.creates != 1 || r.store.rows["T-1"].CommentID != id {
		t.Fatalf("creates=%d record=%+v", r.host.creates, r.store.rows["T-1"])
	}
}

func TestCommentIdsSurviveARestart(t *testing.T) {
	r := newRig(t, ModeAll)
	r.review(VerdictChangesRequested, "one")
	r.run()
	id, _ := r.host.only(t)

	again := r.service(ModeAll, r.store)
	r.svc = again
	r.msgID = 1
	r.review(VerdictChangesRequested, "two")
	r.run()
	got, c := r.host.only(t)
	if got != id || r.host.creates != 1 || r.host.updates != 1 || !strings.Contains(c.body, "two") {
		t.Fatalf("restart: id %d -> %d creates=%d updates=%d", id, got, r.host.creates, r.host.updates)
	}
}

func TestEnqueueOnlyAcceptsReviewerVerdictMessages(t *testing.T) {
	r := newRig(t, ModeAll)
	base := model.Message{ID: 5, ThreadKind: model.ThreadTask, ThreadID: "T-1", AuthorID: "rev", AuthorRole: model.RoleReviewer, Kind: model.MsgNote, Body: "Review (approve): ok"}
	if !r.svc.Enqueue(base) {
		t.Fatal("valid message rejected")
	}
	for name, mut := range map[string]func(*model.Message){
		"worker author":  func(m *model.Message) { m.AuthorRole = model.RoleWorker },
		"owner author":   func(m *model.Message) { m.AuthorRole = model.RoleOwner },
		"agent thread":   func(m *model.Message) { m.ThreadKind = model.ThreadAgent },
		"system kind":    func(m *model.Message) { m.Kind = model.MsgSystem },
		"other prefix":   func(m *model.Message) { m.Body = "Looks good" },
		"unknown":        func(m *model.Message) { m.Body = "Review (maybe): ok" },
		"bad task id":    func(m *model.Message) { m.ThreadID = "T-1/../x" },
		"no message id":  func(m *model.Message) { m.ID = 0 },
		"forged suffix":  func(m *model.Message) { m.Body = "note: Review (approve): ok" },
		"empty verdict":  func(m *model.Message) { m.Body = "Review (): ok" },
		"missing colons": func(m *model.Message) { m.Body = "Review (approve)" },
	} {
		m := base
		mut(&m)
		if r.svc.Enqueue(m) {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestSkipsTasksWithoutAMatchingHost(t *testing.T) {
	r := newRig(t, ModeAll)
	other := "https://elsewhere.test/o/r/pull/7"
	r.tasks["T-1"].PRURL = &other
	r.review(VerdictChangesRequested, "x")
	r.run()
	elsewhereRepo := "https://git.test/x/y/pull/7"
	r.tasks["T-1"].PRURL = &elsewhereRepo
	r.review(VerdictChangesRequested, "x")
	r.run()
	r.tasks["T-1"].PRURL = nil
	r.review(VerdictChangesRequested, "x")
	r.run()
	delete(r.tasks, "T-1")
	r.review(VerdictChangesRequested, "x")
	r.run()
	if r.host.creates != 0 || r.svc.Pending() != 0 {
		t.Fatalf("creates=%d pending=%d", r.host.creates, r.svc.Pending())
	}
}
