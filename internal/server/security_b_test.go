package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/spawner"
)

func startHTTP(t *testing.T, body, idle time.Duration) (*env, string) {
	t.Helper()
	e := newEnvCfg(t, Config{BodyTimeout: body, IdleTimeout: idle})
	return e, e.ts.Listener.Addr().String()
}

func newEnvCfg(t *testing.T, cfg Config) *env {
	t.Helper()
	cfg.DBPath, cfg.OwnerToken, cfg.PublicURL = filepath.Join(t.TempDir(), "t.db"), ownerTok, "http://sub.test"
	cfg.FlushEvery, cfg.SweepEvery, cfg.Lease = 50*time.Millisecond, 50*time.Millisecond, time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	s, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	go s.Board.Run(ctx)
	ts := httptest.NewUnstartedServer(nil)
	ts.Config = s.httpServer()
	ts.Start()
	t.Cleanup(func() { ts.Close(); cancel(); _ = s.Close() })
	return &env{t: t, ts: ts, srv: s}
}

func TestStalledRequestBodyDoesNotHoldConnection(t *testing.T) {
	e, addr := startHTTP(t, 200*time.Millisecond, time.Minute)
	agent := e.register("w1", "worker", "claude-code")
	cases := []struct{ path, auth string }{
		{"/mcp", ""},
		{"/mcp", agent},
		{"/api/v1/briefs", ""},
		{"/api/v1/briefs", ownerTok},
	}
	for _, c := range cases {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		hdr := ""
		if c.auth != "" {
			hdr = "Authorization: Bearer " + c.auth + "\r\n"
		}
		_, _ = fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: sub.test\r\nContent-Type: application/json\r\n%sContent-Length: 4096\r\n\r\n{", c.path, hdr)
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, err = io.ReadAll(conn)
		_ = conn.Close()
		if err != nil {
			t.Fatalf("%s (auth=%v): stalled body kept the connection open: %v", c.path, c.auth != "", err)
		}
	}
}

func TestIdleKeepAliveConnectionIsClosed(t *testing.T) {
	_, addr := startHTTP(t, time.Minute, 200*time.Millisecond)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = fmt.Fprint(conn, "GET /healthz HTTP/1.1\r\nHost: sub.test\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := br.ReadByte(); err != io.EOF {
		t.Fatalf("idle keep-alive connection not closed: %v", err)
	}
}

func TestEventStreamOutlivesRequestTimeouts(t *testing.T) {
	e, _ := startHTTP(t, 100*time.Millisecond, 100*time.Millisecond)
	req, _ := http.NewRequest("GET", e.ts.URL+"/api/v1/events?topics=control", nil)
	req.Header.Set("Authorization", "Bearer "+ownerTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	time.Sleep(500 * time.Millisecond)
	if code, out := e.rest("POST", "/api/v1/pause-all", map[string]any{"reason": "test"}); code != 200 {
		t.Fatalf("pause-all: %d %v", code, out)
	}
	timeout := time.After(5 * time.Second)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("event stream closed by request timeouts")
			}
			if strings.HasPrefix(l, "event: control.paused") {
				return
			}
		case <-timeout:
			t.Fatal("no event received")
		}
	}
}

type failingSpawner struct{}

func (failingSpawner) Name() string { return "hc" }

func (failingSpawner) Spawn(context.Context, spawner.Request) error {
	return errors.New("spawner hc: ssh: connect to host build-07.internal port 22: /home/ops/.ssh/id_ed25519 denied")
}

func TestSpawnFailureDetailsHiddenFromLeader(t *testing.T) {
	e := newEnvCfg(t, Config{Spawner: failingSpawner{}})
	lead := e.register("rex", "leader", "other")
	bid := e.secBrief(map[string]any{"title": "B"})
	sp := e.must(lead, "request_spawn", map[string]any{"brief_id": bid, "worker_kind": "claude-code", "model": "m", "n": 1})["spawns"].([]any)[0].(map[string]any)
	if sp["status"] != "failed" {
		t.Fatalf("spawn status = %v", sp["status"])
	}
	if msg, _ := sp["error"].(string); strings.Contains(msg, "build-07") || strings.Contains(msg, ".ssh") {
		t.Fatalf("spawner internals leaked to the leader: %q", msg)
	}
	_, out := e.rest("GET", "/api/v1/spawns", nil)
	if msg, _ := out["spawns"].([]any)[0].(map[string]any)["error"].(string); !strings.Contains(msg, "build-07") {
		t.Fatalf("owner lost spawn failure details: %q", msg)
	}
}
