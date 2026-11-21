package server

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShutdownEndsEventStreams(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := New(ctx, Config{Addr: addr, DBPath: filepath.Join(t.TempDir(), "s.db"), OwnerToken: ownerTok, PublicURL: "http://sub.test"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ran := make(chan error, 1)
	go func() { ran <- s.Run(ctx) }()

	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+ownerTok)
	var resp *http.Response
	for range 100 {
		if resp, err = http.DefaultClient.Do(req); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	br := bufio.NewReader(resp.Body)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("no hello: %v", err)
		}
		if strings.HasPrefix(line, "event: hello") {
			break
		}
	}

	begin := time.Now()
	cancel()
	select {
	case err := <-ran:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return while an event stream was open")
	}
	if d := time.Since(begin); d > 2*time.Second {
		t.Fatalf("shutdown took %s with one open event stream", d)
	}
	end := make(chan struct{})
	go func() {
		_, _ = br.ReadString('\x00')
		close(end)
	}()
	select {
	case <-end:
	case <-time.After(2 * time.Second):
		t.Fatal("event stream still open after shutdown")
	}
}
