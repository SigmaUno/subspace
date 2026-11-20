package events

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const DefaultPingInterval = 15 * time.Second

type shutdownKey struct{}

func WithShutdown(ctx context.Context, done <-chan struct{}) context.Context {
	return context.WithValue(ctx, shutdownKey{}, done)
}

func shutdownFrom(ctx context.Context) <-chan struct{} {
	done, _ := ctx.Value(shutdownKey{}).(<-chan struct{})
	return done
}

type seqer interface{ Seq() int64 }

type wireEvent struct {
	Type string `json:"type"`
	TS   string `json:"ts"`
	Data any    `json:"data"`
}

type encoded struct {
	once sync.Once
	b    []byte
}

func encodeFrame(id int64, typ string, ts time.Time, data any) []byte {
	b, err := json.Marshal(wireEvent{Type: typ, TS: ts.UTC().Format(time.RFC3339), Data: data})
	if err != nil {
		b, _ = json.Marshal(wireEvent{Type: typ, TS: ts.UTC().Format(time.RFC3339), Data: nil})
	}
	buf := make([]byte, 0, len(b)+len(typ)+40)
	if id >= 0 {
		buf = append(buf, "id: "...)
		buf = strconv.AppendInt(buf, id, 10)
		buf = append(buf, '\n')
	}
	buf = append(buf, "event: "...)
	buf = append(buf, typ...)
	buf = append(buf, "\ndata: "...)
	buf = append(buf, b...)
	return append(buf, "\n\n"...)
}

func (e *Event) frame() []byte {
	if e.enc == nil {
		return encodeFrame(e.Seq, e.Type, e.TS, e.Data)
	}
	e.enc.once.Do(func() { e.enc.b = encodeFrame(e.Seq, e.Type, e.TS, e.Data) })
	return e.enc.b
}

func TopicsFromQuery(r *http.Request) []string {
	var out []string
	for _, v := range r.URL.Query()["topics"] {
		for _, t := range strings.Split(v, ",") {
			if t = strings.TrimSpace(t); t != "" {
				out = append(out, t)
			}
		}
	}
	if len(out) == 0 {
		return []string{TopicAll}
	}
	return out
}

func SSEHandler(h Hub, topics func(*http.Request) []string) http.Handler {
	return NewSSEHandler(h, topics, DefaultPingInterval)
}

func NewSSEHandler(h Hub, topics func(*http.Request) []string, ping time.Duration) http.Handler {
	if topics == nil {
		topics = TopicsFromQuery
	}
	if ping <= 0 {
		ping = DefaultPingInterval
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		shutdown := shutdownFrom(r.Context())
		sub := h.Subscribe(topics(r))
		defer sub.Close()

		var seq int64
		if s, ok := h.(seqer); ok {
			seq = s.Seq()
		}

		hd := w.Header()
		hd.Set("Content-Type", "text/event-stream")
		hd.Set("Cache-Control", "no-cache")
		hd.Set("Connection", "keep-alive")
		hd.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		lagged := func() error {
			if n := sub.TakeDropped(); n > 0 {
				_, err := w.Write(encodeFrame(-1, "lagged", time.Now(), map[string]int{"dropped": n}))
				return err
			}
			return nil
		}

		send := func(ev Event) (bool, error) {
			for n := 0; ; n++ {
				if err := lagged(); err != nil {
					return false, err
				}
				if _, err := w.Write(ev.frame()); err != nil {
					return false, err
				}
				if n == SubscriberBuffer {
					return true, rc.Flush()
				}
				select {
				case next, ok := <-sub.C():
					if !ok {
						return false, rc.Flush()
					}
					ev = next
				default:
					return true, rc.Flush()
				}
			}
		}

		if _, err := w.Write(encodeFrame(seq, "hello", time.Now(), map[string]int64{"seq": seq})); err != nil {
			return
		}
		if err := rc.Flush(); err != nil {
			return
		}

		t := time.NewTicker(ping)
		defer t.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-shutdown:
				if _, err := w.Write(encodeFrame(-1, "close", time.Now(), map[string]string{"reason": "shutdown"})); err == nil {
					_ = rc.Flush()
				}
				return
			case <-t.C:
				if err := lagged(); err != nil {
					return
				}
				if _, err := w.Write([]byte(": ping\n\n")); err != nil {
					return
				}
				if err := rc.Flush(); err != nil {
					return
				}
			case ev, ok := <-sub.C():
				if !ok {
					return
				}
				if more, err := send(ev); err != nil || !more {
					return
				}
			}
		}
	})
}
