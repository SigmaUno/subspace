package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func pushServer(t *testing.T, p *Push) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.Serve(w, r, "s1", "a1")
	}))
	t.Cleanup(ts.Close)
	return ts
}

type rawFrame struct {
	id     int64
	method string
}

func readFrames(t *testing.T, ts *httptest.Server, lastID string) (<-chan rawFrame, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL, nil)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	out := make(chan rawFrame, 1024)
	go func() {
		defer close(out)
		defer func() { _ = resp.Body.Close() }()
		var f rawFrame
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if f.method != "" {
					out <- f
				}
				f = rawFrame{}
			case strings.HasPrefix(line, "id: "):
				f.id, _ = strconv.ParseInt(line[4:], 10, 64)
			case strings.HasPrefix(line, "data: "):
				var m struct{ Method string }
				_ = json.Unmarshal([]byte(line[6:]), &m)
				f.method = m.Method
			}
		}
	}()
	t.Cleanup(cancel)
	return out, cancel
}

func TestPushToolsChangedReachesOnlyTheAgentsStreams(t *testing.T) {
	p := NewPush()
	ts := pushServer(t, p)
	frames, _ := readFrames(t, ts, "")
	time.Sleep(50 * time.Millisecond)
	p.ToolsChanged("other")
	p.ToolsChanged("a1")
	select {
	case f := <-frames:
		if f.method != MethodToolsChanged || f.id != 1 {
			t.Fatalf("frame %+v", f)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no list_changed")
	}
}

func TestPushConcurrentProducersNeverReorderOrSilentlyDrop(t *testing.T) {
	p := NewPush()
	p.Buffer = 8
	ts := pushServer(t, p)
	frames, cancel := readFrames(t, ts, "")
	defer cancel()
	time.Sleep(50 * time.Millisecond)

	const producers, each = 8, 200
	var wg sync.WaitGroup
	for range producers {
		wg.Go(func() {
			for range each {
				p.Event("a1", map[string]any{"type": "message"})
			}
		})
	}
	wg.Wait()
	p.Broadcast(MethodSubspaceEvent, map[string]any{"type": "paused", "scope": "all"})

	var last int64
	var resyncs int
	events := int64(0)
	deadline := time.After(5 * time.Second)
	for last < producers*each+1 {
		select {
		case f := <-frames:
			if f.method == MethodSubspaceResync {
				resyncs++
				continue
			}
			if f.id <= last {
				t.Fatalf("id %d after %d", f.id, last)
			}
			if f.id != last+1 && resyncs == 0 {
				t.Fatalf("gap %d..%d without resync", last, f.id)
			}
			last = f.id
			events++
		case <-deadline:
			t.Fatalf("stalled at id %d after %d events", last, events)
		}
	}
	if events < 1 || (events < producers*each+1 && resyncs == 0) {
		t.Fatalf("%d events, %d resyncs", events, resyncs)
	}
}
