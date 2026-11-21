package server

import (
	"context"
	"net/http"
	"time"

	"github.com/SigmaUno/subspace/internal/alerts"
	"github.com/SigmaUno/subspace/internal/decisions"
)

func init() {
	wraps = append(wraps, func(next http.Handler, d Deps) http.Handler {
		th := alerts.FromEnv(d.getenv(), d.Log)
		mon := &alerts.SSEMonitor{}
		src := &alerts.StoreSource{
			Store: d.Store, DBPath: d.Cfg.DBPath, DataDir: d.Cfg.DataDir, BackupsEnabled: d.Cfg.BackupInterval > 0, Offsite: d.Offsite != nil,
			Backups: d.Board.ListBackups, Paused: func() bool { p, _ := d.Board.Paused(); return p },
			SSE: mon, Started: d.Cfg.Now(),
		}
		if sz, ok := d.Store.(interface {
			DatabaseSize(context.Context) (int64, error)
		}); ok {
			src.DBSize = sz.DatabaseSize
		}
		eng := alerts.New(alerts.Options{
			Source:     src,
			Thresholds: th, Publisher: d.Hub, Now: d.Cfg.Now, Log: d.Log,
		})
		d.reloadable("alerts", alerts.ReloadKeys, eng)
		d.API.Mount(d.Mux, "GET /api/v1/alerts", alerts.Handler(eng))
		detach := decisions.Attach(d.Store, eng)
		go func() {
			<-d.Ctx.Done()
			detach()
		}()
		if th.Interval > 0 {
			d.Leader.Go(d.Ctx, func(ctx context.Context) {
				tk := time.NewTicker(th.Interval)
				defer tk.Stop()
				eng.Run(ctx, tk.C)
			})
		}
		return mon.Middleware(next)
	})
}
