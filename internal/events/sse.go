package events

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// DefaultPingInterval is how often a ": ping" comment is written to idle SSE streams.
const DefaultPingInterval = 15 * time.Second

// seqer is implemented by hubs that expose the last published sequence number.
type seqer interface{ Seq() int64 }

// wireEvent is the JSON payload of an SSE "data:" line (docs/api.md §4).
type wireEvent struct {
	Type string `json:"type"`
	TS   string `json:"ts"`
	Data any    `json:"data"`
}

// TopicsFromQuery parses the comma separated "topics" query parameter (default "all").
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

// SSEHandler streams hub events as text/event-stream with the default ping interval.
// topics selects the subscription topics per request (nil = TopicsFromQuery).
// Authentication is the caller's responsibility.
func SSEHandler(h Hub, topics func(*http.Request) []string) http.Handler {
	return NewSSEHandler(h, topics, DefaultPingInterval)
}

// NewSSEHandler is SSEHandler with a configurable ping interval.
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

		write := func(id int64, typ string, ts time.Time, data any) error {
			b, err := json.Marshal(wireEvent{Type: typ, TS: ts.UTC().Format(time.RFC3339), Data: data})
			if err != nil {
				b, _ = json.Marshal(wireEvent{Type: typ, TS: ts.UTC().Format(time.RFC3339), Data: nil})
			}
			if id >= 0 {
				if _, err := fmt.Fprintf(w, "id: %d\n", id); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, b); err != nil {
				return err
			}
			return rc.Flush()
		}
		// lagged carries no id: it is not part of the hub sequence.
		lagged := func() error {
			if n := sub.TakeDropped(); n > 0 {
				return write(-1, "lagged", time.Now(), map[string]int{"dropped": n})
			}
			return nil
		}

		if err := write(seq, "hello", time.Now(), map[string]int64{"seq": seq}); err != nil {
			return
		}

		t := time.NewTicker(ping)
		defer t.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-t.C:
				if err := lagged(); err != nil {
					return
				}
				if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
					return
				}
				if err := rc.Flush(); err != nil {
					return
				}
			case ev, ok := <-sub.C():
				if !ok {
					return
				}
				if err := lagged(); err != nil {
					return
				}
				if err := write(ev.Seq, ev.Type, ev.TS, ev.Data); err != nil {
					return
				}
			}
		}
	})
}
