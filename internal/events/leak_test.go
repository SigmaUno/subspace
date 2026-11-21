package events

import (
	"bufio"
	"io"
	"net"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

func settleGoroutines(t *testing.T, base int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for runtime.NumGoroutine() > base+2 {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			t.Fatalf("goroutines %d, base %d\n%s", runtime.NumGoroutine(), base, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func rawSSE(t *testing.T, srv *httptest.Server) *net.TCPConn {
	t.Helper()
	c, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	tc := c.(*net.TCPConn)
	_ = tc.SetReadBuffer(4096)
	if _, err := io.WriteString(tc, "GET /events HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	return tc
}

func TestSSEAbruptDisconnectReleasesSubscriber(t *testing.T) {
	h := NewHub()
	srv := httptest.NewServer(NewSSEHandler(h, nil, 20*time.Millisecond))
	defer srv.Close()
	base := runtime.NumGoroutine()

	for range 50 {
		c := rawSSE(t, srv)
		waitSubs(t, h, 1)
		_ = c.SetLinger(0)
		_ = c.Close()
		h.Publish([]string{"board"}, "task.updated", nil)
		waitSubs(t, h, 0)
	}
	settleGoroutines(t, base)
}

func TestSSEStalledReaderIsDropped(t *testing.T) {
	h := NewHub()
	srv := httptest.NewServer(NewSSEHandler(h, nil, 20*time.Millisecond))
	defer srv.Close()

	c := rawSSE(t, srv)
	defer func() { _ = c.Close() }()
	waitSubs(t, h, 1)

	payload := strings.Repeat("x", 64<<10)
	deadline := time.Now().Add(30 * time.Second)
	for h.Subscribers() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("handler stuck writing to a client that stopped reading")
		}
		h.Publish([]string{"board"}, "task.updated", payload)
		time.Sleep(2 * time.Millisecond)
	}
}

func TestHubCloseEndsStreams(t *testing.T) {
	h := NewHub()
	srv := httptest.NewServer(NewSSEHandler(h, nil, time.Hour))
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	waitSubs(t, h, 1)

	h.(interface{ Close() }).Close()
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, bufio.NewReader(resp.Body))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream still open after hub close")
	}
	waitSubs(t, h, 0)

	s := h.Subscribe([]string{"board"})
	if _, ok := <-s.C(); ok {
		t.Fatal("subscription on a closed hub delivered an event")
	}
	s.Close()
	if h.Subscribers() != 0 {
		t.Fatalf("subscribers = %d", h.Subscribers())
	}
}
