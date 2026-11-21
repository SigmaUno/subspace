package server

import (
	"net/http"
	"os"
	"time"

	"github.com/SigmaUno/subspace/internal/alerts"
	"github.com/SigmaUno/subspace/internal/decisions"
)

func init() {
	wraps = append(wraps, func(next http.Handler, d Deps) http.Handler {
		th := alerts.FromEnv(os.Getenv, d.Log)
		mon := &alerts.SSEMonitor{}
		eng := alerts.New(alerts.Options{
			Source: &alerts.StoreSource{
				Store: d.Store, DBPath: d.Cfg.DBPath, BackupsEnabled: d.Cfg.BackupInterval > 0,
				Backups: d.Board.ListBackups, Paused: func() bool { p, _ := d.Board.Paused(); return p },
				SSE: mon, Started: d.Cfg.Now(),
			},
			Thresholds: th, Publisher: d.Hub, Now: d.Cfg.Now, Log: d.Log,
		})
		d.API.Mount(d.Mux, "GET /api/v1/alerts", alerts.Handler(eng))
		detach := decisions.Attach(d.Store, eng)
		go func() {
			<-d.Ctx.Done()
			detach()
		}()
		if th.Interval > 0 {
			tk := time.NewTicker(th.Interval)
			go func() {
				defer tk.Stop()
				eng.Run(d.Ctx, tk.C)
			}()
		}
		return mon.Middleware(next)
	})
}
