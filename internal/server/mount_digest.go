package server

import (
	"time"

	"github.com/SigmaUno/subspace/internal/digest"
	"github.com/SigmaUno/subspace/internal/forecast"
	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/spendguard"
)

func init() { registerMount(mountDigest) }

func mountDigest(d Deps) {
	briefs := forecast.StoreSource{Store: d.Store, Estimator: estimator(d)}
	src := digest.StoreSource{
		Store:    d.Store,
		Feed:     d.Feed,
		Briefs:   briefs,
		Forecast: forecast.NewService(briefs, forecast.Options{}, time.Now),
		Alerts: func() []spendguard.Status {
			if g, ok := spendGuards.Load(d.Mux); ok {
				return g.(*spendguard.Guard).View().Windows
			}
			return nil
		},
	}
	d.API.Mount(d.Mux, "GET /api/v1/digest/preview", digest.Handler(src, time.Now))

	getenv := d.getenv()
	cfg, err := digest.ConfigFromEnv(getenv)
	if err != nil {
		d.Log.Error("digest disabled", "err", err)
		cfg = digest.Config{}
	}
	ncfg, err := notify.ConfigFromEnv(getenv)
	if err != nil {
		d.Log.Error("digest disabled", "err", err)
		ncfg = notify.Config{}
	}
	ncfg.Log = d.Log
	sink := notify.New(ncfg)
	sink.Run(d.Ctx)
	sched := digest.NewScheduler(digest.Options{Rules: cfg.Rules, Source: src, Sink: sink, KV: spendKV{d.Store}, Log: d.Log})
	d.reloadable("digest-notify", notify.ReloadKeys, sink)
	d.reloadable("digest", digest.ReloadKeys, sched)
	go sched.Run(d.Ctx)
}
