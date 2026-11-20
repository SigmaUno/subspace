package loadtest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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
}

// sseFleet runs n SSE subscribers on /api/v1/events and measures delivery lag (receive time - event ts).
type sseFleet struct {
	owner *client.Owner
	n     int

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

func newSSEFleet(base, token string, n int) *sseFleet {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = n + 1
	return &sseFleet{owner: client.NewOwner(base, token, client.WithHTTPClient(&http.Client{Transport: tr})), n: n}
}

// Start connects all subscribers and waits (up to wait) until each has received hello or failed.
func (f *sseFleet) Start(ctx context.Context, wait time.Duration) {
	f.hello.Add(f.n)
	for i := 0; i < f.n; i++ {
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			f.subscribe(ctx)
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

func (f *sseFleet) subscribe(ctx context.Context) {
	var once sync.Once
	ready := func() { once.Do(f.hello.Done) }
	defer ready()
	stream, err := f.owner.Events(ctx, "all")
	if err != nil {
		if ctx.Err() == nil {
			f.connectErrors.Add(1)
		}
		return
	}
	defer func() { _ = stream.Close() }()
	f.connected.Add(1)
	for {
		ev, err := stream.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) && ctx.Err() == nil {
				f.streamErrors.Add(1)
			}
			return
		}
		f.handle(ev, ready)
	}
}

func (f *sseFleet) handle(ev client.Event, ready func()) {
	now := time.Now()
	env, _ := ev.Envelope()
	typ := ev.Name
	if typ == "" {
		typ = env.Type
	}
	switch typ {
	case "hello":
		ready()
		return
	case "lagged":
		var d struct {
			Dropped int64 `json:"dropped"`
		}
		_ = json.Unmarshal(env.Data, &d)
		f.lagged.Add(1)
		f.dropped.Add(d.Dropped)
		return
	}
	f.events.Add(1)
	if !env.TS.IsZero() {
		f.lag.Record(now.Sub(env.TS))
	}
}

// Report returns the current SSE statistics.
func (f *sseFleet) Report() SSEReport {
	return SSEReport{Subscribers: f.n, Connected: f.connected.Load(), ConnectErrors: f.connectErrors.Load(),
		StreamErrors: f.streamErrors.Load(), Events: f.events.Load(), LaggedEvents: f.lagged.Load(), Dropped: f.dropped.Load(),
		LagP50ms: ms(f.lag.Quantile(0.5)), LagP95ms: ms(f.lag.Quantile(0.95)), LagP99ms: ms(f.lag.Quantile(0.99)), LagMaxms: ms(f.lag.Max())}
}
