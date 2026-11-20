package loadtest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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
	hc    *http.Client
	url   string
	token string
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
	return &sseFleet{hc: &http.Client{Transport: tr}, url: strings.TrimRight(base, "/") + "/api/v1/events?topics=all", token: token, n: n}
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.url, nil)
	if err != nil {
		f.connectErrors.Add(1)
		return
	}
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := f.hc.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			f.connectErrors.Add(1)
		}
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		f.connectErrors.Add(1)
		return
	}
	f.connected.Add(1)
	err = readSSE(resp.Body, func(ev sseFrame) {
		f.handle(ev, ready)
	})
	if err != nil && ctx.Err() == nil {
		f.streamErrors.Add(1)
	}
}

func (f *sseFleet) handle(ev sseFrame, ready func()) {
	now := time.Now()
	var env struct {
		Type string          `json:"type"`
		TS   time.Time       `json:"ts"`
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal([]byte(ev.Data), &env)
	typ := ev.Event
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

type sseFrame struct {
	ID    string
	Event string
	Data  string
}

// readSSE parses a text/event-stream, calling fn for each dispatched event. Comments (": ping") are ignored.
func readSSE(r io.Reader, fn func(sseFrame)) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var cur sseFrame
	var data []string
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if len(data) > 0 || cur.Event != "" {
				cur.Data = strings.Join(data, "\n")
				fn(cur)
			}
			cur, data = sseFrame{}, data[:0]
		case strings.HasPrefix(line, ":"):
		default:
			k, v, _ := strings.Cut(line, ":")
			v = strings.TrimPrefix(v, " ")
			switch k {
			case "id":
				cur.ID = v
			case "event":
				cur.Event = v
			case "data":
				data = append(data, v)
			}
		}
	}
}
