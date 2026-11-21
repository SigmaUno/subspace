package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/store"
)

type proc struct {
	cfg    server.Config
	srv    *server.Server
	hs     *http.Server
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newProc(dir string, lease time.Duration, log *slog.Logger) (*proc, error) {
	for _, k := range []string{"SUBSPACE_RATE_MCP", "SUBSPACE_RATE_AUTHFAIL"} {
		if _, ok := os.LookupEnv(k); !ok {
			if err := os.Setenv(k, "off"); err != nil {
				return nil, err
			}
		}
	}
	tb := make([]byte, 24)
	if _, err := rand.Read(tb); err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		return nil, err
	}
	return &proc{cfg: server.Config{
		Addr: addr, DBPath: filepath.Join(dir, "subspace.db"), OwnerToken: "soak-owner-" + hex.EncodeToString(tb),
		PublicURL: "http://" + addr, Lease: lease, BackupDir: filepath.Join(dir, "backups"), BackupKeep: 3, Log: log,
	}}, nil
}

func (p *proc) url() string { return p.cfg.PublicURL }

func (p *proc) start() error {
	ctx, cancel := context.WithCancel(context.Background())
	s, err := server.New(ctx, p.cfg)
	if err != nil {
		cancel()
		return err
	}
	ln, err := net.Listen("tcp", p.cfg.Addr)
	if err != nil {
		cancel()
		return errors.Join(err, s.Close())
	}
	hs := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute}
	p.srv, p.hs, p.cancel = s, hs, cancel
	p.wg.Add(2)
	go func() {
		defer p.wg.Done()
		s.Board.Run(ctx)
	}()
	go func() {
		defer p.wg.Done()
		_ = hs.Serve(ln)
	}()
	return nil
}

func (p *proc) stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.hs.Shutdown(ctx); err != nil {
		_ = p.hs.Close()
	}
	p.cancel()
	p.wg.Wait()

	return p.srv.Close()
}

func (p *proc) writerQueue() int {
	return chanLen(p.srv.Store)
}

func chanLen(st store.Store) int {
	v := reflect.ValueOf(st)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		return -1
	}
	f := v.Elem().FieldByName("reqs")
	if !f.IsValid() || f.Kind() != reflect.Chan {
		return -1
	}
	return f.Len()
}

func (p *proc) walBytes() int64 {
	fi, err := os.Stat(p.cfg.DBPath + "-wal")
	if err != nil {
		return 0
	}
	return fi.Size()
}

func openFDs() int {
	es, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(es) - 1
}
