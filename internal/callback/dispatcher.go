package callback

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

const (
	ReasonAssigned         = "task_assigned"
	ReasonChangesRequested = "changes_requested"
	ReasonMessage          = "message"
	ReasonAnswer           = "answer"

	EventWake = "wake"

	maxReasons = 20
	maxDrain   = 4 << 10
)

type Reason struct {
	Type       string `json:"type"`
	TaskID     string `json:"task_id,omitempty"`
	Thread     string `json:"thread,omitempty"`
	QuestionID string `json:"question_id,omitempty"`
}

type Event struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	AgentID   string    `json:"agent_id"`
	CreatedAt time.Time `json:"created_at"`
	Reasons   []Reason  `json:"reasons"`
	Truncated bool      `json:"truncated,omitempty"`
}

type Clock interface {
	Now() time.Time
	At(t time.Time) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                  { return time.Now() }
func (realClock) At(t time.Time) <-chan time.Time { return time.After(time.Until(t)) }

type Stats struct {
	Sent    int64
	Failed  int64
	Dropped int64
	Pending int
}

type Options struct {
	Targets     Targets
	Client      *http.Client
	Clock       Clock
	Log         *slog.Logger
	QueueSize   int
	Workers     int
	Coalesce    time.Duration
	Backoff     time.Duration
	MaxBackoff  time.Duration
	MaxAttempts int
	Timeout     time.Duration
	Jitter      func() float64
	NewID       func() string
}

type entry struct {
	agent     string
	id        string
	created   time.Time
	reasons   []Reason
	truncated bool
	due       time.Time
	attempts  int
}

type Dispatcher struct {
	o       Options
	mu      sync.Mutex
	pending map[string]*entry
	flying  map[string]bool
	stats   Stats
	kick    chan struct{}
	wg      sync.WaitGroup
}

func NewDispatcher(o Options) *Dispatcher {
	if o.Clock == nil {
		o.Clock = realClock{}
	}
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.QueueSize <= 0 {
		o.QueueSize = 1024
	}
	if o.Workers <= 0 {
		o.Workers = 4
	}
	if o.Coalesce <= 0 {
		o.Coalesce = 2 * time.Second
	}
	if o.Backoff <= 0 {
		o.Backoff = time.Second
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = time.Minute
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 6
	}
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.Jitter == nil {
		o.Jitter = mrand.Float64
	}
	if o.NewID == nil {
		o.NewID = randomID
	}
	if o.Client == nil {
		o.Client = Policy{}.NewClient(o.Timeout)
	}
	return &Dispatcher{o: o, pending: map[string]*entry{}, flying: map[string]bool{}, kick: make(chan struct{}, 1)}
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "cb_" + hex.EncodeToString(b)
}

func (d *Dispatcher) Stats() Stats {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.stats
	s.Pending = len(d.pending) + len(d.flying)
	return s
}

func (d *Dispatcher) wake() {
	select {
	case d.kick <- struct{}{}:
	default:
	}
}

func (d *Dispatcher) Enqueue(ctx context.Context, agentID string, r Reason) {
	if _, ok, err := d.o.Targets.Lookup(ctx, agentID); err != nil || !ok {
		if err != nil {
			d.o.Log.Warn("callback lookup", "agent", agentID, "err", err)
		}
		return
	}
	d.mu.Lock()
	e := d.pending[agentID]
	if e == nil {
		if len(d.pending)+len(d.flying) >= d.o.QueueSize {
			d.stats.Dropped++
			d.mu.Unlock()
			d.o.Log.Warn("callback queue full", "agent", agentID)
			return
		}
		now := d.o.Clock.Now()
		e = &entry{agent: agentID, id: d.o.NewID(), created: now, due: now.Add(d.o.Coalesce)}
		d.pending[agentID] = e
	}
	e.add(r)
	d.mu.Unlock()
	d.wake()
}

func (e *entry) add(rs ...Reason) {
	for _, r := range rs {
		dup := false
		for _, x := range e.reasons {
			if x == r {
				dup = true
				break
			}
		}
		switch {
		case dup:
		case len(e.reasons) >= maxReasons:
			e.truncated = true
		default:
			e.reasons = append(e.reasons, r)
		}
	}
}

func (d *Dispatcher) Run(ctx context.Context) {
	defer d.wg.Wait()
	for {
		var timer <-chan time.Time
		if at, ok := d.startDue(ctx); ok {
			timer = d.o.Clock.At(at)
		}
		select {
		case <-ctx.Done():
			return
		case <-d.kick:
		case <-timer:
		}
	}
}

func (d *Dispatcher) startDue(ctx context.Context) (time.Time, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.o.Clock.Now()
	order := make([]*entry, 0, len(d.pending))
	for _, e := range d.pending {
		if !d.flying[e.agent] {
			order = append(order, e)
		}
	}
	sort.Slice(order, func(i, j int) bool {
		if !order[i].due.Equal(order[j].due) {
			return order[i].due.Before(order[j].due)
		}
		return order[i].agent < order[j].agent
	})
	for _, e := range order {
		if e.due.After(now) {
			return e.due, true
		}
		if len(d.flying) >= d.o.Workers {
			return time.Time{}, false
		}
		delete(d.pending, e.agent)
		d.flying[e.agent] = true
		e.attempts++
		d.wg.Add(1)
		go d.deliver(ctx, e)
	}
	return time.Time{}, false
}

func (d *Dispatcher) deliver(ctx context.Context, e *entry) {
	defer d.wg.Done()
	retry, err := d.send(ctx, e)
	d.mu.Lock()
	delete(d.flying, e.agent)
	switch {
	case err == nil:
		d.stats.Sent++
	case !retry || e.attempts >= d.o.MaxAttempts || ctx.Err() != nil:
		d.stats.Failed++
		d.o.Log.Warn("callback delivery failed", "agent", e.agent, "event", e.id, "attempts", e.attempts, "err", err)
	default:
		e.due = d.o.Clock.Now().Add(d.backoff(e.attempts))
		if n := d.pending[e.agent]; n != nil {
			e.add(n.reasons...)
			e.truncated = e.truncated || n.truncated
		}
		d.pending[e.agent] = e
	}
	d.mu.Unlock()
	d.wake()
}

func (d *Dispatcher) backoff(attempt int) time.Duration {
	b := d.o.Backoff
	for i := 1; i < attempt && b < d.o.MaxBackoff; i++ {
		b *= 2
	}
	b = min(b, d.o.MaxBackoff)
	return time.Duration(float64(b) * (0.8 + 0.4*d.o.Jitter()))
}

func (d *Dispatcher) send(ctx context.Context, e *entry) (retry bool, err error) {
	t, ok, err := d.o.Targets.Lookup(ctx, e.agent)
	if err != nil {
		return true, err
	}
	if !ok {
		return false, nil
	}
	body, err := json.Marshal(Event{ID: e.id, Type: EventWake, AgentID: e.agent, CreatedAt: e.created.UTC(), Reasons: e.reasons, Truncated: e.truncated})
	if err != nil {
		return false, err
	}
	ts := d.o.Clock.Now().Unix()
	ctx, cancel := context.WithTimeout(ctx, d.o.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.URL, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "subspace-callback/1")
	req.Header.Set(HeaderEvent, EventWake)
	req.Header.Set(HeaderEventID, e.id)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(HeaderSignature, Sign(t.Secret, ts, body))
	resp, err := d.o.Client.Do(req)
	if err != nil {
		return true, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrain))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return false, nil
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests:
		return true, fmt.Errorf("receiver answered %d", resp.StatusCode)
	}
	return false, fmt.Errorf("receiver answered %d", resp.StatusCode)
}
