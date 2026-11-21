package trace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/netguard"
	"github.com/SigmaUno/subspace/internal/retryq"
)

const (
	DefaultQueue    = 2048
	DefaultBatch    = 256
	DefaultInterval = 5 * time.Second
	DefaultAttempts = 5
	DefaultBase     = 500 * time.Millisecond
	DefaultLimit    = 15 * time.Second
	DefaultTimeout  = 10 * time.Second
	maxResponse     = 4096
)

type OTLPOptions struct {
	URL      string
	Headers  map[string]string
	Client   *http.Client
	Queue    int
	Batch    int
	Interval time.Duration
	Attempts int
	Base     time.Duration
	Limit    time.Duration
	Clock    retryq.Timer
	Rand     func(int64) int64
	Resource []Attr
	Log      *slog.Logger
}

type Stats struct {
	Exported int64
	Dropped  int64
	Failed   int64
	Requests int64
}

type OTLP struct {
	o      OTLPOptions
	q      chan *SpanData
	flush  chan chan struct{}
	stop   chan struct{}
	done   chan struct{}
	cancel context.CancelFunc
	once   sync.Once

	exported atomic.Int64
	dropped  atomic.Int64
	failed   atomic.Int64
	requests atomic.Int64
}

func NewOTLP(o OTLPOptions) *OTLP {
	if o.Queue <= 0 {
		o.Queue = DefaultQueue
	}
	if o.Batch <= 0 {
		o.Batch = DefaultBatch
	}
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	if o.Attempts <= 0 {
		o.Attempts = DefaultAttempts
	}
	if o.Base <= 0 {
		o.Base = DefaultBase
	}
	if o.Limit <= 0 {
		o.Limit = DefaultLimit
	}
	if o.Clock == nil {
		o.Clock = retryq.SystemClock{}
	}
	if o.Rand == nil {
		o.Rand = rand.Int64N
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: DefaultTimeout, CheckRedirect: netguard.NoRedirect}
	}
	return &OTLP{
		o:     o,
		q:     make(chan *SpanData, o.Queue),
		flush: make(chan chan struct{}),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
}

func (e *OTLP) Export(sd *SpanData) {
	select {
	case e.q <- sd:
	default:
		e.dropped.Add(1)
	}
}

func (e *OTLP) Stats() Stats {
	return Stats{Exported: e.exported.Load(), Dropped: e.dropped.Load(), Failed: e.failed.Load(), Requests: e.requests.Load()}
}

func (e *OTLP) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	go e.run(ctx)
}

func (e *OTLP) Flush(ctx context.Context) error {
	done := make(chan struct{})
	select {
	case e.flush <- done:
	case <-e.done:
		return errors.New("exporter stopped")
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *OTLP) Close(ctx context.Context) error {
	e.once.Do(func() { close(e.stop) })
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		e.cancel()
		<-e.done
		return ctx.Err()
	}
}

func (e *OTLP) run(ctx context.Context) {
	defer close(e.done)
	tick := time.NewTicker(e.o.Interval)
	defer tick.Stop()
	batch := make([]*SpanData, 0, e.o.Batch)
	send := func() {
		if len(batch) > 0 {
			e.deliver(ctx, batch)
			batch = batch[:0]
		}
	}
	drain := func() {
		for {
			select {
			case sd := <-e.q:
				batch = append(batch, sd)
				if len(batch) >= e.o.Batch {
					send()
				}
			default:
				send()
				return
			}
		}
	}
	for {
		select {
		case sd := <-e.q:
			batch = append(batch, sd)
			if len(batch) >= e.o.Batch {
				send()
			}
		case <-tick.C:
			send()
		case done := <-e.flush:
			drain()
			close(done)
		case <-e.stop:
			drain()
			return
		}
	}
}

type permanent struct{ error }

func (e *OTLP) deliver(ctx context.Context, batch []*SpanData) {
	body, err := encode(e.o.Resource, batch)
	if err != nil {
		e.failed.Add(int64(len(batch)))
		e.o.Log.Warn("trace export: encode failed", "err", err)
		return
	}
	var wait time.Duration
	for attempt := 1; ; attempt++ {
		wait, err = e.post(ctx, body)
		if err == nil {
			e.exported.Add(int64(len(batch)))
			return
		}
		var p permanent
		if errors.As(err, &p) || attempt >= e.o.Attempts {
			break
		}
		d := retryq.Exp(e.o.Base, e.o.Limit, attempt)
		if wait > 0 {
			d = min(wait, e.o.Limit)
		}
		if retryq.Sleep(ctx, e.o.Clock, retryq.HalfJitter(d, e.o.Rand)) != nil {
			break
		}
	}
	e.failed.Add(int64(len(batch)))
	e.o.Log.Warn("trace export: batch dropped", "spans", len(batch), "err", err)
}

func (e *OTLP) post(ctx context.Context, body []byte) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.o.URL, bytes.NewReader(body))
	if err != nil {
		return 0, permanent{err}
	}
	for k, v := range e.o.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	e.requests.Add(1)
	resp, err := e.o.Client.Do(req)
	if err != nil {
		err = netguard.Scrub(err)
		if errors.Is(err, netguard.ErrBlocked) {
			return 0, permanent{err}
		}
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponse))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return 0, nil
	case resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		var wait time.Duration
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			wait = time.Duration(s) * time.Second
		}
		return wait, fmt.Errorf("collector status %d", resp.StatusCode)
	}
	return 0, permanent{fmt.Errorf("collector status %d", resp.StatusCode)}
}

type otlpRequest struct {
	ResourceSpans []resourceSpans `json:"resourceSpans"`
}

type resourceSpans struct {
	Resource   otlpResource `json:"resource"`
	ScopeSpans []scopeSpans `json:"scopeSpans"`
}

type otlpResource struct {
	Attributes []keyValue `json:"attributes"`
}

type scopeSpans struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans"`
}

type otlpScope struct {
	Name string `json:"name"`
}

type otlpSpan struct {
	TraceID           string     `json:"traceId"`
	SpanID            string     `json:"spanId"`
	TraceState        string     `json:"traceState,omitempty"`
	ParentSpanID      string     `json:"parentSpanId,omitempty"`
	Name              string     `json:"name"`
	Kind              int        `json:"kind"`
	StartTimeUnixNano string     `json:"startTimeUnixNano"`
	EndTimeUnixNano   string     `json:"endTimeUnixNano"`
	Attributes        []keyValue `json:"attributes,omitempty"`
	Links             []otlpLink `json:"links,omitempty"`
	Status            *otlpState `json:"status,omitempty"`
}

type otlpLink struct {
	TraceID string `json:"traceId"`
	SpanID  string `json:"spanId"`
}

type otlpState struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

type keyValue struct {
	Key   string   `json:"key"`
	Value anyValue `json:"value"`
}

type anyValue struct {
	String *string  `json:"stringValue,omitempty"`
	Int    *string  `json:"intValue,omitempty"`
	Bool   *bool    `json:"boolValue,omitempty"`
	Double *float64 `json:"doubleValue,omitempty"`
}

const statusError = 2

func attrs(in []Attr) []keyValue {
	out := make([]keyValue, 0, len(in))
	for _, a := range in {
		kv := keyValue{Key: a.Key}
		switch v := a.Value.(type) {
		case string:
			kv.Value.String = &v
		case int64:
			s := strconv.FormatInt(v, 10)
			kv.Value.Int = &s
		case bool:
			kv.Value.Bool = &v
		case float64:
			kv.Value.Double = &v
		default:
			s := fmt.Sprint(v)
			kv.Value.String = &s
		}
		out = append(out, kv)
	}
	return out
}

func encode(resource []Attr, batch []*SpanData) ([]byte, error) {
	spans := make([]otlpSpan, 0, len(batch))
	for _, d := range batch {
		s := otlpSpan{
			TraceID:           d.TraceID.String(),
			SpanID:            d.SpanID.String(),
			TraceState:        d.State,
			Name:              d.Name,
			Kind:              int(d.Kind),
			StartTimeUnixNano: strconv.FormatInt(d.Start.UnixNano(), 10),
			EndTimeUnixNano:   strconv.FormatInt(d.End.UnixNano(), 10),
			Attributes:        attrs(d.Attrs),
		}
		if !d.ParentID.IsZero() {
			s.ParentSpanID = d.ParentID.String()
		}
		for _, l := range d.Links {
			s.Links = append(s.Links, otlpLink{TraceID: l.TraceID.String(), SpanID: l.SpanID.String()})
		}
		if d.Failed {
			s.Status = &otlpState{Code: statusError, Message: d.Message}
		}
		spans = append(spans, s)
	}
	return json.Marshal(otlpRequest{ResourceSpans: []resourceSpans{{
		Resource:   otlpResource{Attributes: attrs(resource)},
		ScopeSpans: []scopeSpans{{Scope: otlpScope{Name: "subspace"}, Spans: spans}},
	}}})
}
