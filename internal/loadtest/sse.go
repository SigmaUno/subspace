package loadtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/client"
)

// SSEReport summarises the SSE subscribers.
type SSEReport struct {
	Subscribers   int     `json:"subscribers"`
	Connected     int64   `json:"connected"`
	ConnectErrors int64   `json:"connect_errors"`
	StreamErrors  int64   `json:"stream_errors"`
	Events        int64   `json:"events"`
	LaggedEvents  int64   `json:"lagged_events"`
	Dropped       int64   `json:"dropped"`
	LagP50ms      float64 `json:"lag_p50_ms"`
	LagP95ms      float64 `json:"lag_p95_ms"`
	LagP99ms      float64 `json:"lag_p99_ms"`
	LagMaxms      float64 `json:"lag_max_ms"`
	Reconnects    int64   `json:"reconnects,omitempty"`
	Resyncs       int64   `json:"resyncs,omitempty"`
	Overlap       int64   `json:"overlap,omitempty"`
	Duplicates    int64   `json:"duplicates,omitempty"`
}

// sseFleet runs n SSE subscribers on /api/v1/events and measures delivery lag (receive time - event ts).
type sseFleet struct {
	owner   *client.Owner
	n       int
	churn   time.Duration
	persist bool

	reconnects    atomic.Int64
	resyncs       atomic.Int64
	overlap       atomic.Int64
	duplicates    atomic.Int64
	lag           Histogram
	connected     atomic.Int64
	connectErrors atomic.Int64
	streamErrors  atomic.Int64
	events        atomic.Int64
	lagged        atomic.Int64
	dropped       atomic.Int64
	hello         sync.WaitGroup
	wg            sync.WaitGroup
}

func newSSEFleet(base, token string, n int, churn time.Duration) *sseFleet {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = n + 1
	return &sseFleet{owner: client.NewOwner(base, token, client.WithHTTPClient(&http.Client{Transport: tr})), n: n, churn: churn}
}

// Start connects all subscribers and waits (up to wait) until each has received hello or failed.
func (f *sseFleet) Start(ctx context.Context, wait time.Duration) {
	f.hello.Add(f.n)
	for i := 0; i < f.n; i++ {
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			f.run(ctx, i)
		}()
	}
	done := make(chan struct{})
	go func() { f.hello.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(wait):
	case <-ctx.Done():
	}
}

// Wait blocks until every subscriber has returned (cancel the Start context first).
func (f *sseFleet) Wait() { f.wg.Wait() }

func (f *sseFleet) run(ctx context.Context, idx int) {
	var once sync.Once
	ready := func() { once.Do(f.hello.Done) }
	defer ready()
	var last int64
	for n := 0; ctx.Err() == nil; n++ {
		lifetime := time.Duration(0)
		if f.churn > 0 {
			lifetime = f.churn/2 + time.Duration(shard(fmt.Sprint(idx, "/", n), int(f.churn/2/time.Millisecond)+1))*time.Millisecond
		}
		if n > 0 {
			f.reconnects.Add(1)
		}
		if !f.subscribe(ctx, &last, lifetime, ready) || (f.churn == 0 && !f.persist) {
			return
		}
		if f.persist {
			sleep(ctx, 50*time.Millisecond)
		}
	}
}

func (f *sseFleet) subscribe(ctx context.Context, last *int64, lifetime time.Duration, ready func()) bool {
	from := ""
	if *last > 0 {
		from = strconv.FormatInt(*last, 10)
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := f.owner.EventsFrom(cctx, from, "all")
	if err != nil {
		if ctx.Err() == nil && !f.persist {
			f.connectErrors.Add(1)
		}
		return f.persist && ctx.Err() == nil
	}
	defer func() { _ = stream.Close() }()
	f.connected.Add(1)
	if lifetime > 0 {
		t := time.AfterFunc(lifetime, cancel)
		defer t.Stop()
	}
	var (
		hello int64
		local sseLocal
	)
	defer local.flush(f)
	for {
		ev, err := stream.Next()
		if err != nil {
			if cctx.Err() == nil && !errors.Is(err, io.EOF) && !f.persist {
				f.streamErrors.Add(1)
			}
			return ctx.Err() == nil && (cctx.Err() != nil || f.persist)
		}
		f.track(ev, last, &hello)
		f.handle(ev, ready, &local)
	}
}

func (f *sseFleet) track(ev client.Event, last, hello *int64) {
	id, err := strconv.ParseInt(ev.ID, 10, 64)
	if err != nil {
		return
	}
	switch ev.Name {
	case "hello":
		if id < *last {
			*last = 0
		}
		*hello = id
		return
	case "resync":
		f.resyncs.Add(1)
		*last, *hello = id, id
		return
	}
	if id <= *last {
		f.duplicates.Add(1)
		return
	}
	if id <= *hello {
		f.overlap.Add(1)
	}
	*last = id
}

type sseLocal struct {
	lag    Histogram
	events int64
}

func (l *sseLocal) flush(f *sseFleet) {
	f.lag.Merge(&l.lag)
	f.events.Add(l.events)
}

func (f *sseFleet) handle(ev client.Event, ready func(), local *sseLocal) {
	now := time.Now()
	typ, ts := envelopeHead(ev.Data)
	if ev.Name != "" {
		typ = ev.Name
	}
	switch typ {
	case "hello":
		ready()
		return
	case "lagged":
		env, _ := ev.Envelope()
		var d struct {
			Dropped int64 `json:"dropped"`
		}
		_ = json.Unmarshal(env.Data, &d)
		f.lagged.Add(1)
		f.dropped.Add(d.Dropped)
		return
	}
	local.events++
	if !ts.IsZero() {
		local.lag.Record(now.Sub(ts))
	}
}

func envelopeHead(data string) (typ string, ts time.Time) {
	typ, _ = jsonStringField(data, `"type":"`)
	if v, ok := jsonStringField(data, `"ts":"`); ok {
		ts, _ = time.Parse(time.RFC3339Nano, v)
		return typ, ts
	}
	env, _ := client.Event{Data: data}.Envelope()
	return env.Type, env.TS
}

func jsonStringField(data, key string) (string, bool) {
	i := strings.Index(data, key)
	if i < 0 {
		return "", false
	}
	v := data[i+len(key):]
	j := strings.IndexAny(v, `"\`)
	if j < 0 || v[j] != '"' {
		return "", false
	}
	return v[:j], true
}

// Report returns the current SSE statistics.
func (f *sseFleet) Report() SSEReport {
	return SSEReport{Subscribers: f.n, Connected: f.connected.Load(), ConnectErrors: f.connectErrors.Load(),
		StreamErrors: f.streamErrors.Load(), Events: f.events.Load(), LaggedEvents: f.lagged.Load(), Dropped: f.dropped.Load(),
		LagP50ms: ms(f.lag.Quantile(0.5)), LagP95ms: ms(f.lag.Quantile(0.95)), LagP99ms: ms(f.lag.Quantile(0.99)), LagMaxms: ms(f.lag.Max()),
		Reconnects: f.reconnects.Load(), Resyncs: f.resyncs.Load(), Overlap: f.overlap.Load(), Duplicates: f.duplicates.Load()}
}
