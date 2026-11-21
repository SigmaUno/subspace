package server

import (
	"bufio"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMCPPushStreamClosesGracefullyOnShutdown(t *testing.T) {
	l := startLive(t)
	init, _ := http.NewRequest(http.MethodPost, l.base+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	init.Header.Set("Authorization", "Bearer "+l.tok)
	ir, err := http.DefaultClient.Do(init)
	if err != nil {
		t.Fatal(err)
	}
	_ = ir.Body.Close()
	req, _ := http.NewRequest(http.MethodGet, l.base+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+l.tok)
	req.Header.Set("Mcp-Session-Id", ir.Header.Get("Mcp-Session-Id"))
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream: %d", resp.StatusCode)
	}

	lines := make(chan string, 16)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	start := time.Now()
	l.cancel()
	var got []string
	for line := range lines {
		got = append(got, line)
	}
	if joined := strings.Join(got, "\n"); !strings.Contains(joined, "event: close") || !strings.Contains(joined, `"reason":"shutdown"`) {
		t.Fatalf("stream ended without a shutdown event: %q", got)
	}
	select {
	case <-l.done:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop with an open stream")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("shutdown took %s", d)
	}
}
