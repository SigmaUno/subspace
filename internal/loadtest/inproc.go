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
	"time"

	"github.com/SigmaUno/subspace/internal/server"
)

// InProcess is a Subspace server started inside the load test process on a temp DB.
type InProcess struct {
	URL        string
	OwnerToken string
	stop       func()
}

// Stop shuts the server down and removes its temp directory.
func (p *InProcess) Stop() { p.stop() }

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
	token := "lt-owner-" + hex.EncodeToString(tb)
	addr, err := freeAddr()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	url := "http://" + addr
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	sctx, cancel := context.WithCancel(ctx)
	for _, k := range []string{"SUBSPACE_RATE_MCP", "SUBSPACE_RATE_AUTHFAIL"} {
		if _, ok := os.LookupEnv(k); !ok {
			_ = os.Setenv(k, "off")
		}
	}
	s, err := server.New(sctx, server.Config{Addr: addr, DBPath: filepath.Join(dir, "subspace.db"), OwnerToken: token, PublicURL: url, Log: log})
	if err != nil {
		cancel()
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("start in-process server: %w", err)
	}
	errc := make(chan error, 1)
	go func() { errc <- s.Run(sctx) }()
	stop := func() {
		cancel()
		select {
		case err := <-errc:
			if err != nil {
				log.Warn("in-process server stopped", "err", err)
			}
		case <-time.After(15 * time.Second):
		}
		_ = s.Close()
		_ = os.RemoveAll(dir)
	}
	if err := waitHealthy(ctx, &http.Client{Timeout: 2 * time.Second}, url, 10*time.Second, errc); err != nil {
		stop()
		return nil, err
	}
	return &InProcess{URL: url, OwnerToken: token, stop: stop}, nil
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
