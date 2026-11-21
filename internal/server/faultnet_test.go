package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/faultnet"
)

type netNode struct {
	srv    *Server
	addr   string
	cancel context.CancelFunc
	done   chan error
}

func startNetNode(t *testing.T, cfg Config, ln net.Listener) *netNode {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(t.TempDir(), "t.db")
	}
	cfg.OwnerToken = ownerTok
	cfg.PublicURL = "http://sub.test"
	cfg.Log = slog.New(slog.DiscardHandler)
	s, err := New(ctx, cfg)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if ln == nil {
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			cancel()
			t.Fatal(err)
		}
	}
	n := &netNode{srv: s, addr: ln.Addr().String(), cancel: cancel, done: make(chan error, 1)}
	go func() { n.done <- s.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-n.done; err != nil {
			t.Errorf("serve: %v", err)
		}
		_ = s.Close()
	})
	return n
}

func (n *netNode) proxy(t *testing.T, seed uint64, f faultnet.Faults) *faultnet.Proxy {
	t.Helper()
	p, err := faultnet.Listen(n.addr, seed, f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func openStream(t *testing.T, base string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/events?topics=board", nil)
	req.Header.Set("Authorization", "Bearer "+ownerTok)
	resp, err := (&http.Client{Transport: &http.Transport{}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events: %d", resp.StatusCode)
	}
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "id: ") {
		t.Fatalf("hello: %q %v", line, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestShutdownIsNotHeldByStuckStreamClients(t *testing.T) {
	n := startNetNode(t, Config{Lease: time.Minute, ShutdownTimeout: 20 * time.Second}, nil)
	p := n.proxy(t, 31, faultnet.Faults{})
	for range 4 {
		openStream(t, p.URL())
	}
	p.Set(faultnet.Faults{HalfOpen: 1})
	payload := strings.Repeat("x", 4<<10)
	deadline := time.Now().Add(10 * time.Second)
	for p.Stats().HalfOpen < 4 {
		if time.Now().After(deadline) {
			t.Fatalf("streams never stalled: %+v", p.Stats())
		}
		for range 200 {
			n.srv.Hub.Publish([]string{"board"}, "test.flood", payload)
		}
		time.Sleep(10 * time.Millisecond)
	}
	big := strings.Repeat("y", 256<<10)
	for range 400 {
		n.srv.Hub.Publish([]string{"board"}, "test.flood", big)
		time.Sleep(time.Millisecond)
	}

	start := time.Now()
	n.cancel()
	select {
	case err := <-n.done:
		n.done <- err
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("shutdown held by stuck clients")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("shutdown took %v with stuck stream clients", d)
	}
}

type countingListener struct {
	net.Listener
	mu         sync.Mutex
	open, peak int
	accepted   int
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.open++
	l.accepted++
	l.peak = max(l.peak, l.open)
	l.mu.Unlock()
	return &countedConn{Conn: c, l: l}, nil
}

func (l *countingListener) stats() (open, peak, accepted int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.open, l.peak, l.accepted
}

type countedConn struct {
	net.Conn
	l    *countingListener
	once sync.Once
}

func (c *countedConn) Close() error {
	c.once.Do(func() {
		c.l.mu.Lock()
		c.l.open--
		c.l.mu.Unlock()
	})
	return c.Conn.Close()
}

func closedWithin(c net.Conn, d time.Duration) error {
	_ = c.SetReadDeadline(time.Now().Add(d))
	buf := make([]byte, 4096)
	for {
		if _, err := c.Read(buf); err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				return err
			}
			return nil
		}
	}
}

func TestSlowClientsDoNotExhaustServer(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cl := &countingListener{Listener: inner}
	const limit = 8
	n := startNetNode(t, Config{Lease: time.Minute, HeaderTimeout: 300 * time.Millisecond, BodyTimeout: 400 * time.Millisecond,
		IdleTimeout: 400 * time.Millisecond, MaxConns: limit, ShutdownTimeout: 5 * time.Second}, cl)
	trickle := n.proxy(t, 41, faultnet.Faults{Bandwidth: 20})
	stall := n.proxy(t, 43, faultnet.Faults{AcceptStall: time.Hour})

	var wg sync.WaitGroup
	slow := make(chan error, 64)
	for i := range 24 {
		wg.Go(func() {
			var (
				c   net.Conn
				err error
			)
			switch i % 4 {
			case 0:
				c, err = net.Dial("tcp", trickle.Addr())
				if err == nil {
					_, err = io.WriteString(c, "GET /healthz HTTP/1.1\r\nHost: sub.test\r\nX-Pad: "+strings.Repeat("a", 64)+"\r\n\r\n")
				}
			case 1:
				c, err = net.Dial("tcp", n.addr)
			case 2:
				c, err = net.Dial("tcp", n.addr)
				if err == nil {
					_, err = io.WriteString(c, "POST /api/v1/briefs HTTP/1.1\r\nHost: sub.test\r\nAuthorization: Bearer "+ownerTok+"\r\nContent-Length: 4096\r\n\r\n{\"title\":")
				}
			case 3:
				c, err = net.Dial("tcp", stall.Addr())
			}
			if err != nil {
				slow <- err
				return
			}
			defer func() { _ = c.Close() }()
			if i%4 == 3 {
				time.Sleep(2 * time.Second)
				return
			}
			if err := closedWithin(c, 10*time.Second); err != nil {
				slow <- fmt.Errorf("slow client %d not disconnected: %w", i, err)
			}
		})
	}

	hc := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	for i := range 20 {
		start := time.Now()
		resp, err := hc.Get("http://" + n.addr + "/healthz")
		if err != nil {
			t.Fatalf("healthy request %d: %v", i, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("healthy request %d: %d", i, resp.StatusCode)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("healthy request %d waited %v", i, d)
		}
	}
	wg.Wait()
	close(slow)
	for err := range slow {
		t.Error(err)
	}
	if _, peak, accepted := cl.stats(); peak > limit || accepted < 20 {
		t.Fatalf("peak %d open connections (limit %d), %d accepted", peak, limit, accepted)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		open, _, _ := cl.stats()
		if open == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d connections still open after slow clients left", open)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readFrames(t *testing.T, resp *http.Response, n int) []sseFrameRaw {
	t.Helper()
	out := make(chan []sseFrameRaw, 1)
	go func() {
		var frames []sseFrameRaw
		var f sseFrameRaw
		sc := bufio.NewScanner(resp.Body)
		for len(frames) < n && sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if f.event != "" {
					frames = append(frames, f)
				}
				f = sseFrameRaw{}
			case strings.HasPrefix(line, "id: "):
				f.id = line[4:]
			case strings.HasPrefix(line, "event: "):
				f.event = line[7:]
			}
		}
		out <- frames
	}()
	select {
	case frames := <-out:
		return frames
	case <-time.After(10 * time.Second):
		t.Fatalf("fewer than %d frames", n)
		return nil
	}
}

type sseFrameRaw struct{ id, event string }

func TestEventResumeAcrossRestartNeverSkipsSilently(t *testing.T) {
	db := filepath.Join(t.TempDir(), "t.db")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	a := startNetNode(t, Config{DBPath: db, Lease: time.Minute}, ln)
	p := a.proxy(t, 53, faultnet.Faults{Latency: time.Millisecond})

	req, _ := http.NewRequest(http.MethodGet, p.URL()+"/api/v1/events?topics=board", nil)
	req.Header.Set("Authorization", "Bearer "+ownerTok)
	resp, err := (&http.Client{Transport: &http.Transport{}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	for n := range 30 {
		a.srv.Hub.Publish([]string{"board"}, "probe.updated", map[string]int{"n": n})
	}
	frames := readFrames(t, resp, 31)
	_ = resp.Body.Close()
	last := frames[len(frames)-1].id

	a.cancel()
	if err := <-a.done; err != nil {
		t.Fatal(err)
	}
	a.done <- nil
	_ = a.srv.Close()

	ln, err = net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	b := startNetNode(t, Config{DBPath: db, Lease: time.Minute}, ln)
	for n := range 100 {
		b.srv.Hub.Publish([]string{"board"}, "probe.updated", map[string]int{"n": n})
	}
	req, _ = http.NewRequest(http.MethodGet, p.URL()+"/api/v1/events?topics=board", nil)
	req.Header.Set("Authorization", "Bearer "+ownerTok)
	req.Header.Set("Last-Event-ID", last)
	resp, err = (&http.Client{Transport: &http.Transport{}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	frames = readFrames(t, resp, 2)
	if frames[0].event != "hello" || frames[1].event != "resync" {
		t.Fatalf("resume from %s after restart got %+v, want hello then resync", last, frames)
	}
	prev, _ := strconv.ParseInt(last, 10, 64)
	if cur, _ := strconv.ParseInt(frames[1].id, 10, 64); cur <= prev {
		t.Fatalf("event id went backwards across restart: %s after %s", frames[1].id, last)
	}
}
