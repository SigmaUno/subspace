package events

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// SeqHub is a Hub that reports its latest sequence number (used for the hello event).
type SeqHub interface {
	Hub
	Seq() int64
}

// TopicsFromQuery parses ?topics=a,b (default all).
func TopicsFromQuery(r *http.Request) []string {
	q := strings.TrimSpace(r.URL.Query().Get("topics"))
	if q == "" {
		return []string{"all"}
	}
	var out []string
	for _, t := range strings.Split(q, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// SSEHandler streams events as text/event-stream. Authentication is done by the caller.
func SSEHandler(h Hub, ping time.Duration) http.Handler {
	if ping <= 0 {
		ping = 15 * time.Second
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		sub := h.Subscribe(TopicsFromQuery(r))
		defer sub.Close()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		var seq int64
		if sh, ok := h.(SeqHub); ok {
			seq = sh.Seq()
		}
		writeFrame(w, Event{Seq: seq, Type: "hello", TS: time.Now().UTC(), Data: map[string]any{"seq": seq}})
		fl.Flush()
		t := time.NewTicker(ping)
		defer t.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case ev := <-sub.C():
				if d := sub.TakeDropped(); d > 0 {
					writeFrame(w, Event{Seq: ev.Seq, Type: "lagged", TS: time.Now().UTC(), Data: map[string]any{"dropped": d}})
				}
				writeFrame(w, ev)
				fl.Flush()
			case <-t.C:
				_, _ = fmt.Fprint(w, ": ping\n\n")
				fl.Flush()
			}
		}
	})
}

func writeFrame(w http.ResponseWriter, ev Event) {
	b, _ := json.Marshal(map[string]any{"type": ev.Type, "ts": ev.TS, "data": ev.Data})
	_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Type, b)
}
