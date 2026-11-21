package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/servertest"
)

type sseFrame struct {
	id, event, data string
}

type sseClient struct {
	frames <-chan sseFrame
	cancel context.CancelFunc
}

func dialSSE(t *testing.T, e *servertest.Env, query, lastID string, viaHeader bool) *sseClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	url := e.TS.URL + "/api/v1/events?" + query
	if lastID != "" && !viaHeader {
		url += "&last_event_id=" + lastID
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+servertest.OwnerToken)
	if lastID != "" && viaHeader {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("events: %d", resp.StatusCode)
	}
	t.Cleanup(cancel)
	out := make(chan sseFrame, 4096)
	go func() {
		defer close(out)
		defer func() { _ = resp.Body.Close() }()
		var f sseFrame
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if f != (sseFrame{}) {
					out <- f
				}
				f = sseFrame{}
			case strings.HasPrefix(line, "id: "):
				f.id = line[4:]
			case strings.HasPrefix(line, "event: "):
				f.event = line[7:]
			case strings.HasPrefix(line, "data: "):
				f.data = line[6:]
			}
		}
	}()
	return &sseClient{frames: out, cancel: cancel}
}

func (c *sseClient) next(t *testing.T) sseFrame {
	t.Helper()
	select {
	case f, ok := <-c.frames:
		if !ok {
			t.Fatal("stream closed")
		}
		return f
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for frame")
	}
	return sseFrame{}
}

func payloadN(t *testing.T, f sseFrame) int {
	t.Helper()
	var ev struct {
		Data struct {
			N int `json:"n"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(f.data), &ev); err != nil {
		t.Fatal(err)
	}
	return ev.Data.N
}

func hubSeq(e *servertest.Env) string {
	return strconv.FormatInt(e.Srv.Hub.(interface{ Seq() int64 }).Seq(), 10)
}

func TestSSEForcedDisconnectsLoseNoEvents(t *testing.T) {
	e := servertest.New(t)
	start := hubSeq(e)
	const total = 600
	done := make(chan struct{})
	go func() {
		defer close(done)
		for n := 0; n < total; n++ {
			e.Srv.Hub.Publish([]string{"board"}, "probe.updated", map[string]int{"n": n})
			if n%50 == 0 {
				time.Sleep(time.Millisecond)
			}
		}
	}()

	var got []int
	lastID := start
	for conn := 0; len(got) < total; conn++ {
		if conn > 200 {
			t.Fatalf("still missing events after %d connections: have %d", conn, len(got))
		}
		c := dialSSE(t, e, "topics=board", lastID, conn%2 == 0)
		for taken, lagged := 0, false; taken < 40 && len(got) < total && !lagged; {
			f := c.next(t)
			switch f.event {
			case "lagged":
				lagged = true
			case "resync":
				t.Fatalf("unexpected resync at id %s", lastID)
			case "probe.updated":
				got = append(got, payloadN(t, f))
				lastID = f.id
				taken++
			}
		}
		c.cancel()
	}
	<-done
	for i, n := range got {
		if n != i {
			t.Fatalf("event %d carries n=%d", i, n)
		}
	}
}

func TestSSEOverflowedBufferSendsResync(t *testing.T) {
	t.Setenv("SUBSPACE_SSE_REPLAY", "5")
	e := servertest.New(t)
	start, _ := strconv.ParseInt(hubSeq(e), 10, 64)
	for n := 0; n < 20; n++ {
		e.Srv.Hub.Publish([]string{"board"}, "probe.updated", map[string]int{"n": n})
	}

	c := dialSSE(t, e, "topics=board", strconv.FormatInt(start+3, 10), true)
	defer c.cancel()
	hello := c.next(t)
	if hello.event != "hello" {
		t.Fatalf("first frame %+v", hello)
	}
	resync := c.next(t)
	if resync.event != "resync" || resync.id != hello.id {
		t.Fatalf("resync %+v after hello %+v", resync, hello)
	}

	e.Srv.Hub.Publish([]string{"board"}, "probe.updated", map[string]int{"n": 99})
	f := c.next(t)
	if f.event != "probe.updated" || payloadN(t, f) != 99 {
		t.Fatalf("live frame %+v", f)
	}
	if id, _ := strconv.ParseInt(f.id, 10, 64); id <= start+20 {
		t.Fatalf("live id %d", id)
	}
}

func TestSSEReplayHonorsTopics(t *testing.T) {
	e := servertest.New(t)
	start := hubSeq(e)
	e.Srv.Hub.Publish([]string{"board"}, "probe.updated", map[string]int{"n": 1})
	e.Srv.Hub.Publish([]string{"control"}, "probe.updated", map[string]int{"n": 2})
	e.Srv.Hub.Publish([]string{"board"}, "probe.updated", map[string]int{"n": 3})

	c := dialSSE(t, e, "topics=board", start, false)
	defer c.cancel()
	if f := c.next(t); f.event != "hello" {
		t.Fatalf("first frame %+v", f)
	}
	for _, want := range []int{1, 3} {
		if f := c.next(t); f.event != "probe.updated" || payloadN(t, f) != want {
			t.Fatalf("want n=%d, got %+v", want, f)
		}
	}
}
