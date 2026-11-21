package server

import (
	"context"
	"fmt"

	"github.com/SigmaUno/subspace/internal/forgestatus"
)

func init() {
	registerMount(mountForgeStatus)
}

func mountForgeStatus(d Deps) {
	d.Leader.Go(d.Ctx, func(ctx context.Context) {
		superviseRepos(ctx, d, func(ctx context.Context) (func(context.Context), error) {
			cfg, ok, err := resolveRepos(ctx, d, "forge status checks")
			if err != nil || !ok {
				return nil, err
			}
			svc, err := forgestatus.New(forgestatus.Options{Config: cfg, PublicURL: d.Cfg.PublicURL, Log: d.Log})
			if err != nil {
				return nil, fmt.Errorf("forge status checks disabled: %w", err)
			}
			return func(ctx context.Context) {
				svc.Start(ctx, d.Leader.Gate(d.Hub))
				<-ctx.Done()
			}, nil
		})
	})
}
