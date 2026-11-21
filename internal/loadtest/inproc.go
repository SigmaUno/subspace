package loadtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
)

// InProcess is a Subspace server started inside the load test process on a temp DB.
type InProcess struct {
	URL        string
	OwnerToken string

	ctx  context.Context
	dir  string
	addr string
	log  *slog.Logger
	mu   sync.Mutex
	cur  *instance
}

type instance struct {
	srv    *server.Server
	cancel context.CancelFunc
	errc   chan error
}

// Stop shuts the server down and removes its temp directory.
func (p *InProcess) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shutdown()
	_ = os.RemoveAll(p.dir)
}

func (p *InProcess) Restart() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shutdown()
	return p.start()
}

func (p *InProcess) shutdown() {
	i := p.cur
	if i == nil {
		return
	}
	p.cur = nil
	i.cancel()
	select {
	case err := <-i.errc:
		if err != nil {
			p.log.Warn("in-process server stopped", "err", err)
		}
	case <-time.After(15 * time.Second):
	}
	_ = i.srv.Close()
}

func (p *InProcess) start() error {
	sctx, cancel := context.WithCancel(p.ctx)
	s, err := server.New(sctx, server.Config{Addr: p.addr, DBPath: filepath.Join(p.dir, "subspace.db"), OwnerToken: p.OwnerToken, PublicURL: p.URL, Log: p.log})
	if err != nil {
		cancel()
		return fmt.Errorf("start in-process server: %w", err)
	}
	i := &instance{srv: s, cancel: cancel, errc: make(chan error, 1)}
	go func() { i.errc <- s.Run(sctx) }()
	p.cur = i
	if err := waitHealthy(p.ctx, &http.Client{Timeout: 2 * time.Second}, p.URL, 10*time.Second, i.errc); err != nil {
		p.shutdown()
		return err
	}
	return nil
}

// StartInProcess starts a server on 127.0.0.1 with a fresh SQLite DB in a temp directory and waits for /healthz.
func StartInProcess(ctx context.Context, log *slog.Logger) (*InProcess, error) {
	dir, err := os.MkdirTemp("", "subspace-loadtest-")
	if err != nil {
		return nil, err
	}
	tb := make([]byte, 24)
	if _, err := rand.Read(tb); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	addr, err := freeAddr()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	for _, k := range []string{"SUBSPACE_RATE_MCP", "SUBSPACE_RATE_AUTHFAIL"} {
		if _, ok := os.LookupEnv(k); !ok {
			_ = os.Setenv(k, "off")
		}
	}
	p := &InProcess{URL: "http://" + addr, OwnerToken: "lt-owner-" + hex.EncodeToString(tb), ctx: ctx, dir: dir, addr: addr, log: log}
	if err := p.start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return p, nil
}

func freeAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := l.Addr().String()
	return addr, l.Close()
}

// waitHealthy polls GET /healthz until it returns 200, the timeout passes, or errc reports a server exit.
func waitHealthy(ctx context.Context, hc *http.Client, base string, timeout time.Duration, errc <-chan error) error {
	deadline := time.Now().Add(timeout)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
		if err != nil {
			return err
		}
		if resp, err := hc.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return errors.New("server at " + base + " did not become healthy")
		}
		select {
		case err := <-errc:
			if err == nil {
				err = errors.New("exited")
			}
			return fmt.Errorf("server stopped: %w", err)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
