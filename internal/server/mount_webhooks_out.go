package server

import (
	"os"

	"github.com/SigmaUno/subspace/internal/webhookout"
)

const envWebhooksOutAllow = "SUBSPACE_WEBHOOKS_OUT_ALLOW_CIDRS"

func init() { registerMount(mountWebhooksOut) }

func mountWebhooksOut(d Deps) {
	allow, err := webhookout.ParsePrefixes(os.Getenv(envWebhooksOutAllow))
	if err != nil {
		d.Log.Error("outbound webhook allowlist ignored", "err", err)
		allow = nil
	}
	svc := webhookout.New(webhookout.Options{
		Store: webhookout.NewSQLStore(d.Store),
		Hub:   d.Hub,
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
	}
}
