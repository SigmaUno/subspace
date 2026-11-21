package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/api"
	"github.com/SigmaUno/subspace/internal/audit"
	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/chat"
	"github.com/SigmaUno/subspace/internal/components"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/hillclimb"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/prompts"
	"github.com/SigmaUno/subspace/internal/spawner"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

type Config struct {
	Addr           string
	DBPath         string
	OwnerToken     string
	PublicURL      string
	CapacityFile   string
	PromptsDir     string
	Lease          time.Duration
	Spawner        spawner.Spawner
	BackupInterval time.Duration
	BackupKeep     int
	BackupDir      string
	FlushEvery     time.Duration
	SweepEvery     time.Duration
	BodyTimeout    time.Duration
	IdleTimeout    time.Duration
	Log            *slog.Logger
}

type Server struct {
	cfg         Config
	Store       store.Store
	Board       *board.Board
	Hub         events.Hub
	handler     http.Handler
	shutdown    chan struct{}
	shutdowning sync.Once
}

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
	if cfg.BodyTimeout <= 0 {
		cfg.BodyTimeout = 30 * time.Second
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 2 * time.Minute
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
	rest := &api.API{B: b, Hub: hub, OwnerToken: cfg.OwnerToken, Agents: cache, Log: cfg.Log}
	rest.Register(mux)

	feed := decisions.New(decisions.StoreSource{Store: st}, decisionActions{b}, decisions.Options{})
	rest.Mount(mux, "/api/v1/decisions", decisions.Handler(feed, cfg.Log))
	rest.Mount(mux, "/api/v1/decisions/", decisions.Handler(feed, cfg.Log))

	router := chat.NewRouter(chat.Config{Directory: chatDirectory{b}, Threads: chatThreads{b}})
	rest.Mount(mux, "/api/v1/messages", router.Handler())
	rest.Mount(mux, "/api/v1/conversations", router.Handler())
	rest.Mount(mux, "/api/v1/conversations/", router.Handler())

	comps := components.NewService(components.NewMemory(), componentSource{b, st}, nil)
	rest.Mount(mux, "GET /api/v1/briefs/{id}/architecture", components.Handler(comps))
	rest.Mount(mux, "PUT /api/v1/briefs/{id}/components", components.Handler(comps))

	lib := prompts.New(cfg.PromptsDir)
	rest.Mount(mux, "/api/v1/prompts", lib.Handler())
	rest.Mount(mux, "/api/v1/prompts/", lib.Handler())

	climb := hillclimb.NewHandler(hillclimb.NewMeasurer(hillclimb.NewMemStore(), noRunner{}))
	rest.Mount(mux, "/api/v1/hillclimb/", climb)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		p, _ := b.Paused()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":"ok","version":%q,"paused":%v}`+"\n", board.Version, p)
	})
	deps := Deps{Ctx: ctx, Board: b, Store: st, Hub: hub, Cfg: cfg, Log: cfg.Log, API: rest, Mux: mux, Auth: cache}
	for _, m := range extraMounts {
		m(deps)
	}
	var h http.Handler = mux
	for _, w := range wraps {
		h = w(h, deps)
	}
	s := &Server{cfg: cfg, Store: st, Board: b, Hub: hub, shutdown: make(chan struct{})}
	s.handler = bodyDeadline(s.endStreamsOnShutdown(h), cfg.BodyTimeout)
	return s, nil
}

func (s *Server) endStreamsOnShutdown(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/events" {
			h.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		go func() {
			select {
			case <-s.shutdown:
				cancel()
			case <-ctx.Done():
			}
		}()
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

type deadlineBody struct {
	io.ReadCloser
	rc *http.ResponseController
}

func (b *deadlineBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		_ = b.rc.SetReadDeadline(time.Time{})
	}
	return n, err
}

func bodyDeadline(h http.Handler, d time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength != 0 {
			rc := http.NewResponseController(w)
			if rc.SetReadDeadline(time.Now().Add(d)) == nil {
				r.Body = &deadlineBody{ReadCloser: r.Body, rc: rc}
			}
		}
		h.ServeHTTP(w, r)
	})
}

func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) Close() error { return s.Store.Close() }

func (s *Server) httpServer() *http.Server {
	return &http.Server{Addr: s.cfg.Addr, Handler: s.handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: s.cfg.IdleTimeout}
}

func (s *Server) Run(ctx context.Context) error {
	go s.Board.Run(ctx)
	if s.cfg.BackupInterval > 0 {
		go s.backupLoop(ctx)
	}
	srv := s.httpServer()
	srv.RegisterOnShutdown(func() { s.shutdowning.Do(func() { close(s.shutdown) }) })
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
	for i := 0; i < len(names)-keep; i++ {
		_ = os.Remove(filepath.Join(s.cfg.BackupDir, names[i]))
	}
}
