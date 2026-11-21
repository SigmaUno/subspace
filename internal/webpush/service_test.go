package webpush

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/ratelimit"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/testdb"
	"github.com/SigmaUno/subspace/internal/webhookout"
)

type device struct {
	priv *ecdh.PrivateKey
	auth []byte
}

type delivery struct {
	header http.Header
	size   int
	msg    Message
	err    error
}

type pushService struct {
	*httptest.Server
	mu      sync.Mutex
	devices map[string]device
	status  map[string]int
	retry   map[string]string
	hits    map[string][]delivery
}

func newPushService(t *testing.T) *pushService {
	t.Helper()
	p := &pushService{devices: map[string]device{}, status: map[string]int{}, retry: map[string]string{}, hits: map[string][]delivery{}}
	p.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		defer p.mu.Unlock()
		d := delivery{header: r.Header.Clone(), size: len(body)}
		if dev, ok := p.devices[r.URL.Path]; ok {
			var plain []byte
			if plain, d.err = decrypt(body, dev.priv, dev.auth); d.err == nil {
				d.err = json.Unmarshal(plain, &d.msg)
			}
		}
		p.hits[r.URL.Path] = append(p.hits[r.URL.Path], d)
		if ra := p.retry[r.URL.Path]; ra != "" {
			w.Header().Set("Retry-After", ra)
		}
		code := p.status[r.URL.Path]
		if code == 0 {
			code = http.StatusCreated
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *pushService) device(t *testing.T, path string) Input {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, authLen)
	_, _ = rand.Read(auth)
	p.mu.Lock()
	p.devices[path] = device{priv, auth}
	p.mu.Unlock()
	return Input{Endpoint: p.URL + path, Keys: InputKeys{P256DH: b64.EncodeToString(priv.PublicKey().Bytes()), Auth: b64.EncodeToString(auth)}, Name: "phone " + path}
}

func (p *pushService) set(path string, code int, retryAfter string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status[path], p.retry[path] = code, retryAfter
}

func (p *pushService) got(path string) []delivery {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]delivery(nil), p.hits[path]...)
}

type rig struct {
	svc   *Service
	st    store.Store
	push  *pushService
	clock *ratelimit.FakeClock
}

func newRig(t *testing.T, rate ratelimit.Limit) *rig {
	t.Helper()
	st := testdb.Open(t, testdb.Path(t, "t.db"))
	push := newPushService(t)
	r := &rig{st: st, push: push, clock: ratelimit.NewFakeClock(time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC))}
	r.svc = r.service(t, true, rate)
	return r
}

func (r *rig) service(t *testing.T, allowLoopback bool, rate ratelimit.Limit) *Service {
	t.Helper()
	u, _ := url.Parse(r.push.URL)
	hosts, err := NewHosts(u.Host + ", localhost:" + u.Port())
	if err != nil {
		t.Fatal(err)
	}
	var allow []netip.Prefix
	if allowLoopback {
		allow = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	}
	guard := webhookout.NewGuard(allow)
	tr := &http.Transport{DialContext: guard.DialContext, TLSClientConfig: r.push.Client().Transport.(*http.Transport).TLSClientConfig}
	svc, err := New(context.Background(), Options{
		Store: r.st, Hosts: hosts, Guard: guard, Subject: "mailto:owner@example.com", Clock: r.clock, Rate: rate,
		Log:    slog.New(slog.DiscardHandler),
		Client: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func (r *rig) subscribe(t *testing.T, in Input) *Subscription {
	t.Helper()
	sub, created, err := r.svc.Subscribe(context.Background(), in)
	if err != nil || !created {
		t.Fatalf("subscribe %s: %v created=%v", in.Endpoint, err, created)
	}
	return sub
}

func (r *rig) ids(t *testing.T) []string {
	t.Helper()
	subs, err := r.svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range subs {
		out = append(out, s.ID)
	}
	return out
}

func (r *rig) audits(t *testing.T, tool string) []*model.AuditEvent {
	t.Helper()
	var out []*model.AuditEvent
	err := r.st.View(context.Background(), func(tx store.Tx) error {
		var err error
		out, err = tx.ListAudit(store.AuditFilter{Tool: tool, Limit: 100})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func note(id string, priority int) notify.Notification {
	return notify.Notification{ID: id, Kind: "test", Title: "Title " + id, Text: "Text " + id, Priority: priority, TS: time.Unix(1700000000, 0).UTC()}
}

func TestNotificationsReachFakePushService(t *testing.T) {
	r := newRig(t, ratelimit.Limit{})
	alive := r.subscribe(t, r.push.device(t, "/push/alive"))
	dead := r.subscribe(t, r.push.device(t, "/push/dead"))
	r.push.set("/push/dead", http.StatusGone, "")

	hub := events.NewHub()
	ns := notify.New(notify.Config{
		Sinks: []notify.Sink{r.svc}, Window: -1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, Attempts: 3,
		Mint: func(decision, action string, option int) (string, error) {
			return "https://sub.test/a/" + decision + "-" + action + "-" + strconv.Itoa(option), nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); ns.Wait() }()
	ns.Start(ctx, hub)

	brief := "B-1"
	hub.Publish([]string{"questions"}, "question.created", &model.Question{ID: "Q-7", BriefID: &brief, AgentID: "w1", Options: []string{"yes", "no"}})
	eventually(t, "delivery and removal", func() bool { return len(r.push.got("/push/alive")) == 1 && len(r.ids(t)) == 1 })

	d := r.push.got("/push/alive")[0]
	if d.err != nil {
		t.Fatalf("push service could not decrypt: %v", d.err)
	}
	h := d.header
	if h.Get("Content-Encoding") != "aes128gcm" || h.Get("TTL") != "43200" || h.Get("Urgency") != "high" ||
		!regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(h.Get("Topic")) {
		t.Fatalf("headers %v", h)
	}
	if (d.size-headerLen-tagLen)%padBlock != 0 || d.size > recordSize {
		t.Fatalf("record of %d bytes is not padded to %d", d.size, padBlock)
	}
	tok, key, ok := strings.Cut(strings.TrimPrefix(h.Get("Authorization"), "vapid t="), ", k=")
	if !ok || key != r.svc.PublicKey() {
		t.Fatalf("authorization %q", h.Get("Authorization"))
	}
	if c := verifyJWT(t, tok, key); c.Aud != r.push.URL || c.Sub != "mailto:owner@example.com" {
		t.Fatalf("claims %+v", c)
	}
	m := d.msg
	if m.Title != "Question Q-7 needs an answer" || m.Tag != "Q-7" || m.URL != "/questions" || !strings.Contains(m.Body, "Q-7") {
		t.Fatalf("message %+v", m)
	}
	want := []Action{
		{"a0", "Option 1", "https://sub.test/a/q:Q-7-answer-0"},
		{"a1", "Option 2", "https://sub.test/a/q:Q-7-answer-1"},
		{"a2", "Snooze 1h", "https://sub.test/a/q:Q-7-skip-0"},
	}
	if len(m.Actions) != len(want) {
		t.Fatalf("actions %+v", m.Actions)
	}
	for i := range want {
		if m.Actions[i] != want[i] {
			t.Fatalf("action %d = %+v, want %+v", i, m.Actions[i], want[i])
		}
	}
	if ids := r.ids(t); ids[0] != alive.ID {
		t.Fatalf("remaining %v", ids)
	}
	if ev := r.audits(t, "expire_push_subscription"); len(ev) != 1 || ev[0].Before["id"] != dead.ID || *ev[0].Note != "gone" {
		t.Fatalf("expiry audit %+v", ev)
	}

	r.push.set("/push/alive", http.StatusTooManyRequests, "120")
	hub.Publish([]string{"plans"}, "plan.proposed", &model.Plan{ID: "P-2", BriefID: "B-1", ProposedBy: "rex"})
	eventually(t, "retries to give up", func() bool { return ns.Stats().Failed == 1 })
	if n := len(r.push.got("/push/alive")); n != 2 {
		t.Fatalf("push service was called %d times during Retry-After", n)
	}
	r.push.set("/push/alive", 0, "")
	r.clock.Advance(121 * time.Second)
	hub.Publish([]string{"plans"}, "plan.proposed", &model.Plan{ID: "P-3", BriefID: "B-1", ProposedBy: "rex"})
	eventually(t, "delivery after Retry-After", func() bool { return len(r.push.got("/push/alive")) == 3 })
	if m := r.push.got("/push/alive")[2].msg; m.URL != "/plans" || m.Actions[0].Title != "Approve" {
		t.Fatalf("plan message %+v", m)
	}
	eventually(t, "success to be recorded", func() bool {
		subs, err := r.svc.List(context.Background())
		return err == nil && subs[0].LastSuccessAt != nil && subs[0].LastSuccessAt.Equal(r.clock.Now()) && subs[0].Failures == 0
	})
}

func TestDeliveryLimitsExpiryAndFailures(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, ratelimit.Limit{})
	a := r.subscribe(t, r.push.device(t, "/push/a"))
	b := r.subscribe(t, r.push.device(t, "/push/b"))
	r.push.set("/push/b", http.StatusInternalServerError, "")

	if err := r.svc.Send(ctx, note("n1", 5)); err == nil {
		t.Fatal("a 500 must be retried")
	}
	r.push.set("/push/b", 0, "")
	if err := r.svc.Send(ctx, note("n1", 5)); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Send(ctx, note("n1", 5)); err != nil {
		t.Fatal(err)
	}
	if na, nb := len(r.push.got("/push/a")), len(r.push.got("/push/b")); na != 1 || nb != 2 {
		t.Fatalf("retry delivered a=%d b=%d, want 1 and 2", na, nb)
	}
	if u := r.push.got("/push/b")[1].header.Get("Urgency"); u != "high" {
		t.Fatalf("urgency %q", u)
	}

	r.push.set("/push/b", http.StatusForbidden, "")
	for i := range MaxFailures {
		if err := r.svc.Send(ctx, note("f"+strconv.Itoa(i), 1)); err != nil {
			t.Fatal(err)
		}
	}
	if ids := r.ids(t); len(ids) != 1 || ids[0] != a.ID {
		t.Fatalf("after %d rejections: %v (b=%s)", MaxFailures, ids, b.ID)
	}
	if u := r.push.got("/push/a")[1].header.Get("Urgency"); u != "low" {
		t.Fatalf("urgency %q", u)
	}

	in := r.push.device(t, "/push/expiring")
	exp := float64(r.clock.Now().Add(time.Hour).UnixMilli())
	in.ExpirationTime = &exp
	r.subscribe(t, in)
	r.clock.Advance(2 * time.Hour)
	if err := r.svc.Send(ctx, note("n2", 3)); err != nil {
		t.Fatal(err)
	}
	if n := len(r.push.got("/push/expiring")); n != 0 || len(r.ids(t)) != 1 {
		t.Fatalf("expired subscription was called %d times, remaining %v", n, r.ids(t))
	}
	reasons := map[string]bool{}
	for _, ev := range r.audits(t, "expire_push_subscription") {
		reasons[*ev.Note] = true
	}
	if !reasons["expired"] || !reasons["failing"] {
		t.Fatalf("expiry reasons %v", reasons)
	}
}

func TestDeliveryRefusesPrivateAddresses(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, ratelimit.Limit{})
	key := r.svc.PublicKey()
	r.svc = r.service(t, false, ratelimit.Limit{})
	if r.svc.PublicKey() != key {
		t.Fatal("vapid key was not persisted")
	}
	in := r.push.device(t, "/push/rebound")
	in.Endpoint = strings.Replace(in.Endpoint, "127.0.0.1", "localhost", 1)
	r.subscribe(t, in)
	if err := r.svc.Send(ctx, note("n1", 3)); err != nil {
		t.Fatal(err)
	}
	if n := len(r.push.got("/push/rebound")); n != 0 || len(r.ids(t)) != 0 {
		t.Fatalf("loopback endpoint was called %d times, remaining %v", n, r.ids(t))
	}
}

func TestDeliveryRateLimit(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, ratelimit.Limit{PerMinute: 1, Burst: 2})
	r.subscribe(t, r.push.device(t, "/push/a"))
	for i := range 3 {
		if err := r.svc.Send(ctx, note("r"+strconv.Itoa(i), 3)); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(r.push.got("/push/a")); n != 2 || r.svc.Limited() != 1 {
		t.Fatalf("delivered %d, limited %d", n, r.svc.Limited())
	}
	r.clock.Advance(time.Minute)
	if err := r.svc.Send(ctx, note("r3", 3)); err != nil {
		t.Fatal(err)
	}
	if n := len(r.push.got("/push/a")); n != 3 {
		t.Fatalf("delivered %d after refill", n)
	}
}
