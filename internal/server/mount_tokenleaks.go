package server

import (
	"context"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/leakguard"
	"github.com/SigmaUno/subspace/internal/tokens"
)

func init() {
	registerMount(func(d Deps) {
		o := leakguard.Options{Board: d.Board, Store: d.Store, Agents: d.Auth, Pub: d.Hub, Log: d.Log, Now: d.Cfg.Now}
		if ts, ok := d.Store.(tokens.Store); ok {
			o.Tokens = ts
		}
		g := leakguard.New(o)
		d.Board.SetLeakScanner(g.Scan)
		d.API.Mount(d.Mux, "POST /api/v1/token-leaks/{id}/reissue", leakguard.Handler(g))
		detach := decisions.Attach(d.Store, g)
		context.AfterFunc(d.Ctx, detach)
	})
}
