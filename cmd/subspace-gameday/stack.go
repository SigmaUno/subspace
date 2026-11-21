package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/SigmaUno/subspace/internal/server"
)

type Clock struct{ off atomic.Int64 }

func (c *Clock) Now() time.Time { return time.Now().UTC().Add(time.Duration(c.off.Load())) }

func (c *Clock) Jump(d time.Duration) { c.off.Add(int64(d)) }

type Stack struct {
	URL   string
	Token string
	Clock *Clock
	Cfg   server.Config

	mu      sync.Mutex
	srv     *server.Server
	cancel  context.CancelFunc
	done    chan error
	cleanup []func()
}

func (s *Stack) Local() bool { return s.Cfg.DBPath != "" }

func (s *Stack) Client() *Client { return NewClient(s.URL, s.Token, 10*time.Second) }

func (s *Stack) Server() *server.Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srv
}

func (s *Stack) Up() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srv != nil
}

func (s *Stack) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv != nil {
		return errors.New("stack already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv, err := server.New(ctx, s.Cfg)
	if err != nil {
		cancel()
		return err
	}
	ln, err := listenRetry(s.Cfg.Addr)
	if err != nil {
		cancel()
		return errors.Join(err, srv.Close())
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	s.srv, s.cancel, s.done = srv, cancel, done
	return s.waitHealthy()
}

func listenRetry(addr string) (net.Listener, error) {
	var err error
	for range 50 {
		var ln net.Listener
		if ln, err = net.Listen("tcp", addr); err == nil {
			return ln, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil, err
}

func (s *Stack) waitHealthy() error {
	hc := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := hc.Get(s.URL + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case err := <-s.done:
			return fmt.Errorf("server stopped: %w", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	return errors.New("server did not become healthy")
}

func (s *Stack) Kill() error {
	s.mu.Lock()
	srv, cancel, done := s.srv, s.cancel, s.done
	s.srv, s.cancel, s.done = nil, nil, nil
	s.mu.Unlock()
	if srv == nil {
		return nil
	}
	cancel()
	var err error
	select {
	case err = <-done:
	case <-time.After(30 * time.Second):
		err = errors.New("server did not stop")
	}
	return errors.Join(err, srv.Close())
}

func (s *Stack) Close() {
	_ = s.Kill()
	for _, f := range s.cleanup {
		f()
	}
}

func freeAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := l.Addr().String()
	return addr, l.Close()
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

type Mod func(*server.Config)

func (o Options) NewStack(mods ...Mod) (*Stack, error) {
	if o.remote() {
		return &Stack{URL: strings.TrimRight(o.URL, "/"), Token: o.Token}, nil
	}
	for _, k := range []string{"SUBSPACE_RATE_MCP", "SUBSPACE_RATE_AUTHFAIL"} {
		if _, ok := os.LookupEnv(k); !ok {
			if err := os.Setenv(k, "off"); err != nil {
				return nil, err
			}
		}
	}
	dir, err := os.MkdirTemp("", "subspace-gameday-")
	if err != nil {
		return nil, err
	}
	addr, err := freeAddr()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	clk := &Clock{}
	s := &Stack{URL: "http://" + addr, Token: "gd-owner-" + randHex(16), Clock: clk}
	s.cleanup = append(s.cleanup, func() { _ = os.RemoveAll(dir) })
	s.Cfg = server.Config{
		Addr: addr, DBPath: filepath.Join(dir, "subspace.db"), DataDir: dir, OwnerToken: s.Token, PublicURL: s.URL,
		Lease: o.Lease, FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond,
		ShutdownTimeout: time.Second, Now: clk.Now, Log: quietLog(),
	}
	for _, m := range mods {
		m(&s.Cfg)
	}
	if err := s.Start(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (o Options) NewCluster(n int, mods ...Mod) ([]*Stack, func(), error) {
	dsn, drop, err := pgSchema(o.PGDSN)
	if err != nil {
		return nil, nil, err
	}
	token := "gd-owner-" + randHex(16)
	var out []*Stack
	closeAll := func() {
		for _, s := range out {
			s.Close()
		}
		drop()
	}
	for i := range n {
		id := fmt.Sprintf("gd-node-%d", i)
		s, err := o.NewStack(append([]Mod{func(c *server.Config) {
			c.DBPath, c.OwnerToken, c.InstanceID = dsn, token, id
			c.BackupDir = filepath.Join(c.DataDir, "backups")
			c.LeaderInterval, c.LeaderTTL = 100*time.Millisecond, 500*time.Millisecond
		}}, mods...)...)
		if err != nil {
			closeAll()
			return nil, nil, err
		}
		s.Token = token
		out = append(out, s)
	}
	return out, closeAll, nil
}

func pgSchema(base string) (string, func(), error) {
	schema := "gameday_" + randHex(6)
	admin, err := sql.Open("pgx", base)
	if err != nil {
		return "", nil, err
	}
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		_ = admin.Close()
		return "", nil, err
	}
	u, err := url.Parse(base)
	if err != nil {
		_ = admin.Close()
		return "", nil, err
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String(), func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = admin.Close()
	}, nil
}
