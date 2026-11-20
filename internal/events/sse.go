package events

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const DefaultPingInterval = 15 * time.Second

type seqer interface{ Seq() int64 }

type wireEvent struct {
	Type string `json:"type"`
	TS   string `json:"ts"`
	Data any    `json:"data"`
}

const (
	flushDelay = 5 * time.Millisecond
	sseBuffer  = 32 << 10
)

func encodeFrame(id int64, typ string, ts time.Time, data any) []byte {
	b, err := json.Marshal(wireEvent{Type: typ, TS: ts.UTC().Format(time.RFC3339), Data: data})
	if err != nil {
		b, _ = json.Marshal(wireEvent{Type: typ, TS: ts.UTC().Format(time.RFC3339), Data: nil})
	}
	out := make([]byte, 0, len(b)+len(typ)+40)
	if id >= 0 {
		out = append(out, "id: "...)
		out = strconv.AppendInt(out, id, 10)
		out = append(out, '\n')
	}
	out = append(out, "event: "...)
	out = append(out, typ...)
	out = append(out, "\ndata: "...)
	out = append(out, b...)
	return append(out, "\n\n"...)
}

func (ev Event) wire() []byte {
	if ev.enc == nil {
		return encodeFrame(ev.Seq, ev.Type, ev.TS, ev.Data)
	}
	ev.enc.once.Do(func() { ev.enc.b = encodeFrame(ev.Seq, ev.Type, ev.TS, ev.Data) })
	return ev.enc.b
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

		bw := bufio.NewWriterSize(w, sseBuffer)
		flush := func() error {
			if err := bw.Flush(); err != nil {
				return err
			}
			return rc.Flush()
		}
		lagged := func() error {
			if n := sub.TakeDropped(); n > 0 {
				_, err := bw.Write(encodeFrame(-1, "lagged", time.Now(), map[string]int{"dropped": n}))
				return err
			}
			return nil
		}
		send := func(ev Event) error {
			if err := lagged(); err != nil {
				return err
			}
			_, err := bw.Write(ev.wire())
			return err
		}

		if _, err := bw.Write(encodeFrame(seq, "hello", time.Now(), map[string]int64{"seq": seq})); err != nil {
			return
		}
		if err := flush(); err != nil {
			return
		}

		t := time.NewTicker(ping)
		defer t.Stop()
		timer := time.NewTimer(flushDelay)
		timer.Stop()
		defer timer.Stop()
		pending := false
		for {
			select {
			case <-r.Context().Done():
				return
			case <-t.C:
				if err := lagged(); err != nil {
					return
				}
				if _, err := fmt.Fprint(bw, ": ping\n\n"); err != nil {
					return
				}
				if err := flush(); err != nil {
					return
				}
			case <-timer.C:
				pending = false
				if err := flush(); err != nil {
					return
				}
			case ev, ok := <-sub.C():
				if !ok {
					if pending {
						_ = flush()
					}
					return
				}
				if err := send(ev); err != nil {
					return
				}
				if !pending {
					pending = true
					timer.Reset(flushDelay)
				}
			}
		}
	})
}
