package server

import (
	"os"
	"path/filepath"

	"github.com/SigmaUno/subspace/internal/retention"
)

func init() {
	registerMount(func(d Deps) {
		pol, err := retention.PolicyFromEnv(os.Getenv)
		if err != nil {
			d.Log.Error("retention disabled", "err", err)
			return
		}
		dir := d.Cfg.DataDir
		if dir == "" && !isPostgres(d.Cfg.DBPath) {
			dir = filepath.Dir(d.Cfg.DBPath)
		}
		if dir != "" {
			dir = filepath.Join(dir, "retention")
		}
		if pol.Dir == "" && dir == "" {
			d.Log.Error("retention disabled", "err", "SUBSPACE_DATA_DIR or SUBSPACE_RETAIN_DIR is required with the postgres backend")
			return
		}
		svc, err := retention.New(retention.Options{Store: d.Store, Policy: pol, Dir: dir, Now: d.Cfg.Now, Log: d.Log})
		if err != nil {
			d.Log.Error("retention disabled", "err", err)
			return
		}
		h := retention.Handler(svc)
		d.API.Mount(d.Mux, "GET /api/v1/retention", h)
		d.API.Mount(d.Mux, "POST /api/v1/retention/run", h)
		d.API.Mount(d.Mux, "GET /api/v1/retention/verify", h)
		d.API.Mount(d.Mux, "POST /api/v1/retention/segments/{id}/restore", h)
		d.Leader.Go(d.Ctx, svc.Run)
	})
}
