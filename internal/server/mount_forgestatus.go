package server

import (
	"github.com/SigmaUno/subspace/internal/forgestatus"
	"github.com/SigmaUno/subspace/internal/prsync"
)

func init() {
	registerMount(mountForgeStatus)
}

func mountForgeStatus(d Deps) {
	cfg, ok, err := prsync.Resolve(d.Ctx, repoSource(d), d.Log)
	if err != nil {
		d.Log.Error("forge status checks disabled", "err", err)
		return
	}
	if !ok {
		return
	}
	svc, err := forgestatus.New(forgestatus.Options{Config: cfg, PublicURL: d.Cfg.PublicURL, Log: d.Log})
	if err != nil {
		d.Log.Error("forge status checks disabled", "err", err)
		return
	}
	svc.Start(d.Ctx, d.Leader.Gate(d.Hub))
}
