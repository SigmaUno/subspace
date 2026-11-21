package webhookout

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/testdb"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

type hit struct {
	header http.Header
	body   []byte
}

type receiver struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	hits   []hit
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	r := &receiver{status: http.StatusOK}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.hits = append(r.hits, hit{req.Header.Clone(), b})
		status := r.status
		r.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *receiver) setStatus(code int) {
	r.mu.Lock()
	r.status = code
	r.mu.Unlock()
}

func (r *receiver) got() []hit {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]hit(nil), r.hits...)
}

type rig struct {
	svc *Service
	st  *SQLStore
	clk *clock
	rcv *receiver
}

func newRig(t *testing.T, mutate func(*Options)) *rig {
	t.Helper()
	db := testdb.Open(t, testdb.Path(t, "t.db"))
	return buildRig(t, NewSQLStore(db), mutate)
}

func buildRig(t *testing.T, st *SQLStore, mutate func(*Options)) *rig {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	o := Options{Store: st, Hub: events.NewHub(), Now: clk.Now, Guard: NewGuard(mustPrefixes("127.0.0.0/8")),
		Jitter: func(max time.Duration) time.Duration { return max }}
	if mutate != nil {
		mutate(&o)
	}
	return &rig{svc: New(o), st: st, clk: clk, rcv: newReceiver(t)}
}

func (r *rig) create(t *testing.T, evs []string, brief string) *Subscription {
	t.Helper()
	sub, err := r.svc.Create(context.Background(), CreateInput{URL: r.rcv.URL + "/hook", Events: evs, BriefID: brief})
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

func (r *rig) emit(typ string, data any) {
	r.svc.handle(context.Background(), events.Event{Type: typ, Data: data})
}

func (r *rig) deliveries(t *testing.T, sub *Subscription) []*Delivery {
	t.Helper()
	ds, err := r.svc.Deliveries(context.Background(), sub.ID, "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	return ds
}

func task() *model.Task {
	return &model.Task{ID: "T-3", BriefID: "B-1", Title: "Add login", Spec: "SPEC-BODY-MUST-NOT-LEAK token=abcdefghijklmnop1234567890", Status: "in_progress"}
}

func TestDeliverySignedAndMinimal(t *testing.T) {
	r := newRig(t, nil)
	sub := r.create(t, []string{"task.updated"}, "")
	if !strings.HasPrefix(sub.Secret, SecretPrefix) {
		t.Fatalf("secret %q", sub.Secret)
	}
	r.emit("task.updated", task())
	r.emit("agent.updated", map[string]any{"id": "w1"})
	r.svc.Tick(context.Background())

	hits := r.rcv.got()
	if len(hits) != 1 {
		t.Fatalf("receiver got %d requests, want 1", len(hits))
	}
	h := hits[0]
	if !Verify(sub.Secret, h.header.Get("X-Subspace-Timestamp"), h.body, h.header.Get("X-Subspace-Signature")) {
		t.Fatal("signature does not verify")
	}
	if Verify("whsec_other", h.header.Get("X-Subspace-Timestamp"), h.body, h.header.Get("X-Subspace-Signature")) {
		t.Fatal("signature verifies under another secret")
	}
	if Verify(sub.Secret, h.header.Get("X-Subspace-Timestamp"), append(h.body, ' '), h.header.Get("X-Subspace-Signature")) {
		t.Fatal("signature verifies over a modified body")
	}
	if got := h.header.Get("X-Subspace-Timestamp"); got != "1795176000" {
		t.Fatalf("timestamp %q", got)
	}
	if h.header.Get("X-Subspace-Event") != "task.updated" || h.header.Get("X-Subspace-Delivery") != "WD-1" {
		t.Fatalf("headers %v", h.header)
	}
	if strings.Contains(string(h.body), "SPEC-BODY") || strings.Contains(string(h.body), "abcdefghijklmnop") {
		t.Fatalf("payload leaks task body: %s", h.body)
	}
	var p struct {
		ID, Event string
		Data      map[string]any
	}
	if err := json.Unmarshal(h.body, &p); err != nil {
		t.Fatal(err)
	}
	if p.ID != "WD-1" || p.Event != "task.updated" || p.Data["id"] != "T-3" || p.Data["title"] != "Add login" || p.Data["brief_id"] != "B-1" || p.Data["status"] != "in_progress" {
		t.Fatalf("payload %s", h.body)
	}
	if _, ok := p.Data["spec"]; ok {
		t.Fatal("spec in payload")
	}
	ds := r.deliveries(t, sub)
	if len(ds) != 1 || ds[0].State != StateDelivered || ds[0].Attempts != 1 || *ds[0].LastStatus != 200 {
		t.Fatalf("deliveries %+v", ds)
	}
}

func TestPlanPayloadKeepsTaskTitlesOnly(t *testing.T) {
	r := newRig(t, nil)
	r.create(t, []string{"plan.*"}, "B-1")
	r.emit("plan.proposed", &model.Plan{ID: "P-1", BriefID: "B-1", Status: "proposed", Tasks: []*model.Task{task()}})
	r.emit("plan.proposed", &model.Plan{ID: "P-2", BriefID: "B-2", Status: "proposed"})
	r.svc.Tick(context.Background())
	hits := r.rcv.got()
	if len(hits) != 1 {
		t.Fatalf("got %d deliveries, want only the B-1 plan", len(hits))
	}
	var p struct {
		Data struct {
			Tasks []map[string]any
		}
	}
	if err := json.Unmarshal(hits[0].body, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Data.Tasks) != 1 || len(p.Data.Tasks[0]) != 2 || p.Data.Tasks[0]["title"] != "Add login" {
		t.Fatalf("tasks %v in %s", p.Data.Tasks, hits[0].body)
	}
}

func TestEventFilters(t *testing.T) {
	r := newRig(t, nil)
	sub := r.create(t, []string{"question.created", "decision.*"}, "")
	for _, typ := range []string{"question.created", "question.answered", "decision.made", "decision.skipped", "decisions.x", "task.updated"} {
		r.emit(typ, map[string]any{"id": "x"})
	}
	var got []string
	for _, d := range r.deliveries(t, sub) {
		got = append(got, d.Event)
	}
	if strings.Join(got, ",") != "decision.skipped,decision.made,question.created" {
		t.Fatalf("queued %v", got)
	}
}

func TestRetryScheduleAndDeadLetter(t *testing.T) {
	r := newRig(t, nil)
	sub := r.create(t, []string{"task.updated"}, "")
	r.rcv.setStatus(http.StatusServiceUnavailable)
	r.emit("task.updated", task())
	ctx := context.Background()
	start := r.clk.Now()
	deadline := start.Add(MaxAge)

	var delays []time.Duration
	last := start
	for range 100 {
		r.svc.Tick(ctx)
		d := r.deliveries(t, sub)[0]
		if d.State == StateDead {
			break
		}
		if d.State != StatePending || d.NextAttemptAt == nil {
			t.Fatalf("state %s", d.State)
		}
		delays = append(delays, d.NextAttemptAt.Sub(last))
		last = *d.NextAttemptAt
		r.clk.Set(last)
	}
	want := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second, 160 * time.Second, 320 * time.Second,
		640 * time.Second, 1280 * time.Second, 2560 * time.Second, 5120 * time.Second, 10240 * time.Second, 4 * time.Hour, 4 * time.Hour, 4 * time.Hour, 4 * time.Hour}
	if len(delays) < len(want) {
		t.Fatalf("only %d retries: %v", len(delays), delays)
	}
	for i, w := range want {
		if delays[i] != w {
			t.Fatalf("delay %d = %s, want %s (%v)", i, delays[i], w, delays)
		}
	}
	d := r.deliveries(t, sub)[0]
	if d.State != StateDead || d.NextAttemptAt != nil || *d.LastStatus != 503 || d.LastError == "" {
		t.Fatalf("dead letter %+v", d)
	}
	if !r.clk.Now().Equal(deadline) {
		t.Fatalf("last attempt at %s, want the 24h deadline %s", r.clk.Now(), deadline)
	}
	if d.Attempts != len(delays)+1 || len(r.rcv.got()) != d.Attempts {
		t.Fatalf("attempts %d, delays %d, receiver saw %d", d.Attempts, len(delays), len(r.rcv.got()))
	}

	r.svc.Tick(ctx)
	if len(r.rcv.got()) != d.Attempts {
		t.Fatal("dead delivery was attempted again")
	}
	if _, err := r.svc.Redeliver(ctx, sub.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Redeliver(ctx, sub.ID, d.ID); model.AsError(err).Code != "conflict" {
		t.Fatalf("second redeliver: %v", err)
	}
	r.rcv.setStatus(http.StatusNoContent)
	r.svc.Tick(ctx)
	d = r.deliveries(t, sub)[0]
	if d.State != StateDelivered || d.Attempts != 1 || d.DeliveredAt == nil {
		t.Fatalf("after redeliver %+v", d)
	}
}

func TestJitterStaysWithinHalfToFullStep(t *testing.T) {
	r := newRig(t, func(o *Options) { o.Jitter = nil })
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for attempts := 1; attempts <= 30; attempts++ {
		step := min(BaseDelay<<min(attempts-1, 20), MaxDelay)
		for range 50 {
			at, ok := r.svc.nextAttempt(now, now, attempts)
			if !ok {
				t.Fatal("unexpected dead")
			}
			if d := at.Sub(now); d < step/2 || d > step {
				t.Fatalf("attempt %d: delay %s outside [%s, %s]", attempts, d, step/2, step)
			}
		}
	}
}

func TestQueueIsBounded(t *testing.T) {
	r := newRig(t, func(o *Options) { o.MaxPending = 3 })
	sub := r.create(t, []string{"task.updated"}, "")
	for range 5 {
		r.emit("task.updated", task())
	}
	got, err := r.svc.Get(context.Background(), sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pending != 3 || got.Dead != 2 {
		t.Fatalf("pending %d dead %d", got.Pending, got.Dead)
	}
	for _, d := range r.deliveries(t, sub) {
		if d.State == StateDead && d.LastError != "queue full" {
			t.Fatalf("dead delivery %+v", d)
		}
	}
	r.svc.Tick(context.Background())
	if n := len(r.rcv.got()); n != 3 {
		t.Fatalf("delivered %d, want the 3 queued", n)
	}
}

func TestPendingDeliveriesSurviveRestart(t *testing.T) {
	path := testdb.Path(t, "t.db")
	db := testdb.Open(t, path)
	first := buildRig(t, NewSQLStore(db), nil)
	sub := first.create(t, []string{"task.updated"}, "")
	first.rcv.setStatus(http.StatusBadGateway)
	first.emit("task.updated", task())
	first.emit("task.updated", task())
	first.svc.Tick(context.Background())
	if n := len(first.rcv.got()); n != 1 {
		t.Fatalf("first run attempted %d, want the head of the queue only", n)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db2 := testdb.Open(t, path)
	second := buildRig(t, NewSQLStore(db2), nil)
	second.clk.Set(first.clk.Now().Add(time.Hour))
	second.rcv.Close()
	second.rcv = first.rcv
	first.rcv.setStatus(http.StatusOK)
	if err := second.svc.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	second.svc.Tick(context.Background())
	ds := second.deliveries(t, sub)
	if len(ds) != 2 {
		t.Fatalf("deliveries after restart: %+v", ds)
	}
	for _, d := range ds {
		if d.State != StateDelivered {
			t.Fatalf("delivery %s is %s after restart", d.ID, d.State)
		}
	}
	if n := len(first.rcv.got()); n != 3 {
		t.Fatalf("receiver saw %d requests, want 3 (1 failed + 2 delivered after restart)", n)
	}
}

func TestDeleteRemovesDeliveries(t *testing.T) {
	r := newRig(t, nil)
	sub := r.create(t, []string{"task.updated"}, "")
	r.emit("task.updated", task())
	if err := r.svc.Delete(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	r.svc.Tick(context.Background())
	if len(r.rcv.got()) != 0 {
		t.Fatal("deleted subscription still delivered")
	}
	if _, err := r.svc.Deliveries(context.Background(), sub.ID, "", 10); model.AsError(err).Code != "not_found" {
		t.Fatalf("err %v", err)
	}
	r.emit("task.updated", task())
}

func TestCreateValidation(t *testing.T) {
	r := newRig(t, nil)
	cases := map[string]CreateInput{
		"no events":     {URL: "https://hooks.example.com/"},
		"bad event":     {URL: "https://hooks.example.com/", Events: []string{"task"}},
		"upper event":   {URL: "https://hooks.example.com/", Events: []string{"Task.Updated"}},
		"bad brief":     {URL: "https://hooks.example.com/", Events: []string{"task.updated"}, BriefID: "x"},
		"private url":   {URL: "http://192.168.1.10/", Events: []string{"task.updated"}},
		"metadata url":  {URL: "http://169.254.169.254/", Events: []string{"task.updated"}},
		"scheme":        {URL: "gopher://hooks.example.com/", Events: []string{"task.updated"}},
		"too many":      {URL: "https://hooks.example.com/", Events: manyEvents()},
		"credentials":   {URL: "https://a:b@hooks.example.com/", Events: []string{"task.updated"}},
		"empty url":     {Events: []string{"task.updated"}},
		"loopback v6":   {URL: "http://[::1]/", Events: []string{"task.updated"}},
		"mapped v4 rfc": {URL: "http://[::ffff:10.0.0.1]/", Events: []string{"task.updated"}},
	}
	for name, in := range cases {
		if _, err := r.svc.Create(context.Background(), in); model.AsError(err).Code != "bad_request" {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func manyEvents() []string {
	var out []string
	for _, c := range "abcdefghijklmnopqrstu" {
		out = append(out, "task."+string(c))
	}
	return out
}
