package events

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const DefaultPingInterval = 15 * time.Second

const flushDelay = 20 * time.Millisecond

const writeBuffer = 32 << 10

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
	stall := max(2*ping, time.Second)
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

		arm := func() { _ = rc.SetWriteDeadline(time.Now().Add(stall)) }
		bw := bufio.NewWriterSize(w, writeBuffer)
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

		arm()
		if _, err := bw.Write(encodeFrame(seq, "hello", time.Now(), map[string]int64{"seq": seq})); err != nil {
			return
		}
		if err := flush(); err != nil {
			return
		}

		write := func(ev Event) error {
			if err := lagged(); err != nil {
				return err
			}
			_, err := bw.Write(ev.frame())
			return err
		}

		t := time.NewTicker(ping)
		defer t.Stop()
		ft := time.NewTimer(flushDelay)
		defer ft.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-t.C:
				arm()
				if err := lagged(); err != nil {
					return
				}
				if _, err := bw.WriteString(": ping\n\n"); err != nil {
					return
				}
				if err := flush(); err != nil {
					return
				}
			case ev, ok := <-sub.C():
				if !ok {
					return
				}
				arm()
				if err := write(ev); err != nil {
					return
				}
				ft.Reset(flushDelay)
				select {
				case <-r.Context().Done():
					return
				case <-ft.C:
				}
				arm()
			drain:
				for n := 0; n < SubscriberBuffer; n++ {
					select {
					case ev, ok := <-sub.C():
						if !ok {
							_ = flush()
							return
						}
						if err := write(ev); err != nil {
							return
						}
					default:
						break drain
					}
				}
				if err := flush(); err != nil {
					return
				}
			}
		}
	})
}
