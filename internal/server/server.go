package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/api"
	"github.com/SigmaUno/subspace/internal/audit"
	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/backup"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/breaker"
	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/chat"
	"github.com/SigmaUno/subspace/internal/components"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/ha"
	"github.com/SigmaUno/subspace/internal/hillclimb"
	"github.com/SigmaUno/subspace/internal/logpipe"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/prompts"
	"github.com/SigmaUno/subspace/internal/reload"
	"github.com/SigmaUno/subspace/internal/spawner"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/trace"
)

const traceShutdown = 5 * time.Second

type Config struct {
	Addr            string
	DBPath          string
	DataDir         string
	OwnerToken      string
	PublicURL       string
	CapacityFile    string
	PromptsDir      string
	Lease           time.Duration
	PoisonExpiries  int
	Spawner         spawner.Spawner
	BackupInterval  time.Duration
	BackupKeep      int
	BackupDir       string
	FlushEvery      time.Duration
	SweepEvery      time.Duration
	BodyTimeout     time.Duration
	Now             func() time.Time
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	ConfigFile      string
	Log             *slog.Logger
	LogPipe         *logpipe.Pipeline
	InstanceID      string
	LeaderInterval  time.Duration
	LeaderTTL       time.Duration
	DrainDelay      time.Duration
	SchemaReadOnly  bool
}

type Server struct {
	cfg     Config
	Store   store.Store
	Board   *board.Board
	Hub     events.Hub
	api     *api.API
	handler http.Handler
	backups *backup.Scheduler
	reload  *reload.Manager
	reg     *mcp.Registry
	Leader  *ha.Elector
	cluster *cluster
	closers []func()
	tracer  *trace.Tracer

	drain     chan struct{}
	drainOnce sync.Once
}

func New(ctx context.Context, cfg Config) (*Server, error) {
	if len(cfg.OwnerToken) < 24 {
		return nil, errors.New("owner token must be at least 24 characters (SUBSPACE_OWNER_TOKEN or SUBSPACE_OWNER_TOKEN_FILE)")
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.LogPipe == nil {
		cfg.LogPipe = logpipe.Wrap(cfg.Log.Handler(), logpipe.DefaultCapacity)
		cfg.Log = cfg.LogPipe.Logger()
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
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = 20 * time.Second
	}
	if cfg.FlushEvery <= 0 {
		cfg.FlushEvery = 2 * time.Second
	}
	if cfg.SweepEvery <= 0 {
		cfg.SweepEvery = 5 * time.Second
	}
	if cfg.InstanceID == "" {
		cfg.InstanceID = instanceID()
	}
	if cfg.BackupDir == "" && !isPostgres(cfg.DBPath) {
		cfg.BackupDir = filepath.Join(filepath.Dir(cfg.DBPath), "backups")
	}
	rl := reload.New(cfg.ConfigFile, func(k string) string {
		if v := os.Getenv(k); v != "" || k != board.EnvCapacityFile {
			return v
		}
		return cfg.CapacityFile
	})
	env, err := rl.Load()
	if err != nil {
		return nil, err
	}
	if isPostgres(cfg.DBPath) {
		if err := store.CheckSQLiteOnly(env); err != nil {
			return nil, err
		}
	}
	off, err := openOffsite(env, cfg.BackupDir)
	if err != nil {
		return nil, err
	}
	tcfg, err := trace.ConfigFromEnv(env)
	if err != nil {
		return nil, err
	}
	tracing := tcfg.Build(cfg.Log, board.Version, cfg.InstanceID)
	ctx = trace.WithTracer(ctx, tracing.Tracer)
	var capCfg capacity.Config
	if path := env(board.EnvCapacityFile); path != "" {
		c, err := capacity.Load(path)
		if err != nil {
			return nil, fmt.Errorf("capacity: %w", err)
		}
		capCfg = c
	}
	st, err := openStore(cfg.DBPath, store.OpenOptions{ReadOnlyNewer: cfg.SchemaReadOnly})
	if err != nil {
		return nil, err
	}
	local := events.NewHub()
	var (
		b     *board.Board
		cache *auth.Cache
	)
	cl, err := newCluster(cfg, local, func(_ []string, typ string, data any) {
		switch typ {
		case "agent.updated":
			if a, ok := data.(*model.Agent); ok {
				cache.Invalidate(a.ID)
			}
		case "control.paused", "control.resumed", "capacity.changed":
			if err := b.Reload(ctx); err != nil && ctx.Err() == nil {
				cfg.Log.Error("reload shared state", "err", err)
			}
		}
	})
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	hub := cl.hub(local)
	var backups *backup.Scheduler
	if cfg.BackupDir != "" {
		backups, err = backup.New(backup.Config{
			Dir: cfg.BackupDir, Interval: cfg.BackupInterval, Keep: cfg.BackupKeep,
			Run: st.Backup, Verify: backupVerifier(cfg.DBPath), Logger: cfg.Log, Now: cfg.Now,
		})
		if err != nil {
			cl.close()
			_ = st.Close()
			return nil, err
		}
	}
	b, err = board.New(ctx, st, board.Options{
		PublicURL: cfg.PublicURL, Lease: cfg.Lease, PoisonExpiries: cfg.PoisonExpiries, Capacity: capCfg, Spawner: cfg.Spawner, Hub: hub, Now: cfg.Now,
		Invalidate: func(id string) {
			if cache != nil {
				cache.Invalidate(id)
			}
		},
		Log: cfg.Log, Backups: backups, FlushEvery: cfg.FlushEvery, SweepEvery: cfg.SweepEvery, Shared: isPostgres(cfg.DBPath),
	})
	if err != nil {
		cl.close()
		_ = st.Close()
		return nil, err
	}
	cache = auth.NewCacheClock(b.LoadByTokenHash, 30*time.Second, cfg.Now)
	cl.start(ctx)

	reg := mcp.NewRegistry(b)
	climber := newHillclimb(st, b, reg, hub, cfg.Log)
	m := mcp.NewServer(cache, reg)
	m.Guard = audit.NewLoopGuard(audit.DefaultMaxRepeats, audit.DefaultWindow)
	m.Hooks = b
	m.OwnerToken = cfg.OwnerToken
	if isPostgres(cfg.DBPath) {
		m.Sessions = sqlSessions{st}
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", m)
	rest := &api.API{B: b, Hub: hub, OwnerToken: cfg.OwnerToken, Agents: cache, Log: cfg.Log}
	rest.Register(mux)

	feed := decisions.New(decisions.StoreSource{Store: st, Overlay: b.OverlayAgent}, decisionActions{b}, decisions.Options{Now: cfg.Now})
	rest.Mount(mux, "/api/v1/decisions", decisions.Handler(feed, cfg.Log))
	rest.Mount(mux, "/api/v1/decisions/", decisions.Handler(feed, cfg.Log))

	router := chat.NewRouter(chat.Config{Directory: chatDirectory{b}, Threads: chatThreads{b}})
	rest.Mount(mux, "/api/v1/messages", router.Handler())
	rest.Mount(mux, "/api/v1/conversations", router.Handler())
	rest.Mount(mux, "/api/v1/conversations/", router.Handler())

	compStore := components.NewKV(st)
	comps := components.NewService(compStore, componentSource{b, st}, nil)
	comps.Changed = b.AuditComponents
	b.SetComponentPaths(func(briefID, id string) []string { return comps.Paths(ctx, briefID, id) })
	rest.Mount(mux, "GET /api/v1/briefs/{id}/architecture", components.Handler(comps))
	rest.Mount(mux, "PUT /api/v1/briefs/{id}/components", components.Handler(comps))

	lib := prompts.New(cfg.PromptsDir)
	mcp.RegisterTaskTemplates(reg, b, lib)
	mcp.RegisterPrompts(reg, lib)
	m.Surface = &mcp.Surface{B: b, Lib: lib}
	rest.Mount(mux, "/api/v1/prompts", lib.Handler())
	rest.Mount(mux, "/api/v1/prompts/", lib.Handler())

	rest.Mount(mux, "/api/v1/hillclimb/", hillclimb.NewHandler(climber))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		p, _ := b.Paused()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":"ok","version":%q,"paused":%v}`+"\n", board.Version, p)
	})
	drain := make(chan struct{})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !ready(st, drain) {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	brkCfg, err := breaker.ConfigFromEnv(os.Getenv)
	if err != nil {
		cl.close()
		_ = st.Close()
		return nil, err
	}
	var closers []func()
	deps := Deps{Ctx: ctx, OnClose: func(f func()) { closers = append(closers, f) }, Board: b, Store: st, Hub: hub, LocalHub: local, Leader: cl.elector, KeyLock: cl.keyLock(), Cfg: cfg, Log: cfg.Log, API: rest, Mux: mux, Auth: cache, Feed: feed, Backups: backups, Offsite: off,
		MCP: m, Registry: reg, Breakers: breaker.New(brkCfg, nil, breakerLog(cfg.Log)), Components: compStore, Comps: comps,
		Env: env, Reload: rl, Trust: newProxyTrust(cfg.Log, env)}
	rl.Register("capacity", board.CapacityReloadKeys, b)
	rl.Register("trusted-proxies", []string{envTrustedProxies}, deps.Trust)
	level := &reload.LogLevel{Pipe: cfg.LogPipe}
	if v := env(reload.EnvLogLevel); v != "" {
		if lv, err := logpipe.ParseLevel(v); err == nil {
			cfg.LogPipe.SetLevel(lv)
		}
	}
	level.Applied = env(reload.EnvLogLevel)
	rl.Register("log-level", []string{reload.EnvLogLevel}, level)
	for _, m := range extraMounts {
		m(deps)
	}
	var h http.Handler = mux
	for _, w := range wraps {
		h = w(h, deps)
	}
	h = newAllowlisted(h, deps)
	h = tracing.Tracer.Handler(h, func(r *http.Request) string { _, p := mux.Handler(r); return p })
	if err := rl.Check(); err != nil {
		cl.close()
		_ = st.Close()
		return nil, err
	}
	if tracing.Exporter != nil {
		tracing.Start()
		closers = append([]func(){func() {
			cctx, cancel := context.WithTimeout(context.Background(), traceShutdown)
			defer cancel()
			_ = tracing.Close(cctx)
		}}, closers...)
	}
	s := &Server{cfg: cfg, Store: st, Board: b, Hub: hub, api: rest, backups: backups, reload: rl, reg: reg, Leader: cl.elector, cluster: cl, closers: closers, drain: drain, tracer: tracing.Tracer}
	s.handler = s.withDrain(bodyDeadline(h, cfg.BodyTimeout))
	context.AfterFunc(ctx, s.beginDrain)
	return s, nil
}

func (s *Server) Reload(ctx context.Context, note string) (reload.Result, error) {
	return reloadConfig(ctx, s.reload, s.Board, s.cfg.Log, note)
}

func ready(st store.Store, drain <-chan struct{}) bool {
	if ro, ok := st.(interface{ ReadOnly() bool }); ok && ro.ReadOnly() {
		return false
	}
	var closed <-chan struct{}
	if d, ok := st.(interface{ Done() <-chan struct{} }); ok {
		closed = d.Done()
	}
	select {
	case <-drain:
		return false
	case <-closed:
		return false
	default:
		return true
	}
}

func (s *Server) beginDrain() { s.drainOnce.Do(func() { close(s.drain) }) }

func (s *Server) withDrain(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(events.WithShutdown(r.Context(), s.drain)))
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

func (s *Server) ToolNames() map[string][]string {
	out := make(map[string][]string, len(mcp.AllRoles))
	for _, role := range mcp.AllRoles {
		out[role] = s.reg.Names(role)
	}
	return out
}

func (s *Server) Close() error {
	s.beginDrain()
	for _, f := range slices.Backward(s.closers) {
		f()
	}
	s.resign(context.Background())
	s.cluster.close()
	return s.Store.Close()
}

func (s *Server) httpServer() *http.Server {
	return &http.Server{Handler: s.handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: s.cfg.IdleTimeout}
}

func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	bgCtx, stopBg := context.WithCancel(trace.WithTracer(ctx, s.tracer))
	defer stopBg()
	flushCtx, stopFlush := context.WithCancel(trace.WithTracer(context.WithoutCancel(ctx), s.tracer))
	defer stopFlush()

	var bg, flusher sync.WaitGroup
	bg.Go(func() { s.Leader.Lead(bgCtx, s.sweepLoop) })
	if s.backups != nil {
		bg.Go(func() { s.Leader.Lead(bgCtx, s.backups.Run) })
	}
	flusher.Go(func() { s.flushLoop(flushCtx) })

	srv := s.httpServer()
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-errc:
	}
	s.cfg.Log.Info("shutting down", "timeout", s.cfg.ShutdownTimeout)
	drained := time.After(s.cfg.DrainDelay)
	s.beginDrain()
	stopBg()
	bg.Wait()
	s.resign(ctx)
	if serveErr == nil {
		<-drained
	}

	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		s.cfg.Log.Warn("shutdown timed out, closing connections", "err", err)
		_ = srv.Close()
	}
	if c, ok := s.Hub.(interface{ Close() }); ok {
		c.Close()
	}

	stopFlush()
	flusher.Wait()
	return serveErr
}

func (s *Server) resign(ctx context.Context) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.ShutdownTimeout)
	defer cancel()
	if err := s.Leader.Resign(rctx); err != nil {
		s.cfg.Log.Warn("leader hand-off timed out", "err", err)
	}
}

func (s *Server) RunBackground(ctx context.Context) {
	ctx = trace.WithTracer(ctx, s.tracer)
	var wg sync.WaitGroup
	wg.Go(func() { s.Leader.Lead(ctx, s.sweepLoop) })
	wg.Go(func() { s.flushLoop(ctx) })
	wg.Wait()
}

func (s *Server) sweepLoop(ctx context.Context) {
	t := time.NewTicker(s.cfg.SweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tctx, span := trace.StartRoot(ctx, "leader.sweep", trace.KindInternal, trace.String("subspace.instance", s.cfg.InstanceID))
			if err := s.Board.Sweep(tctx); err != nil && ctx.Err() == nil {
				span.Fail("sweep failed")
				s.cfg.Log.Error("sweep", "err", err)
			}
			span.End()
		}
	}
}

func (s *Server) flushLoop(ctx context.Context) {
	t := time.NewTicker(s.cfg.FlushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.Board.Flush(context.Background())
			return
		case <-t.C:
			tctx, span := trace.StartRoot(ctx, "board.flush", trace.KindInternal)
			s.Board.Flush(context.WithoutCancel(tctx))
			span.End()
		}
	}
}
