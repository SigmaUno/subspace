package trace

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

type fakeCollector struct {
	*httptest.Server
	mu       sync.Mutex
	plan     []int
	retry    string
	requests []collected
}

type collected struct {
	header http.Header
	spans  []map[string]any
	body   map[string]any
}

func newFakeCollector(t *testing.T, plan ...int) *fakeCollector {
	t.Helper()
	c := &fakeCollector{plan: plan}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		c.mu.Lock()
		status := http.StatusOK
		if len(c.plan) > 0 {
			status, c.plan = c.plan[0], c.plan[1:]
		}
		retry := c.retry
		var spans []map[string]any
		if rs, ok := body["resourceSpans"].([]any); ok && len(rs) > 0 {
			for _, ss := range rs[0].(map[string]any)["scopeSpans"].([]any) {
				for _, sp := range ss.(map[string]any)["spans"].([]any) {
					spans = append(spans, sp.(map[string]any))
				}
			}
		}
		c.requests = append(c.requests, collected{r.Header.Clone(), spans, body})
		c.mu.Unlock()
		if status == http.StatusTooManyRequests && retry != "" {
			w.Header().Set("Retry-After", retry)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *fakeCollector) snapshot() []collected {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]collected(nil), c.requests...)
}

type fakeClock struct {
	mu     sync.Mutex
	sleeps []time.Duration
}

func (f *fakeClock) Timer(d time.Duration) (<-chan time.Time, func() bool) {
	f.mu.Lock()
	f.sleeps = append(f.sleeps, d)
	f.mu.Unlock()
	ch := make(chan time.Time, 1)
	ch <- time.Time{}
	return ch, func() bool { return true }
}

func newExporter(c *fakeCollector, clock *fakeClock, mod func(*OTLPOptions)) *OTLP {
	o := OTLPOptions{
		URL: c.URL + "/v1/traces", Headers: map[string]string{"Authorization": "Bearer secret-collector-key"},
		Batch: 100, Queue: 1000, Interval: time.Hour, Attempts: 4, Base: 100 * time.Millisecond, Limit: time.Second,
		Clock: clock, Rand: func(int64) int64 { return 0 }, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Resource: []Attr{String("service.name", "subspace")},
	}
	if mod != nil {
		mod(&o)
	}
	e := NewOTLP(o)
	e.Start()
	return e
}

func sampleSpan(i int) *SpanData {
	start := time.Unix(1700000000, int64(i))
	return &SpanData{
		TraceID: newID(), SpanID: newSpanID(), Name: "span-" + strconv.Itoa(i), Kind: KindServer,
		Start: start, End: start.Add(time.Millisecond),
		Attrs: []Attr{String("k", "v"), Int("n", i), Bool("b", true)},
	}
}

func closeExporter(t *testing.T, e *OTLP) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestExporterBatchesAndEncodesOTLPJSON(t *testing.T) {
	col := newFakeCollector(t)
	exp := newExporter(col, &fakeClock{}, nil)
	for i := range 250 {
		exp.Export(sampleSpan(i))
	}
	if err := exp.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := col.snapshot()
	if len(reqs) != 3 || len(reqs[0].spans) != 100 || len(reqs[1].spans) != 100 || len(reqs[2].spans) != 50 {
		sizes := []int{}
		for _, r := range reqs {
			sizes = append(sizes, len(r.spans))
		}
		t.Fatalf("batches = %v, want [100 100 50]", sizes)
	}
	if got := reqs[0].header.Get("Authorization"); got != "Bearer secret-collector-key" {
		t.Fatalf("configured header not sent: %q", got)
	}
	if got := reqs[0].header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type %q", got)
	}
	sp := reqs[0].spans[0]
	if len(sp["traceId"].(string)) != 32 || len(sp["spanId"].(string)) != 16 {
		t.Fatalf("ids must be lowercase hex: %v", sp)
	}
	if _, has := sp["parentSpanId"]; has {
		t.Fatal("root span must omit parentSpanId")
	}
	if sp["kind"] != float64(2) || sp["startTimeUnixNano"] != "1700000000000000000" || sp["endTimeUnixNano"] != "1700000000001000000" {
		t.Fatalf("span encoding: %v", sp)
	}
	attrs := sp["attributes"].([]any)
	if len(attrs) != 3 {
		t.Fatalf("attributes: %v", attrs)
	}
	wantInt := map[string]any{"key": "n", "value": map[string]any{"intValue": "0"}}
	if got := attrs[1].(map[string]any); got["key"] != wantInt["key"] || got["value"].(map[string]any)["intValue"] != "0" {
		t.Fatalf("int attribute must be a string intValue: %v", got)
	}
	res := reqs[0].body["resourceSpans"].([]any)[0].(map[string]any)["resource"].(map[string]any)["attributes"].([]any)
	if res[0].(map[string]any)["key"] != "service.name" {
		t.Fatalf("resource: %v", res)
	}
	closeExporter(t, exp)
	if st := exp.Stats(); st.Exported != 250 || st.Dropped != 0 || st.Failed != 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestExporterRetriesTransientFailures(t *testing.T) {
	col := newFakeCollector(t, http.StatusServiceUnavailable, http.StatusTooManyRequests, http.StatusOK)
	clock := &fakeClock{}
	exp := newExporter(col, clock, nil)
	exp.Export(sampleSpan(1))
	if err := exp.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := col.snapshot()
	if len(reqs) != 3 {
		t.Fatalf("requests = %d, want 3", len(reqs))
	}
	for _, r := range reqs {
		if len(r.spans) != 1 || r.spans[0]["name"] != "span-1" {
			t.Fatalf("a retry must resend the same batch: %v", r.spans)
		}
	}
	if want := []time.Duration{50 * time.Millisecond, 100 * time.Millisecond}; len(clock.sleeps) != 2 || clock.sleeps[0] != want[0] || clock.sleeps[1] != want[1] {
		t.Fatalf("backoff = %v, want %v (half of exponential, no jitter)", clock.sleeps, want)
	}
	closeExporter(t, exp)
	if st := exp.Stats(); st.Exported != 1 || st.Failed != 0 || st.Requests != 3 {
		t.Fatalf("stats %+v", st)
	}
}

func TestExporterHonoursRetryAfterAndGivesUp(t *testing.T) {
	col := newFakeCollector(t, 429, 429, 429, 429, 429)
	col.retry = "3"
	clock := &fakeClock{}
	exp := newExporter(col, clock, nil)
	exp.Export(sampleSpan(1))
	if err := exp.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(col.snapshot()); got != 4 {
		t.Fatalf("attempts = %d, want 4", got)
	}
	for _, d := range clock.sleeps {
		if d != 500*time.Millisecond {
			t.Fatalf("Retry-After above the limit must be capped at it (half-jittered): %v", clock.sleeps)
		}
	}
	closeExporter(t, exp)
	if st := exp.Stats(); st.Exported != 0 || st.Failed != 1 {
		t.Fatalf("stats %+v", st)
	}
}

func TestExporterDoesNotRetryRejectedBatches(t *testing.T) {
	col := newFakeCollector(t, http.StatusBadRequest)
	exp := newExporter(col, &fakeClock{}, nil)
	exp.Export(sampleSpan(1))
	if err := exp.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(col.snapshot()); got != 1 {
		t.Fatalf("a 400 must not be retried, got %d requests", got)
	}
	closeExporter(t, exp)
	if st := exp.Stats(); st.Failed != 1 {
		t.Fatalf("stats %+v", st)
	}
}

func TestExporterQueueIsBounded(t *testing.T) {
	col := newFakeCollector(t)
	exp := NewOTLP(OTLPOptions{URL: col.URL + "/v1/traces", Queue: 10, Batch: 100, Interval: time.Hour, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	for i := range 25 {
		exp.Export(sampleSpan(i))
	}
	if st := exp.Stats(); st.Dropped != 15 {
		t.Fatalf("dropped = %d, want 15 (queue of 10, exporter not started)", st.Dropped)
	}
	exp.Start()
	closeExporter(t, exp)
	if st := exp.Stats(); st.Exported != 10 {
		t.Fatalf("stats %+v", st)
	}
}

func TestExporterCloseFlushesPending(t *testing.T) {
	col := newFakeCollector(t)
	exp := newExporter(col, &fakeClock{}, nil)
	for i := range 7 {
		exp.Export(sampleSpan(i))
	}
	closeExporter(t, exp)
	reqs := col.snapshot()
	if len(reqs) != 1 || len(reqs[0].spans) != 7 {
		t.Fatalf("close must send what is queued: %d requests", len(reqs))
	}
}

func TestExporterEncodesParentLinksAndErrors(t *testing.T) {
	col := newFakeCollector(t)
	exp := newExporter(col, &fakeClock{}, nil)
	sd := sampleSpan(1)
	sd.ParentID = newSpanID()
	sd.Links = []Link{{TraceID: newID(), SpanID: newSpanID()}}
	sd.Failed, sd.Message = true, "boom"
	sd.State = "vendor=a"
	exp.Export(sd)
	closeExporter(t, exp)
	sp := col.snapshot()[0].spans[0]
	if sp["parentSpanId"] != sd.ParentID.String() || sp["traceState"] != "vendor=a" {
		t.Fatalf("span: %v", sp)
	}
	if st := sp["status"].(map[string]any); st["code"] != float64(2) || st["message"] != "boom" {
		t.Fatalf("status: %v", st)
	}
	if l := sp["links"].([]any)[0].(map[string]any); l["traceId"] != sd.Links[0].TraceID.String() {
		t.Fatalf("links: %v", sp["links"])
	}
}

func TestConfigRejectsUnsafeEndpoints(t *testing.T) {
	get := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, ep := range []string{
		"http://127.0.0.1:4318", "http://localhost:4318", "http://169.254.169.254/", "http://10.0.0.5:4318",
		"http://[::1]:4318", "ftp://collector.example.com", "http://user:pw@collector.example.com", "http://collector.example.com/?x=1",
	} {
		if _, err := ConfigFromEnv(get(map[string]string{EnvEndpoint: ep})); err == nil {
			t.Errorf("endpoint %q was accepted", ep)
		}
	}
	c, err := ConfigFromEnv(get(map[string]string{EnvEndpoint: "http://127.0.0.1:4318", EnvAllowPrivate: "true", EnvHeaders: "Authorization=Bearer abc, X-Team=a%20b"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.URL != "http://127.0.0.1:4318/v1/traces" || c.Headers["Authorization"] != "Bearer abc" || c.Headers["X-Team"] != "a b" {
		t.Fatalf("config %+v", c)
	}
	c, err = ConfigFromEnv(get(map[string]string{EnvEndpoint: "https://collector.example.com/otlp/v1/traces"}))
	if err != nil || c.URL != "https://collector.example.com/otlp/v1/traces" || c.Sample != DefaultSample {
		t.Fatalf("config %+v %v", c, err)
	}
	for _, bad := range []map[string]string{
		{EnvSample: "2"}, {EnvSample: "x"}, {EnvQueue: "0"}, {EnvEndpoint: "https://c.example.com", EnvHeaders: "Host=evil"},
		{EnvEndpoint: "https://c.example.com", EnvHeaders: "novalue"}, {EnvEndpoint: "https://c.example.com", EnvHeaders: "A=b%0d%0aX: y"},
	} {
		if _, err := ConfigFromEnv(get(bad)); err == nil {
			t.Errorf("config %v was accepted", bad)
		}
	}
	if c, err := ConfigFromEnv(get(nil)); err != nil || c.Enabled() {
		t.Fatalf("empty config: %+v %v", c, err)
	}
}

func TestBuiltExporterBlocksPrivateCollector(t *testing.T) {
	col := newFakeCollector(t)
	cfg := Config{URL: col.URL + "/v1/traces", Sample: 1, Queue: 10}
	rt := cfg.Build(slog.New(slog.NewTextHandler(io.Discard, nil)), "test", "i-1")
	rt.Exporter.o.Clock = &fakeClock{}
	rt.Start()
	rt.Exporter.Export(sampleSpan(1))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rt.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(col.snapshot()); n != 0 {
		t.Fatalf("the SSRF guard let %d requests reach a loopback collector", n)
	}
	if st := rt.Exporter.Stats(); st.Failed != 1 || st.Requests != 1 {
		t.Fatalf("a blocked destination is permanent: %+v", st)
	}
}
