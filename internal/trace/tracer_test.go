package trace

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

var startTime = time.Unix(1700000000, 0)

type recorder struct {
	mu    sync.Mutex
	spans []*SpanData
}

func (r *recorder) Export(sd *SpanData) {
	r.mu.Lock()
	r.spans = append(r.spans, sd)
	r.mu.Unlock()
}

func (r *recorder) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, s := range r.spans {
		out = append(out, s.Name)
	}
	return out
}

func TestRatioSamplingIsDeterministicPerTraceID(t *testing.T) {
	for _, ratio := range []float64{0, 0.01, 0.25, 1} {
		tr := NewTracer(ratio, &recorder{})
		hits := 0
		const n = 40000
		for range n {
			id := newID()
			got := tr.SampledID(id)
			if got != tr.SampledID(id) {
				t.Fatal("decision must be a pure function of the trace id")
			}
			if got {
				hits++
			}
		}
		if ratio == 0 && hits != 0 || ratio == 1 && hits != n {
			t.Fatalf("ratio %v sampled %d of %d", ratio, hits, n)
		}
		if got := float64(hits) / n; got < ratio*0.8-0.002 || got > ratio*1.2+0.002 {
			t.Fatalf("ratio %v sampled %.4f", ratio, got)
		}
	}
	if NewTracer(1, nil).SampledID(newID()) {
		t.Fatal("a tracer without an exporter never samples")
	}
}

func TestSamplingIsParentBased(t *testing.T) {
	rec := &recorder{}
	tr := NewTracer(0, rec)
	ctx := WithTracer(context.Background(), tr)

	sampled, _ := Parse("00-" + goodTrace + "-" + goodSpan + "-01")
	sctx, span := StartRemote(ctx, sampled, "child", KindServer)
	if span == nil {
		t.Fatal("a sampled remote parent must be recorded even at ratio 0")
	}
	_, inner := Start(sctx, "inner", KindInternal)
	inner.End()
	span.End()
	if len(rec.spans) != 2 {
		t.Fatalf("spans %v", rec.names())
	}
	in, outer := rec.spans[0], rec.spans[1]
	if outer.ParentID.String() != goodSpan || in.ParentID != outer.SpanID || in.TraceID.String() != goodTrace {
		t.Fatalf("parent links: outer %+v inner %+v", outer, in)
	}

	unsampled, _ := Parse("00-" + goodTrace + "-" + goodSpan + "-00")
	full := WithTracer(context.Background(), NewTracer(1, rec))
	uctx, none := StartRemote(full, unsampled, "dropped", KindServer)
	if none != nil {
		t.Fatal("an unsampled remote parent must not be recorded even at ratio 1")
	}
	if From(uctx).TraceID.String() != goodTrace || From(uctx).Sampled() {
		t.Fatalf("the remote trace id must still propagate: %+v", From(uctx))
	}
	if _, s := Start(uctx, "inner", KindInternal); s != nil {
		t.Fatal("children of an unsampled span stay unsampled")
	}
}

func TestRootsFollowTheRatioAndAlwaysCarryATraceID(t *testing.T) {
	rec := &recorder{}
	ctx, span := StartRoot(WithTracer(context.Background(), NewTracer(0, rec)), "loop", KindInternal)
	if span != nil || TraceID(ctx) == "" || Recorded(ctx) != "" {
		t.Fatalf("unsampled root: span %v id %q recorded %q", span, TraceID(ctx), Recorded(ctx))
	}
	ctx, span = StartRoot(WithTracer(context.Background(), NewTracer(1, rec)), "loop", KindInternal)
	if span == nil || Recorded(ctx) != TraceID(ctx) {
		t.Fatal("sampled root must be recorded")
	}
	span.End()
	if _, span := StartRoot(context.Background(), "x", KindInternal); span != nil {
		t.Fatal("no tracer, no span")
	}
}

func TestContinueKeepsTheDecisionOfTheOriginalTrace(t *testing.T) {
	rec := &recorder{}
	ctx := WithTracer(context.Background(), NewTracer(0, rec))

	_, span := Continue(ctx, goodTrace, FlagSampled, "deliver", KindClient)
	if span == nil || span.data.TraceID.String() != goodTrace || !span.data.ParentID.IsZero() {
		t.Fatalf("continued span: %+v", span)
	}
	span.End()
	if _, span := Continue(ctx, goodTrace, 0, "deliver", KindClient); span != nil {
		t.Fatal("an explicit unsampled decision wins over the ratio")
	}
	cctx, span := Continue(ctx, "not-a-trace-id", FlagSampled, "deliver", KindClient)
	if span != nil || TraceID(cctx) == "" || TraceID(cctx) == "not-a-trace-id" {
		t.Fatal("a malformed trace id starts a fresh trace decided by the ratio")
	}
}

func TestRecordedOnlyForPropagatedOrSampledTraces(t *testing.T) {
	ctx := WithSpanContext(context.Background(), SpanContext{TraceID: newID(), SpanID: newSpanID()})
	if Recorded(ctx) != "" {
		t.Fatal("a locally generated unsampled trace is not recorded")
	}
	remote, _ := Parse("00-" + goodTrace + "-" + goodSpan + "-00")
	if got := Recorded(WithSpanContext(context.Background(), remote)); got != goodTrace {
		t.Fatalf("a propagated trace is recorded: %q", got)
	}
}

func TestBatchLinksEveryOtherSampledRequest(t *testing.T) {
	rec := &recorder{}
	tr := NewTracer(0, rec)
	mk := func(flags string) context.Context {
		sc, _ := Parse("00-" + newID().String() + "-" + newSpanID().String() + "-" + flags)
		return WithSpanContext(WithTracer(context.Background(), tr), sc)
	}
	var b Batch
	b.Add(mk("00"))
	first, second := mk("01"), mk("01")
	b.Add(first)
	b.Add(second)
	b.Add(mk("00"))
	b.End("store.commit", startTime, nil)
	if len(rec.spans) != 1 {
		t.Fatalf("spans %v", rec.names())
	}
	sd := rec.spans[0]
	if sd.TraceID != From(first).TraceID || len(sd.Links) != 1 || sd.Links[0].TraceID != From(second).TraceID {
		t.Fatalf("commit span %+v", sd)
	}

	var none Batch
	none.Add(mk("00"))
	none.End("store.commit", startTime, nil)
	if len(rec.spans) != 1 {
		t.Fatal("a batch without sampled requests records nothing")
	}
}

func serve(t *testing.T, tr *Tracer, h http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	tr.Handler(h, func(r *http.Request) string {
		if r.URL.Path == "/api/v1/tasks/T-1" {
			return "GET /api/v1/tasks/{id}"
		}
		return ""
	}).ServeHTTP(rr, req)
	return rr
}

func TestMiddleware(t *testing.T) {
	rec := &recorder{}
	tr := NewTracer(0, rec)
	var seen SpanContext
	ok := func(w http.ResponseWriter, r *http.Request) {
		seen = From(r.Context())
		if TracerFrom(r.Context()) != tr {
			t.Error("handler context must carry the tracer")
		}
		w.WriteHeader(http.StatusTeapot)
	}

	req := httptest.NewRequest("GET", "/api/v1/tasks/T-1", nil)
	req.Header.Set("Traceparent", "00-"+goodTrace+"-"+goodSpan+"-01")
	rr := serve(t, tr, ok, req)
	if rr.Header().Get(HeaderTraceID) != goodTrace || seen.TraceID.String() != goodTrace || !seen.Propagated {
		t.Fatalf("inbound trace not adopted: %q %+v", rr.Header().Get(HeaderTraceID), seen)
	}
	if len(rec.spans) != 1 {
		t.Fatalf("spans %v", rec.names())
	}
	sd := rec.spans[0]
	if sd.Name != "GET /api/v1/tasks/{id}" || sd.ParentID.String() != goodSpan || sd.Kind != KindServer || sd.Failed {
		t.Fatalf("server span %+v", sd)
	}
	if seen.SpanID != sd.SpanID {
		t.Fatal("handlers must see the server span as the current span")
	}

	junk := httptest.NewRequest("GET", "/x", nil)
	junk.Header.Set("Traceparent", "00-"+goodTrace+"-"+goodSpan)
	rr = serve(t, tr, ok, junk)
	if id := rr.Header().Get(HeaderTraceID); len(id) != 32 || id == goodTrace || seen.Propagated {
		t.Fatalf("malformed header must start a new trace: %q %+v", id, seen)
	}
	if len(rec.spans) != 1 {
		t.Fatal("a fresh unsampled trace at ratio 0 records nothing")
	}

	fail := httptest.NewRequest("GET", "/boom", nil)
	fail.Header.Set("Traceparent", "00-"+goodTrace+"-"+goodSpan+"-01")
	serve(t, tr, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }, fail)
	last := rec.spans[len(rec.spans)-1]
	if !last.Failed || last.Name != "GET unmatched" {
		t.Fatalf("5xx must fail the span: %+v", last)
	}

	before := len(rec.spans)
	hz := httptest.NewRequest("GET", "/healthz", nil)
	hz.Header.Set("Traceparent", "00-"+goodTrace+"-"+goodSpan+"-01")
	serve(t, tr, ok, hz)
	if len(rec.spans) != before {
		t.Fatal("health probes are not traced")
	}
}

func TestMiddlewareKeepsFlushingAndResponseController(t *testing.T) {
	tr := NewTracer(1, &recorder{})
	rr := httptest.NewRecorder()
	h := tr.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("flush through the tracing wrapper: %v", err)
		}
	}), nil)
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/stream", nil))
	if !rr.Flushed {
		t.Fatal("flush did not reach the underlying writer")
	}
}
