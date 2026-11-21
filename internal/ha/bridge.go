package ha

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"hash/fnv"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/trace"
)

const (
	EventsTable  = "ha_events"
	EventsSchema = `CREATE TABLE IF NOT EXISTS ha_events (
  id TEXT PRIMARY KEY, payload TEXT NOT NULL, created_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ha_events_created ON ha_events(created_at);`

	maxNotify    = 7000
	defaultQueue = 8192
	keepEvents   = 5 * time.Minute
	pruneEvery   = time.Minute
	maxBackoff   = 2 * time.Second
	sendAttempts = 3
)

type Decoder func(typ string, raw json.RawMessage) any

type BridgeOptions struct {
	ID       string
	DSN      string
	Local    events.Hub
	Decode   Decoder
	OnRemote func(topics []string, typ string, data any)
	Queue    int
	Log      *slog.Logger
}

type wireEvent struct {
	Topics []string        `json:"t"`
	Type   string          `json:"y"`
	Data   json.RawMessage `json:"d"`
	Trace  string          `json:"tr,omitempty"`
}

type wire struct {
	Origin string      `json:"o"`
	Events []wireEvent `json:"e,omitempty"`
	Ref    string      `json:"r,omitempty"`
}

type Bridge struct {
	o       BridgeOptions
	out     chan wireEvent
	ready   chan struct{}
	once    sync.Once
	dropped atomic.Int64
	remote  atomic.Int64
}

func NewBridge(o BridgeOptions) *Bridge {
	if o.Queue <= 0 {
		o.Queue = defaultQueue
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Decode == nil {
		o.Decode = func(_ string, raw json.RawMessage) any {
			var v any
			_ = json.Unmarshal(raw, &v)
			return v
		}
	}
	return &Bridge{o: o, out: make(chan wireEvent, o.Queue), ready: make(chan struct{})}
}

func (b *Bridge) Publish(topics []string, typ string, data any) {
	b.PublishTrace(topics, typ, data, trace.SpanContext{})
}

func (b *Bridge) PublishTrace(topics []string, typ string, data any, tc trace.SpanContext) {
	events.PublishTrace(b.o.Local, topics, typ, data, tc)
	raw, err := json.Marshal(data)
	if err != nil {
		b.o.Log.Warn("ha bridge: event not encodable", "type", typ, "err", err)
		return
	}
	w := wireEvent{Topics: topics, Type: typ, Data: raw}
	if tc.Valid() {
		w.Trace = tc.Traceparent()
	}
	select {
	case b.out <- w:
	default:
		b.dropped.Add(1)
	}
}

func (b *Bridge) Subscribe(topics []string) events.Subscription { return b.o.Local.Subscribe(topics) }

func (b *Bridge) Subscribers() int { return b.o.Local.Subscribers() }

func (b *Bridge) Resume(topics []string, after int64) events.Resumed {
	if r, ok := b.o.Local.(events.Resumer); ok {
		return r.Resume(topics, after)
	}
	return events.Resumed{Sub: b.o.Local.Subscribe(topics), Resync: true}
}

func (b *Bridge) Seq() int64 {
	if s, ok := b.o.Local.(interface{ Seq() int64 }); ok {
		return s.Seq()
	}
	return 0
}

func (b *Bridge) SetReplay(n int) {
	if s, ok := b.o.Local.(interface{ SetReplay(int) }); ok {
		s.SetReplay(n)
	}
}

func (b *Bridge) Close() {
	if c, ok := b.o.Local.(interface{ Close() }); ok {
		c.Close()
	}
}

func (b *Bridge) Ready() <-chan struct{} { return b.ready }

func (b *Bridge) Stats() EventStats {
	return EventStats{Received: b.remote.Load(), Dropped: b.dropped.Load()}
}

func (b *Bridge) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Go(func() { b.listen(ctx) })
	wg.Go(func() { b.send(ctx) })
	wg.Wait()
}

func channelFor(schema string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(schema))
	return "subspace_events_" + strconv.FormatUint(h.Sum64(), 16)
}

func (b *Bridge) dial(ctx context.Context) (*pgx.Conn, string, error) {
	c, err := connect(ctx, b.o.DSN, "")
	if err != nil {
		return nil, "", err
	}
	var schema string
	if err := c.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		closeConn(c)
		return nil, "", err
	}
	return c, channelFor(schema), nil
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (b *Bridge) listen(ctx context.Context) {
	backoff := 50 * time.Millisecond
	for ctx.Err() == nil {
		c, ch, err := b.dial(ctx)
		if err == nil {
			_, err = c.Exec(ctx, `LISTEN "`+ch+`"`)
			if err != nil {
				closeConn(c)
			}
		}
		if err != nil {
			if ctx.Err() == nil {
				b.o.Log.Warn("ha bridge: listen", "err", err)
			}
			sleep(ctx, backoff)
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff = 50 * time.Millisecond
		b.once.Do(func() { close(b.ready) })
		for {
			n, err := c.WaitForNotification(ctx)
			if err != nil {
				if ctx.Err() == nil {
					b.o.Log.Warn("ha bridge: notification stream lost, reconnecting", "err", err)
				}
				break
			}
			b.receive(ctx, c, n.Payload)
		}
		closeConn(c)
	}
}

func (b *Bridge) receive(ctx context.Context, c *pgx.Conn, payload string) {
	var w wire
	if err := json.Unmarshal([]byte(payload), &w); err != nil || w.Origin == b.o.ID {
		return
	}
	if w.Ref != "" {
		var full string
		if err := c.QueryRow(ctx, `SELECT payload FROM ha_events WHERE id = $1`, w.Ref).Scan(&full); err != nil {
			b.o.Log.Warn("ha bridge: large event lookup", "id", w.Ref, "err", err)
			return
		}
		w = wire{}
		if err := json.Unmarshal([]byte(full), &w); err != nil {
			return
		}
	}
	for _, ev := range w.Events {
		data := b.o.Decode(ev.Type, ev.Data)
		b.remote.Add(1)
		tc, _ := trace.Parse(ev.Trace)
		events.PublishTrace(b.o.Local, ev.Topics, ev.Type, data, tc)
		if b.o.OnRemote != nil {
			b.o.OnRemote(ev.Topics, ev.Type, data)
		}
	}
}

func (b *Bridge) send(ctx context.Context) {
	var (
		c         *pgx.Conn
		ch        string
		lastPrune time.Time
	)
	defer func() {
		if c != nil {
			closeConn(c)
		}
	}()
	var pending *wireEvent
	for {
		var first wireEvent
		if pending != nil {
			first, pending = *pending, nil
		} else {
			select {
			case <-ctx.Done():
				return
			case first = <-b.out:
			}
		}
		batch := []wireEvent{first}
		size := len(first.Data) + 64
	drain:
		for size < maxNotify {
			select {
			case ev := <-b.out:
				if size+len(ev.Data)+64 > maxNotify {
					pending = &ev
					break drain
				}
				batch = append(batch, ev)
				size += len(ev.Data) + 64
			default:
				break drain
			}
		}
		for attempt := 1; ; attempt++ {
			if c == nil {
				var err error
				if c, ch, err = b.dial(ctx); err != nil {
					c = nil
					if ctx.Err() != nil {
						return
					}
					if attempt >= sendAttempts {
						b.dropped.Add(int64(len(batch)))
						break
					}
					sleep(ctx, time.Duration(attempt)*100*time.Millisecond)
					continue
				}
			}
			err := b.deliver(ctx, c, ch, batch)
			if err == nil {
				break
			}
			closeConn(c)
			c = nil
			if ctx.Err() != nil {
				return
			}
			if attempt >= sendAttempts {
				b.o.Log.Warn("ha bridge: events dropped", "n", len(batch), "err", err)
				b.dropped.Add(int64(len(batch)))
				break
			}
		}
		if c != nil && time.Since(lastPrune) > pruneEvery {
			lastPrune = time.Now()
			_, _ = c.Exec(ctx, `DELETE FROM ha_events WHERE created_at < $1`, time.Now().Add(-keepEvents).UnixMilli())
		}
	}
}

func (b *Bridge) deliver(ctx context.Context, c *pgx.Conn, ch string, batch []wireEvent) error {
	raw, err := json.Marshal(wire{Origin: b.o.ID, Events: batch})
	if err != nil {
		return nil
	}
	if len(raw) <= maxNotify {
		_, err = c.Exec(ctx, `SELECT pg_notify($1, $2)`, ch, string(raw))
		return err
	}
	if len(batch) > 1 {
		for _, ev := range batch {
			if err := b.deliver(ctx, c, ch, []wireEvent{ev}); err != nil {
				return err
			}
		}
		return nil
	}
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	ref := hex.EncodeToString(id)
	if _, err := c.Exec(ctx, `INSERT INTO ha_events (id, payload, created_at) VALUES ($1, $2, $3)`, ref, string(raw), time.Now().UnixMilli()); err != nil {
		return err
	}
	note, _ := json.Marshal(wire{Origin: b.o.ID, Ref: ref})
	_, err = c.Exec(ctx, `SELECT pg_notify($1, $2)`, ch, string(note))
	return err
}
