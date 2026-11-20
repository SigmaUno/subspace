// Package server wires store, board, MCP, REST and SSE into one http.Handler.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/api"
	"github.com/SigmaUno/subspace/internal/audit"
	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/spawner"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

// Config is the server configuration.
type Config struct {
	Addr           string
	DBPath         string
	OwnerToken     string
	PublicURL      string
	CapacityFile   string
	Lease          time.Duration
	Spawner        spawner.Spawner
	BackupInterval time.Duration // 0 = off
	BackupKeep     int
	BackupDir      string
	FlushEvery     time.Duration
	SweepEvery     time.Duration
	Log            *slog.Logger
}

// Server is a running Subspace instance.
type Server struct {
	cfg   Config
	Store store.Store
	Board *board.Board
	Hub   events.Hub
	mux   *http.ServeMux
}

// New opens the store and builds the handler. The owner token must be at least 24 chars.
func New(ctx context.Context, cfg Config) (*Server, error) {
	if len(cfg.OwnerToken) < 24 {
		return nil, errors.New("owner token must be at least 24 characters (SUBSPACE_OWNER_TOKEN or SUBSPACE_OWNER_TOKEN_FILE)")
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.DBPath == "" {
		cfg.DBPath = "/data/subspace.db"
	}
	if cfg.BackupDir == "" {
		cfg.BackupDir = filepath.Join(filepath.Dir(cfg.DBPath), "backups")
	}
	var capCfg capacity.Config
	if cfg.CapacityFile != "" {
		c, err := capacity.Load(cfg.CapacityFile)
		if err != nil {
			return nil, fmt.Errorf("capacity: %w", err)
		}
		capCfg = c
	}
	st, err := sqlite.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	hub := events.NewHub()
	var cache *auth.Cache
	b, err := board.New(ctx, st, board.Options{
		PublicURL: cfg.PublicURL, Lease: cfg.Lease, Capacity: capCfg, Spawner: cfg.Spawner, Hub: hub,
		Invalidate: func(id string) {
			if cache != nil {
				cache.Invalidate(id)
			}
		},
		Log: cfg.Log, BackupDir: cfg.BackupDir, FlushEvery: cfg.FlushEvery, SweepEvery: cfg.SweepEvery,
	})
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	cache = auth.NewCache(b.LoadByTokenHash, 30*time.Second)

	m := mcp.NewServer(cache, mcp.NewRegistry(b))
	m.Guard = audit.NewLoopGuard(audit.DefaultMaxRepeats, audit.DefaultWindow)
	m.Hooks = b
	m.OwnerToken = cfg.OwnerToken

	mux := http.NewServeMux()
	mux.Handle("/mcp", m)
	(&api.API{B: b, Hub: hub, OwnerToken: cfg.OwnerToken, Agents: cache, Log: cfg.Log}).Register(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		p, _ := b.Paused()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":"ok","version":%q,"paused":%v}`+"\n", board.Version, p)
	})
	return &Server{cfg: cfg, Store: st, Board: b, Hub: hub, mux: mux}, nil
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler { return s.mux }

// Close closes the store.
func (s *Server) Close() error { return s.Store.Close() }

// Run serves until ctx is cancelled, running the board background loops and backups.
func (s *Server) Run(ctx context.Context) error {
	go s.Board.Run(ctx)
	if s.cfg.BackupInterval > 0 {
		go s.backupLoop(ctx)
	}
	srv := &http.Server{Addr: s.cfg.Addr, Handler: s.mux, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		return nil
	case err := <-errc:
		return err
	}
}

func (s *Server) backupLoop(ctx context.Context) {
	t := time.NewTicker(s.cfg.BackupInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if p, n, err := s.Board.Backup(ctx); err != nil {
				s.cfg.Log.Error("backup", "err", err)
			} else {
				s.cfg.Log.Info("backup written", "path", p, "bytes", n)
				s.pruneBackups()
			}
		}
	}
}

func (s *Server) pruneBackups() {
	keep := s.cfg.BackupKeep
	if keep <= 0 {
		return
	}
	es, err := os.ReadDir(s.cfg.BackupDir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range es {
		if strings.HasPrefix(e.Name(), "subspace-") && strings.HasSuffix(e.Name(), ".db") {
			names = append(names, e.Name())
		}
	}
	for i := 0; i < len(names)-keep; i++ { // ReadDir is sorted by name = by timestamp
		_ = os.Remove(filepath.Join(s.cfg.BackupDir, names[i]))
	}
}
