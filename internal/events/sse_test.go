package events

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

type frame struct {
	id      string
	event   string
	data    string
	comment string
}

func readFrames(sc *bufio.Scanner, out chan<- frame) {
	defer close(out)
	var f frame
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if f != (frame{}) {
				out <- f
			}
			f = frame{}
		case strings.HasPrefix(line, ":"):
			f.comment = strings.TrimSpace(line[1:])
		case strings.HasPrefix(line, "id: "):
			f.id = line[4:]
		case strings.HasPrefix(line, "event: "):
			f.event = line[7:]
		case strings.HasPrefix(line, "data: "):
			f.data = line[6:]
		}
	}
}

func next(t *testing.T, ch <-chan frame) frame {
	t.Helper()
	select {
	case f, ok := <-ch:
		if !ok {
			t.Fatal("stream closed")
		}
		return f
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for frame")
	}
	return frame{}
}

func open(t *testing.T, srv *httptest.Server, query string) (<-chan frame, context.CancelFunc) {
	t.Helper()
	ch, stop, err := dial(srv, query)
	if err != nil {
		t.Fatal(err)
	}
	return ch, stop
}

func dial(srv *httptest.Server, query string) (<-chan frame, context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events"+query, nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		cancel()
		_ = resp.Body.Close()
		return nil, nil, fmt.Errorf("content-type %q", ct)
	}
	ch := make(chan frame, 1024)
	go readFrames(bufio.NewScanner(resp.Body), ch)
	return ch, func() { cancel(); _ = resp.Body.Close() }, nil
}

func waitSubs(t *testing.T, h Hub, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.Subscribers() != n {
		if time.Now().After(deadline) {
			t.Fatalf("subscribers = %d, want %d", h.Subscribers(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSSEFrames(t *testing.T) {
	h := NewHub()
	h.Publish([]string{"board"}, "task.created", nil)
	srv := httptest.NewServer(NewSSEHandler(h, nil, 50*time.Millisecond))
	defer srv.Close()

	ch, stop := open(t, srv, "?topics=task:T-3,control")
	f := next(t, ch)
	if f.event != "hello" || f.id != "1" {
		t.Fatalf("hello frame %+v", f)
	}
	var hello struct {
		Type string           `json:"type"`
		TS   string           `json:"ts"`
		Data map[string]int64 `json:"data"`
	}
	if err := json.Unmarshal([]byte(f.data), &hello); err != nil || hello.Type != "hello" || hello.Data["seq"] != 1 {
		t.Fatalf("hello data %q err %v", f.data, err)
	}

	h.Publish([]string{"board", "task:T-9"}, "task.updated", map[string]string{"id": "T-9"})
	h.Publish([]string{"board", "task:T-3"}, "task.updated", map[string]string{"id": "T-3"})

	for {
		f = next(t, ch)
		if f.comment == "ping" {
			continue
		}
		break
	}
	if f.event != "task.updated" || f.id != "3" {
		t.Fatalf("frame %+v", f)
	}
	var ev struct {
		Type string            `json:"type"`
		TS   string            `json:"ts"`
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(f.data), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "task.updated" || ev.Data["id"] != "T-3" {
		t.Fatalf("event %+v", ev)
	}
	if _, err := time.Parse(time.RFC3339, ev.TS); err != nil || !strings.HasSuffix(ev.TS, "Z") {
		t.Fatalf("ts %q", ev.TS)
	}

	sawPing := false
	for i := 0; i < 5 && !sawPing; i++ {
		f = next(t, ch)
		sawPing = f.comment == "ping" && f.event == ""
	}
	if !sawPing {
		t.Fatal("no ping")
	}

	stop()
	waitSubs(t, h, 0)
}

func TestSSELagged(t *testing.T) {
	h := NewHub()
	block := make(chan struct{})

	topics := func(r *http.Request) []string {
		return []string{"board"}
	}
	inner := NewSSEHandler(h, topics, time.Hour)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(&stallWriter{ResponseWriter: w, block: block}, r)
	}))
	defer srv.Close()

	type res struct {
		ch   <-chan frame
		stop context.CancelFunc
		err  error
	}
	rc := make(chan res, 1)
	go func() {
		ch, stop, err := dial(srv, "")
		rc <- res{ch, stop, err}
	}()
	waitSubs(t, h, 1)
	const n = SubscriberBuffer + 10
	for i := 0; i < n; i++ {
		h.Publish([]string{"board"}, "task.updated", i)
	}
	close(block)
	r := <-rc
	if r.err != nil {
		t.Fatal(r.err)
	}
	defer r.stop()

	if f := next(t, r.ch); f.event != "hello" {
		t.Fatalf("first frame %+v", f)
	}
	f := next(t, r.ch)
	if f.event != "lagged" || f.id != "" {
		t.Fatalf("expected lagged, got %+v", f)
	}
	var lag struct {
		Data struct {
			Dropped int `json:"dropped"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(f.data), &lag); err != nil || lag.Data.Dropped != 10 {
		t.Fatalf("lagged data %q", f.data)
	}
	f = next(t, r.ch)
	if f.event != "task.updated" || f.id != strconv.Itoa(n-SubscriberBuffer+1) {
		t.Fatalf("first kept event %+v", f)
	}
}

type stallWriter struct {
	http.ResponseWriter
	block chan struct{}
}

func (s *stallWriter) WriteHeader(code int) {
	<-s.block
	s.ResponseWriter.WriteHeader(code)
}

func (s *stallWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func TestSSEEndsWithCloseEventOnShutdown(t *testing.T) {
	h := NewHub()
	done := make(chan struct{})
	inner := NewSSEHandler(h, nil, time.Hour)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(w, r.WithContext(WithShutdown(r.Context(), done)))
	}))
	defer srv.Close()

	var chans []<-chan frame
	for range 3 {
		ch, stop := open(t, srv, "")
		defer stop()
		if f := next(t, ch); f.event != "hello" {
			t.Fatalf("first frame %+v", f)
		}
		chans = append(chans, ch)
	}
	waitSubs(t, h, 3)

	close(done)

	for _, ch := range chans {
		f := next(t, ch)
		if f.event != "close" || !strings.Contains(f.data, `"reason":"shutdown"`) {
			t.Fatalf("frame after shutdown: %+v", f)
		}
		select {
		case _, ok := <-ch:
			if ok {
				t.Fatal("frame after close event")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("stream still open after close event")
		}
	}
	waitSubs(t, h, 0)
}
