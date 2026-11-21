package server

import (
	"context"
	"os"
	"time"

	"github.com/SigmaUno/subspace/internal/ha"

	"github.com/SigmaUno/subspace/internal/webhookout"
)

const (
	envWebhooksOutAllow = "SUBSPACE_WEBHOOKS_OUT_ALLOW_CIDRS"
	webhooksOutRefresh  = time.Second
)

func init() { registerMount(mountWebhooksOut) }

func mountWebhooksOut(d Deps) {
	allow, err := webhookout.ParsePrefixes(os.Getenv(envWebhooksOutAllow))
	if err != nil {
		d.Log.Error("outbound webhook allowlist ignored", "err", err)
		allow = nil
	}
	svc := webhookout.New(webhookout.Options{
		Store: leaderDue{webhookout.NewSQLStore(d.Store), d.Leader},
		Hub:   d.Leader.Gate(d.Hub),
		Now:   d.Cfg.Now,
		Guard: webhookout.NewGuard(allow),
		Log:   d.Log,
	})
	h := svc.Handler()
	d.API.Mount(d.Mux, "POST /api/v1/webhooks-out", h)
	d.API.Mount(d.Mux, "GET /api/v1/webhooks-out", h)
	d.API.Mount(d.Mux, "GET /api/v1/webhooks-out/{id}", h)
	d.API.Mount(d.Mux, "DELETE /api/v1/webhooks-out/{id}", h)
	d.API.Mount(d.Mux, "GET /api/v1/webhooks-out/{id}/deliveries", h)
	d.API.Mount(d.Mux, "POST /api/v1/webhooks-out/{id}/deliveries/{delivery}/redeliver", h)
	if err := svc.Start(d.Ctx); err != nil {
		d.Log.Error("outbound webhooks disabled", "err", err)
		return
	}
	if isPostgres(d.Cfg.DBPath) {
		d.Leader.Go(d.Ctx, func(ctx context.Context) { refreshWebhooks(ctx, d, svc) })
	}
}

func refreshWebhooks(ctx context.Context, d Deps, svc *webhookout.Service) {
	t := time.NewTicker(webhooksOutRefresh)
	defer t.Stop()
	for {
		if err := svc.Refresh(ctx); err != nil && ctx.Err() == nil {
			d.Log.Error("outbound webhooks refresh", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type leaderDue struct {
	webhookout.Store
	leader *ha.Elector
}

func (l leaderDue) Due(ctx context.Context, now time.Time, limit int) ([]*webhookout.Due, error) {
	if !l.leader.IsLeader() {
		return nil, nil
	}
	return l.Store.Due(ctx, now, limit)
}
