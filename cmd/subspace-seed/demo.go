package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
)

func runDemo(ctx context.Context, o options, stdout, stderr io.Writer) error {
	dir, err := os.MkdirTemp("", "subspace-demo-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	tb := make([]byte, 24)
	if _, err := rand.Read(tb); err != nil {
		return err
	}
	token := "demo-" + hex.EncodeToString(tb)
	for _, k := range []string{"SUBSPACE_RATE_MCP", "SUBSPACE_RATE_AUTHFAIL"} {
		if _, ok := os.LookupEnv(k); !ok {
			_ = os.Setenv(k, "off")
		}
	}
	url := "http://" + localAddr(o.addr)
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	srv, err := server.New(sctx, server.Config{
		Addr: o.addr, DBPath: filepath.Join(dir, "subspace.db"), OwnerToken: token, PublicURL: url,
		Lease: 8 * time.Hour, Log: log,
	})
	if err != nil {
		return err
	}
	defer func() { _ = srv.Close() }()
	ln, err := net.Listen("tcp", o.addr)
	if err != nil {
		return err
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(sctx, ln) }()
	if err := waitHealthy(sctx, url, errc); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "demo server on %s\nowner token: %s\n", url, token)
	done := make(chan error, 1)
	go func() { done <- seedAndSimulate(sctx, o, url, token, stdout) }()
	var runErr error
	stopped := false
	select {
	case runErr = <-done:
		if runErr == nil && !o.live {
			_, _ = fmt.Fprintln(stdout, "serving until interrupted")
			select {
			case <-ctx.Done():
			case runErr = <-errc:
				stopped = true
			}
		}
	case runErr = <-errc:
		stopped = true
	case <-ctx.Done():
	}
	cancel()
	if !stopped {
		select {
		case err := <-errc:
			_ = err
		case <-time.After(5 * time.Second):
		}
	}
	return runErr
}

func localAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return net.JoinHostPort(host, port)
}

func waitHealthy(ctx context.Context, url string, errc <-chan error) error {
	hc := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(url, "/")+"/healthz", nil)
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
			return errors.New("demo server did not become healthy")
		}
		select {
		case err := <-errc:
			return fmt.Errorf("server stopped: %w", err)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
