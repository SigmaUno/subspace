package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/model"
)

type fakeClock struct {
	mu        sync.Mutex
	now       time.Time
	timers    []fakeTimer
	requested []time.Duration
}

type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.timers = append(c.timers, fakeTimer{c.now.Add(d), ch})
	c.requested = append(c.requested, d)
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	rest := c.timers[:0]
	for _, t := range c.timers {
		if t.at.After(c.now) {
			rest = append(rest, t)
			continue
		}
		t.ch <- c.now
	}
	c.timers = rest
}

func (c *fakeClock) waiting() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

func (c *fakeClock) durations() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.requested)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

type syncHub struct {
	events.Hub
	settled chan struct{}
}

type syncSub struct {
	events.Subscription
	out chan events.Event
}

func (s *syncSub) C() <-chan events.Event { return s.out }

func (h *syncHub) Subscribe(topics []string) events.Subscription {
	inner := h.Hub.Subscribe(topics)
	s := &syncSub{Subscription: inner, out: make(chan events.Event)}
	go func() {
		for ev := range inner.C() {
			s.out <- ev
			if ev.Type == "test.settle" {
				h.settled <- struct{}{}
			}
		}
	}()
	return s
}

func (h *syncHub) settle() {
	h.Publish([]string{"board"}, "test.settle", nil)
	<-h.settled
}

type harness struct {
	t     *testing.T
	hub   *syncHub
	clock *fakeClock
	svc   *Service
}

func start(t *testing.T, cfg Config) *harness {
	t.Helper()
	h := &harness{t: t, hub: &syncHub{Hub: events.NewHub(), settled: make(chan struct{}, 1)}, clock: newFakeClock()}
	cfg.Clock = h.clock
	if cfg.Jitter == nil {
		cfg.Jitter = func(int64) int64 { return 0 }
	}
	h.svc = New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	h.svc.Start(ctx, h.hub)
	t.Cleanup(func() { cancel(); h.svc.Wait() })
	return h
}

func (h *harness) publish(topic, typ string, data any) {
	h.hub.Publish([]string{topic}, typ, data)
}

func (h *harness) flush() {
	h.t.Helper()
	h.hub.settle()
	eventually(h.t, "window timer", func() bool { return h.clock.waiting() == 1 })
	h.clock.Advance(time.Second)
}

func question(id, task string) *model.Question {
	return &model.Question{ID: id, TaskID: &task, AgentID: "w1", Question: "private question text"}
}

func task(id, status string) *model.Task {
	return &model.Task{ID: id, BriefID: "B-1", Title: "Add login", Spec: "SECRET-SPEC-BODY", Status: status}
}

type collector struct {
	mu   sync.Mutex
	got  []Notification
	srv  *httptest.Server
	fail int
	hits int
}

func newCollector(t *testing.T) *collector {
	c := &collector{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.hits++
		if c.hits <= c.fail {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var n Notification
		_ = json.NewDecoder(r.Body).Decode(&n)
		c.got = append(c.got, n)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *collector) ids() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ids []string
	for _, n := range c.got {
		ids = append(ids, n.ID+":"+n.Kind)
	}
	return ids
}

func (c *collector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.got)
}

func webhook(c *collector) Sink { return NewWebhook(c.srv.URL, "", NewClient(true, 5*time.Second)) }

func TestBurstsCoalesceToOneNotificationPerDecision(t *testing.T) {
	c := newCollector(t)
	h := start(t, Config{Sinks: []Sink{webhook(c)}})

	h.publish("questions", "question.created", question("Q-1", "T-1"))
	h.publish("questions", "question.created", question("Q-1", "T-1"))
	h.publish("board", "task.updated", task("T-1", "blocked"))
	h.publish("board", "task.updated", task("T-1", "needs_owner"))
	h.publish("board", "task.updated", task("T-2", "blocked"))
	h.publish("board", "task.updated", task("T-2", "assigned"))
	h.flush()
	eventually(t, "first batch", func() bool { return c.count() == 2 })
	if got, want := c.ids(), []string{"Q-1:question", "T-1:needs_owner"}; !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}

	h.publish("board", "task.updated", task("T-1", "needs_owner"))
	h.publish("questions", "question.created", question("Q-1", "T-1"))
	h.publish("plans", "plan.proposed", &model.Plan{ID: "P-1", BriefID: "B-1", ProposedBy: "lead"})
	h.flush()
	eventually(t, "second batch", func() bool { return c.count() == 3 })
	if got := c.ids()[2]; got != "P-1:plan" {
		t.Fatalf("got %s", got)
	}

	h.publish("board", "task.updated", task("T-1", "in_review"))
	h.publish("board", "task.updated", task("T-1", "blocked"))
	h.flush()
	eventually(t, "re-block", func() bool { return c.count() == 4 })
	if got := c.ids()[3]; got != "T-1:blocked" {
		t.Fatalf("got %s", got)
	}
}

func TestRetryBackoffUsesInjectedClockAndJitter(t *testing.T) {
	c := newCollector(t)
	c.fail = 3
	h := start(t, Config{
		Sinks: []Sink{webhook(c)}, Attempts: 5, BaseDelay: time.Second, MaxDelay: 3 * time.Second,
		Jitter: func(n int64) int64 { return n - 1 },
	})

	h.publish("questions", "question.created", question("Q-1", "T-1"))
	h.flush()
	for i := 0; i < 3; i++ {
		eventually(t, "backoff timer", func() bool { return h.clock.waiting() == 1 })
		h.clock.Advance(time.Minute)
	}
	eventually(t, "delivery", func() bool { return c.count() == 1 })
	want := []time.Duration{time.Second, time.Second, 2 * time.Second, 3 * time.Second}
	if got := h.clock.durations(); !slices.Equal(got, want) {
		t.Fatalf("timers %v want %v", got, want)
	}
	eventually(t, "sent counter", func() bool { return h.svc.Stats().Sent == 1 })
	if st := h.svc.Stats(); st.Failed != 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestRetriesGiveUpAfterAttempts(t *testing.T) {
	c := newCollector(t)
	c.fail = 100
	h := start(t, Config{Sinks: []Sink{webhook(c)}, Attempts: 3})

	h.publish("questions", "question.created", question("Q-1", "T-1"))
	h.flush()
	for i := 0; i < 2; i++ {
		eventually(t, "backoff timer", func() bool { return h.clock.waiting() == 1 })
		h.clock.Advance(time.Minute)
	}
	eventually(t, "failure", func() bool { return h.svc.Stats().Failed == 1 })
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hits != 3 {
		t.Fatalf("hits %d", c.hits)
	}
}

func TestQueueIsBoundedAndDropsOldest(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n Notification
		_ = json.NewDecoder(r.Body).Decode(&n)
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		mu.Lock()
		got = append(got, n.ID)
		mu.Unlock()
	}))
	defer srv.Close()
	h := start(t, Config{Sinks: []Sink{NewWebhook(srv.URL, "", NewClient(true, 5*time.Second))}, Queue: 2, Window: -1})

	h.publish("questions", "question.created", question("Q-1", "T-1"))
	<-entered
	for _, id := range []string{"Q-2", "Q-3", "Q-4", "Q-5"} {
		h.publish("questions", "question.created", question(id, "T-1"))
	}
	h.hub.settle()
	if st := h.svc.Stats(); st.Dropped != 2 || st.Queued != 2 {
		t.Fatalf("stats %+v", st)
	}
	close(release)
	eventually(t, "drain", func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 3 })
	if want := []string{"Q-1", "Q-4", "Q-5"}; !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestSinkFormatsCarryTitlesAndIDsOnly(t *testing.T) {
	var mu sync.Mutex
	got := map[string]*http.Request{}
	bodies := map[string]string{}
	mk := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			got[name], bodies[name] = r, string(b)
			mu.Unlock()
		}))
	}
	wh, nt, sl := mk("webhook"), mk("ntfy"), mk("slack")
	defer wh.Close()
	defer nt.Close()
	defer sl.Close()
	cl := NewClient(true, 5*time.Second)
	h := start(t, Config{Sinks: []Sink{NewWebhook(wh.URL, "s3cret", cl), NewNtfy(nt.URL, cl), NewSlack(sl.URL, cl)}})

	h.publish("board", "task.updated", task("T-7", "needs_owner"))
	h.flush()
	eventually(t, "all sinks", func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 3 })

	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write([]byte(bodies["webhook"]))
	if sig := got["webhook"].Header.Get(SignatureHeader); sig != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("bad signature %q", sig)
	}
	var n Notification
	if err := json.Unmarshal([]byte(bodies["webhook"]), &n); err != nil || n.TaskID != "T-7" || n.BriefID != "B-1" || n.Kind != "needs_owner" {
		t.Fatalf("webhook body %s (%v)", bodies["webhook"], err)
	}
	if r := got["ntfy"]; r.Header.Get("Title") != "Task T-7 is needs owner" || r.Header.Get("Priority") != "5" {
		t.Fatalf("ntfy headers %v", r.Header)
	}
	var sk map[string]string
	if err := json.Unmarshal([]byte(bodies["slack"]), &sk); err != nil || !strings.Contains(sk["text"], "T-7: Add login") {
		t.Fatalf("slack body %s", bodies["slack"])
	}
	for name, b := range bodies {
		if strings.Contains(b, "SECRET-SPEC-BODY") || strings.Contains(b, "s3cret") {
			t.Fatalf("%s leaked: %s", name, b)
		}
	}
	if got["ntfy"].Header.Get(SignatureHeader) != "" || got["slack"].Header.Get(SignatureHeader) != "" {
		t.Fatal("signature on non-webhook sink")
	}
}

func TestQuestionTextIsNotNotified(t *testing.T) {
	c := newCollector(t)
	h := start(t, Config{Sinks: []Sink{webhook(c)}})
	h.publish("questions", "question.created", question("Q-1", "T-1"))
	h.flush()
	eventually(t, "delivery", func() bool { return c.count() == 1 })
	c.mu.Lock()
	defer c.mu.Unlock()
	if b, _ := json.Marshal(c.got[0]); strings.Contains(string(b), "private question text") {
		t.Fatalf("question body leaked: %s", b)
	}
}

func TestSSRFGuard(t *testing.T) {
	for _, u := range []string{
		"http://127.0.0.1/x", "http://localhost/x", "http://[::1]/x", "http://10.1.2.3/x", "http://192.168.0.1/x",
		"http://169.254.169.254/latest", "http://100.64.0.1/x", "http://0.0.0.0/x", "http://[::ffff:127.0.0.1]/x", "ftp://example.com/x",
	} {
		if CheckURL(u, false) == nil {
			t.Errorf("%s accepted", u)
		}
		if strings.HasPrefix(u, "http") && CheckURL(u, true) != nil {
			t.Errorf("%s rejected with allowPrivate", u)
		}
	}
	if err := CheckURL("https://ntfy.sh/topic", false); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("guarded client reached a loopback server")
	}))
	defer srv.Close()
	err := NewNtfy(srv.URL, NewClient(false, 5*time.Second)).Send(context.Background(), Notification{ID: "Q-1"})
	var perm permanentError
	if !errors.Is(err, ErrBlockedTarget) || !errors.As(err, &perm) {
		t.Fatalf("expected permanent blocked error, got %v", err)
	}
}

func TestConfigFromEnv(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }

	cfg, err := ConfigFromEnv(get)
	if err != nil || New(cfg).Enabled() {
		t.Fatalf("empty env: enabled=%v err=%v", New(cfg).Enabled(), err)
	}
	hub := events.NewHub()
	New(cfg).Start(context.Background(), hub)
	if hub.Subscribers() != 0 {
		t.Fatal("service without sinks subscribed to the hub")
	}

	env[EnvWebhookURL] = "http://127.0.0.1:9/hook"
	if _, err := ConfigFromEnv(get); err == nil {
		t.Fatal("private target accepted without opt-in")
	}
	env[EnvAllowPrivate] = "true"
	env[EnvNtfyURL] = "https://ntfy.sh/topic"
	env[EnvSlackURL] = "https://hooks.slack.com/services/x"
	cfg, err = ConfigFromEnv(get)
	if err != nil {
		t.Fatal(err)
	}
	if got := New(cfg).SinkNames(); !slices.Equal(got, []string{"webhook", "ntfy", "slack"}) {
		t.Fatalf("sinks %v", got)
	}
}

func TestConfigFromEnvTelegramAndSMTP(t *testing.T) {
	for _, tc := range []struct {
		name    string
		env     map[string]string
		sinks   []string
		wantErr bool
		hidden  string
	}{
		{name: "telegram", env: map[string]string{EnvTelegramBot: testBotToken, EnvTelegramChatID: "-100"}, sinks: []string{"telegram"}},
		{name: "telegram token only", env: map[string]string{EnvTelegramBot: testBotToken}, wantErr: true},
		{name: "telegram malformed token", env: map[string]string{EnvTelegramBot: "1/../x", EnvTelegramChatID: "-100"}, wantErr: true, hidden: "1/../x"},
		{
			name: "smtp", sinks: []string{"smtp"},
			env: map[string]string{EnvSMTPURL: "smtp://127.0.0.1:587", EnvSMTPFrom: "bot@sub.test", EnvSMTPTo: "a@x.test, b@x.test", EnvAllowPrivate: "1"},
		},
		{name: "smtp without recipients", env: map[string]string{EnvSMTPURL: "smtp://mail.example.com", EnvSMTPFrom: "bot@sub.test"}, wantErr: true},
		{name: "smtp private without opt-in", env: map[string]string{EnvSMTPURL: "smtp://10.0.0.5", EnvSMTPFrom: "bot@sub.test", EnvSMTPTo: "a@x.test"}, wantErr: true},
		{
			name:    "smtp credentials with starttls disabled",
			env:     map[string]string{EnvSMTPURL: "smtp://bot:hunter2@mail.example.com", EnvSMTPFrom: "bot@sub.test", EnvSMTPTo: "a@x.test", EnvSMTPSTARTTLS: "off"},
			wantErr: true, hidden: "hunter2",
		},
		{
			name: "smtp url with password is not echoed", wantErr: true, hidden: "hunter2",
			env: map[string]string{EnvSMTPURL: "smtp://bot:hunter2@10.0.0.5", EnvSMTPFrom: "bot@sub.test", EnvSMTPTo: "a@x.test"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ConfigFromEnv(func(k string) string { return tc.env[k] })
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if tc.hidden != "" && strings.Contains(err.Error(), tc.hidden) {
					t.Fatalf("error leaks secret: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := New(cfg).SinkNames(); !slices.Equal(got, tc.sinks) {
				t.Fatalf("sinks %v want %v", got, tc.sinks)
			}
		})
	}
}
