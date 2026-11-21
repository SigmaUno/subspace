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
		eng := alerts.New(alerts.Options{
			Source: &alerts.StoreSource{
				Store: d.Store, DBPath: d.Cfg.DBPath, BackupsEnabled: d.Cfg.BackupInterval > 0,
				Backups: d.Board.ListBackups, Paused: func() bool { p, _ := d.Board.Paused(); return p },
				SSE: mon, Started: d.Cfg.Now(),
			},
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
