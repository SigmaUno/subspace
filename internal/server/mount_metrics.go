package server

import (
	"net/http"

	"github.com/SigmaUno/subspace/internal/metrics"
)

func init() {
	wraps = append(wraps, func(next http.Handler, d Deps) http.Handler {
		set := metrics.New()
		set.Collect(metrics.Source{Store: d.Store, Hub: d.Hub, BackupDir: d.Cfg.BackupDir, Log: d.Log})
		d.API.Mount(d.Mux, "GET /api/v1/metrics", set.Registry.Handler())
		return set.Middleware(next, metrics.Options{Log: d.Log, Role: func(token string) string {
			if d.Auth == nil {
				return ""
			}
			if a, err := d.Auth.LookupToken(token); err == nil && a != nil {
				return a.Role
			}
			return ""
		}})
	})
}
