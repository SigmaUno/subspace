package forgestatus

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/githost"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/prsync"
	"github.com/SigmaUno/subspace/internal/ratelimit"
)

const token = "fake-host-token"

type posted struct {
	Repo, SHA string
	Status    githost.CommitStatus
	Auth      string
}

type fakeHost struct {
	kind     string
	srv      *httptest.Server
	mu       sync.Mutex
	posts    []posted
	respond  func(n int, w http.ResponseWriter) bool
	attempts int
	heads    map[int64]string
}

func newFakeHost(t *testing.T, kind string) *fakeHost {
	t.Helper()
	f := &fakeHost{kind: kind, heads: map[int64]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHost) setHead(number int64, sha string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heads[number] = sha
}

func (f *fakeHost) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := "/repos/"
	if f.kind == prsync.KindForgejo {
		prefix = "/api/v1/repos/"
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, prefix), "/")
	switch {
	case r.Method == http.MethodGet && len(parts) == 4 && parts[2] == "pulls":
		n, _ := strconv.ParseInt(parts[3], 10, 64)
		sha, ok := f.heads[n]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"number": n, "state": "open", "head": map[string]string{"ref": "b", "sha": sha}})
	case r.Method == http.MethodPost && len(parts) == 4 && parts[2] == "statuses":
		f.attempts++
		if f.respond != nil && f.respond(f.attempts, w) {
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var st githost.CommitStatus
		_ = json.Unmarshal(raw, &st)
		f.posts = append(f.posts, posted{Repo: parts[0] + "/" + parts[1], SHA: parts[3], Status: st, Auth: r.Header.Get("Authorization")})
		w.WriteHeader(http.StatusCreated)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeHost) all() []posted {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]posted(nil), f.posts...)
}

func (f *fakeHost) tries() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

func (f *fakeHost) config(rate *prsync.Rate) prsync.Config {
	api := f.srv.URL
	if f.kind == prsync.KindForgejo {
		api += "/api/v1"
	}
	return prsync.Config{
		Rate: prsync.Rate{},
		Hosts: map[string]prsync.HostConfig{"h": {
			Kind: f.kind, URL: f.srv.URL, APIURL: api, Token: token, Rate: rate,
		}},
		Repos: map[string]string{},
	}
}

type fixture struct {
	t     *testing.T
	host  *fakeHost
	svc   *Service
	hub   events.Hub
	clock *ratelimit.FakeClock
}

func newFixture(t *testing.T, kind, publicURL string, rate *prsync.Rate) *fixture {
	t.Helper()
	f := &fixture{t: t, host: newFakeHost(t, kind), hub: events.NewHub(), clock: ratelimit.NewFakeClock(time.Unix(1_800_000_000, 0))}
	svc, err := New(Options{
		Config: f.host.config(rate), PublicURL: publicURL, Clock: f.clock,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	f.svc = svc
	return f
}

func (f *fixture) start() {
	ctx := f.t.Context()
	f.svc.Start(ctx, f.hub)
}

func (f *fixture) task(id string, number int64, status string) model.Task {
	u := fmt.Sprintf("%s/o/r/pulls/%d", f.host.srv.URL, number)
	return model.Task{ID: id, Repo: "o/r", Status: status, PRURL: &u}
}

func (f *fixture) publish(t model.Task) {
	f.svc.Enqueue(&t)
}

func (f *fixture) awaitPosts(n int) []posted {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := f.host.all(); len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("got %d posts, want %d", len(f.host.all()), n)
		}
		f.clock.Advance(0)
		time.Sleep(2 * time.Millisecond)
	}
}

func (f *fixture) settle() {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.svc.mu.Lock()
		idle := len(f.svc.order) == 0 && len(f.svc.inflight) == 0
		f.svc.mu.Unlock()
		if idle {
			return
		}
		if time.Now().After(deadline) {
			f.t.Fatal("service did not go idle")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (f *fixture) advanceUntilIdle(step time.Duration) {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.svc.mu.Lock()
		idle := len(f.svc.order) == 0 && len(f.svc.inflight) == 0
		f.svc.mu.Unlock()
		if idle {
			return
		}
		if time.Now().After(deadline) {
			f.t.Fatal("service did not go idle")
		}
		if f.clock.Pending() > 0 {
			f.clock.Advance(step)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

const sha1 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const sha2 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestMapTable(t *testing.T) {
	tests := []struct {
		status string
		state  string
		ok     bool
	}{
		{model.StatusSubmitted, githost.StatePending, true},
		{model.StatusInReview, githost.StatePending, true},
		{model.StatusChangesRequested, githost.StateFailure, true},
		{model.StatusApproved, githost.StateSuccess, true},
		{model.StatusNeedsOwner, githost.StateError, true},
		{model.StatusDone, githost.StateSuccess, true},
		{model.StatusInProgress, "", false},
		{model.StatusAssigned, "", false},
		{model.StatusBlocked, "", false},
		{model.StatusCancelled, "", false},
	}
	for _, tc := range tests {
		state, desc, ok := Map(tc.status)
		if state != tc.state || ok != tc.ok {
			t.Errorf("%s: got %q ok=%v want %q ok=%v", tc.status, state, ok, tc.state, tc.ok)
		}
		if ok && (desc == "" || len(desc) > 140) {
			t.Errorf("%s: description %q", tc.status, desc)
		}
	}
}

func TestPostsStatusOnBothHosts(t *testing.T) {
	for _, kind := range []string{prsync.KindForgejo, prsync.KindGitHub} {
		for _, tc := range []struct{ public, target string }{
			{"https://sub.example/", "https://sub.example/tasks/T-1"},
			{"http://sub.example", ""},
		} {
			t.Run(kind+" "+tc.public, func(t *testing.T) {
				f := newFixture(t, kind, tc.public, nil)
				f.host.setHead(7, sha1)
				f.start()
				tk := f.task("T-1", 7, model.StatusApproved)
				f.hub.Publish([]string{"board"}, "task.updated", &tk)
				got := f.awaitPosts(1)
				want := posted{Repo: "o/r", SHA: sha1, Auth: "token " + token, Status: githost.CommitStatus{
					State: githost.StateSuccess, Context: "subspace/review", Description: "Approved by Subspace review", TargetURL: tc.target,
				}}
				if kind == prsync.KindGitHub {
					want.Auth = "Bearer " + token
				}
				if got[0] != want {
					t.Fatalf("got %+v want %+v", got[0], want)
				}
				if strings.Contains(got[0].Status.Description, token) {
					t.Fatal("description leaks the token")
				}
			})
		}
	}
}

func TestPostsEveryReviewStateInOrder(t *testing.T) {
	f := newFixture(t, prsync.KindForgejo, "https://sub.example", nil)
	f.host.setHead(7, sha1)
	f.start()
	for _, status := range []string{
		model.StatusSubmitted, model.StatusInReview, model.StatusChangesRequested, model.StatusInProgress,
		model.StatusSubmitted, model.StatusApproved, model.StatusDone, model.StatusNeedsOwner,
	} {
		f.publish(f.task("T-1", 7, status))
		f.settle()
	}
	want := []string{githost.StatePending, githost.StateFailure, githost.StatePending, githost.StateSuccess, githost.StateError}
	got := f.host.all()
	if len(got) != len(want) {
		t.Fatalf("got %d posts %+v want states %v", len(got), got, want)
	}
	for i, s := range want {
		if got[i].Status.State != s {
			t.Errorf("post %d: %s want %s", i, got[i].Status.State, s)
		}
	}
}

func TestIdempotentBySHAAndState(t *testing.T) {
	f := newFixture(t, prsync.KindGitHub, "https://sub.example", nil)
	f.host.setHead(7, sha1)
	f.start()

	approved := f.task("T-1", 7, model.StatusApproved)
	f.publish(approved)
	f.publish(approved)
	f.awaitPosts(1)
	f.settle()
	f.publish(approved)
	other := f.task("T-2", 7, model.StatusApproved)
	f.publish(other)
	f.settle()
	if n := len(f.host.all()); n != 1 {
		t.Fatalf("duplicate event or second task on the same sha and state posted %d statuses", n)
	}

	f.host.setHead(7, sha2)
	f.publish(f.task("T-1", 7, model.StatusDone))
	f.awaitPosts(2)
	f.settle()
	f.publish(f.task("T-3", 7, model.StatusApproved))
	f.settle()
	got := f.host.all()
	if len(got) != 2 || got[1].SHA != sha2 || got[1].Status.State != githost.StateSuccess {
		t.Fatalf("new head sha must get one status of its own: %+v", got)
	}
}

func TestRetriesTransientFailuresWithClock(t *testing.T) {
	f := newFixture(t, prsync.KindForgejo, "https://sub.example", nil)
	f.host.setHead(7, sha1)
	f.host.respond = func(n int, w http.ResponseWriter) bool {
		switch n {
		case 1:
			w.WriteHeader(http.StatusInternalServerError)
			return true
		case 2:
			w.Header().Set("Retry-After", "20")
			w.WriteHeader(http.StatusTooManyRequests)
			return true
		}
		return false
	}
	f.start()
	f.publish(f.task("T-1", 7, model.StatusSubmitted))

	deadline := time.Now().Add(5 * time.Second)
	for f.clock.Pending() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no retry timer scheduled")
		}
		time.Sleep(time.Millisecond)
	}
	if n := f.host.tries(); n != 1 {
		t.Fatalf("retried before the backoff elapsed: %d attempts", n)
	}
	f.clock.Advance(time.Second)
	for f.host.tries() < 2 {
		time.Sleep(time.Millisecond)
	}
	for f.clock.Pending() == 0 {
		time.Sleep(time.Millisecond)
	}
	f.clock.Advance(10 * time.Second)
	time.Sleep(20 * time.Millisecond)
	if n := f.host.tries(); n != 2 {
		t.Fatalf("Retry-After of 20s was not honoured: %d attempts", n)
	}
	f.clock.Advance(10 * time.Second)
	got := f.awaitPosts(1)
	if n := f.host.tries(); n != 3 || got[0].Status.State != githost.StatePending {
		t.Fatalf("attempts=%d posts=%+v", n, got)
	}
}

func TestGivesUpAfterAttemptsAndRetriesOnNextEvent(t *testing.T) {
	f := newFixture(t, prsync.KindGitHub, "https://sub.example", nil)
	f.host.setHead(7, sha1)
	f.host.respond = func(n int, w http.ResponseWriter) bool {
		if n <= 4 {
			w.WriteHeader(http.StatusBadGateway)
			return true
		}
		return false
	}
	f.start()
	f.publish(f.task("T-1", 7, model.StatusSubmitted))
	f.advanceUntilIdle(time.Minute)
	if n := f.host.tries(); n != 4 || len(f.host.all()) != 0 {
		t.Fatalf("attempts=%d posts=%d", n, len(f.host.all()))
	}
	f.publish(f.task("T-1", 7, model.StatusSubmitted))
	f.awaitPosts(1)
}

func TestPermanentFailuresAreNotRetried(t *testing.T) {
	for name, status := range map[string]int{"forbidden": 403, "validation": 422, "unauthorized": 401, "not found": 404} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, prsync.KindForgejo, "https://sub.example", nil)
			f.host.setHead(7, sha1)
			f.host.respond = func(_ int, w http.ResponseWriter) bool {
				w.WriteHeader(status)
				return true
			}
			f.start()
			f.publish(f.task("T-1", 7, model.StatusApproved))
			f.settle()
			if n := f.host.tries(); n != 1 {
				t.Fatalf("%d attempts", n)
			}
			if f.clock.Pending() != 0 {
				t.Fatal("retry timer scheduled for a permanent failure")
			}
		})
	}
}

func TestHostRateLimitSpacesRequests(t *testing.T) {
	f := newFixture(t, prsync.KindForgejo, "https://sub.example", &prsync.Rate{PerMinute: 30, Burst: 1})
	f.host.setHead(1, sha1)
	f.host.setHead(2, sha2)
	f.start()
	f.publish(f.task("T-1", 1, model.StatusApproved))
	f.publish(f.task("T-2", 2, model.StatusApproved))

	time.Sleep(50 * time.Millisecond)
	if n := len(f.host.all()); n > 0 {
		t.Fatalf("%d statuses posted without waiting for tokens", n)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(f.host.all()) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("only %d posts", len(f.host.all()))
		}
		f.clock.Advance(2 * time.Second)
		time.Sleep(2 * time.Millisecond)
	}
}

func TestIgnoresTasksWithoutMappedHostOrPR(t *testing.T) {
	f := newFixture(t, prsync.KindForgejo, "https://sub.example", nil)
	f.host.setHead(7, sha1)
	f.start()

	foreign := f.task("T-1", 7, model.StatusApproved)
	u := "https://other.example/o/r/pulls/7"
	foreign.PRURL = &u
	wrongRepo := f.task("T-2", 7, model.StatusApproved)
	wrongRepo.Repo = "o/else"
	noPR := model.Task{ID: "T-3", Repo: "o/r", Status: model.StatusApproved}
	working := f.task("T-4", 7, model.StatusInProgress)
	for _, tk := range []model.Task{foreign, wrongRepo, noPR, working} {
		f.publish(tk)
	}
	f.svc.Enqueue("not a task")
	f.settle()
	time.Sleep(20 * time.Millisecond)
	if n := f.host.tries(); n != 0 {
		t.Fatalf("%d requests for ineligible tasks", n)
	}
}

func TestRepoMappingPicksConfiguredHost(t *testing.T) {
	a, b := newFakeHost(t, prsync.KindForgejo), newFakeHost(t, prsync.KindGitHub)
	a.setHead(7, sha1)
	b.setHead(7, sha2)
	cfg := a.config(nil)
	cfg.Hosts["gh"] = b.config(nil).Hosts["h"]
	cfg.Hosts["f"] = cfg.Hosts["h"]
	delete(cfg.Hosts, "h")
	cfg.Repos["o/r"] = "gh"
	svc, err := New(Options{Config: cfg, PublicURL: "https://sub.example", Clock: ratelimit.NewFakeClock(time.Unix(1_800_000_000, 0)), Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	hub := events.NewHub()
	svc.Start(t.Context(), hub)
	u := b.srv.URL + "/o/r/pulls/7"
	hub.Publish([]string{"board"}, "task.updated", &model.Task{ID: "T-1", Repo: "o/r", Status: model.StatusApproved, PRURL: &u})
	deadline := time.Now().Add(5 * time.Second)
	for len(b.all()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("mapped host got no status")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(a.all()) != 0 || b.all()[0].SHA != sha2 {
		t.Fatalf("a=%v b=%v", a.all(), b.all())
	}
}

func TestForgetsTasksThatLeaveReview(t *testing.T) {
	f := newFixture(t, prsync.KindGitHub, "https://sub.example", nil)
	for i := range 40 {
		f.host.setHead(int64(i+1), fmt.Sprintf("%040x", i+1))
	}
	f.start()
	for i := range 40 {
		id := fmt.Sprintf("T-%d", i)
		f.publish(f.task(id, int64(i+1), model.StatusApproved))
		f.settle()
		last := model.StatusDone
		if i%2 == 1 {
			last = model.StatusCancelled
		}
		f.publish(f.task(id, int64(i+1), last))
		f.settle()
	}
	f.svc.mu.Lock()
	n := len(f.svc.sent)
	f.svc.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d finished tasks still tracked", n)
	}
	if got := len(f.host.all()); got != 40 {
		t.Fatalf("got %d posts, want one per head", got)
	}
}
