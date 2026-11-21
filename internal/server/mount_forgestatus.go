package server

import (
	"os"

	"github.com/SigmaUno/subspace/internal/forgestatus"
	"github.com/SigmaUno/subspace/internal/prsync"
)

func init() {
	registerMount(mountForgeStatus)
}

func mountForgeStatus(d Deps) {
	path := os.Getenv("SUBSPACE_PRSYNC_CONFIG")
	if path == "" {
		return
	}
	cfg, err := prsync.LoadConfig(path, os.Getenv("SUBSPACE_PRSYNC_INTERVAL"))
	if err != nil {
		d.Log.Error("forge status checks disabled", "err", err)
		return
	}
	svc, err := forgestatus.New(forgestatus.Options{Config: cfg, PublicURL: d.Cfg.PublicURL, Log: d.Log})
	if err != nil {
		d.Log.Error("forge status checks disabled", "err", err)
		return
	}
	svc.Start(d.Ctx, d.Hub)
}
