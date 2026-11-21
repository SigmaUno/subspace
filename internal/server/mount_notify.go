package server

import (
	"context"
	"os"

	"github.com/SigmaUno/subspace/internal/notify"
)

func init() {
	registerMount(func(d Deps) {
		cfg, err := notify.ConfigFromEnv(os.Getenv)
		if err != nil {
			d.Log.Error("notify disabled", "err", err)
			cfg = notify.Config{}
		}
		cfg.Log = d.Log
		svc := notify.New(cfg)
		svc.Start(context.Background(), d.Hub)
		d.API.Mount(d.Mux, "/api/v1/notify/", svc.Handler())
	})
}
