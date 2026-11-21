package server

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/SigmaUno/subspace/internal/backup"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/drill"
)

const (
	envDrillEvery       = "SUBSPACE_DRILL_EVERY"
	drillUnsupportedMsg = "restore drills need SQLite backups: the postgres backup scheduler writes JSON-lines dumps, not pg_dump files"
)

type backupLister func(context.Context) ([]backup.Info, error)

func (f backupLister) List(ctx context.Context) ([]backup.Info, error) { return f(ctx) }

func init() { registerMount(mountDrill) }

func mountDrill(d Deps) {
	if !drill.SupportsSQL(d.Ctx, d.Store) {
		d.Log.Error("restore drills disabled", "err", "store does not support SQL extensions")
		return
	}
	every := drill.DefaultEvery
	if raw := d.getenv()(envDrillEvery); raw != "" {
		v, err := time.ParseDuration(raw)
		if err != nil || v < 0 {
			d.Log.Error("restore drills disabled", "err", fmt.Sprintf("%s must be a non-negative duration such as 24h", envDrillEvery))
			return
		}
		every = v
	}
	opts := drill.Options{
		Store: d.Store, Backups: backupLister(d.Board.ListBackups), Every: every,
		Pub: d.Hub, Now: d.Cfg.Now, Log: d.Log,
	}
	if isPostgres(d.Cfg.DBPath) {
		opts.Unsupported = drillUnsupportedMsg
	} else {
		dir := d.Cfg.DataDir
		if dir == "" {
			dir = filepath.Dir(d.Cfg.DBPath)
		}
		opts.Dir = dir
	}
	svc, err := drill.New(opts)
	if err != nil {
		d.Log.Error("restore drills disabled", "err", err)
		return
	}
	h := drill.Handler(svc, d.Log)
	d.API.Mount(d.Mux, "GET /api/v1/drills", h)
	d.API.Mount(d.Mux, "POST /api/v1/drills", h)
	detach := decisions.Attach(d.Store, svc)
	go func() {
		<-d.Ctx.Done()
		detach()
	}()
	if opts.Unsupported == "" {
		d.Leader.Go(d.Ctx, svc.Run)
	}
}
