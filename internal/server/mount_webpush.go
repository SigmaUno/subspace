package server

import (
	"os"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/webhookout"
	"github.com/SigmaUno/subspace/internal/webpush"
)

const (
	envWebPushSubject  = "SUBSPACE_WEBPUSH_SUBJECT"
	envWebPushHosts    = "SUBSPACE_WEBPUSH_ALLOW_HOSTS"
	envWebPushCIDRs    = "SUBSPACE_WEBPUSH_ALLOW_CIDRS"
	envWebPushTTL      = "SUBSPACE_WEBPUSH_TTL"
	envRateWebPush     = "SUBSPACE_RATE_WEBPUSH"
	defaultRateWebPush = "30/m:10"
)

func init() { registerMount(mountWebPush) }

func mountWebPush(d Deps) {
	hosts, err := webpush.NewHosts(os.Getenv(envWebPushHosts))
	if err != nil {
		d.Log.Error("web push host allowlist ignored", "err", err)
		hosts, _ = webpush.NewHosts("")
	}
	allow, err := webhookout.ParsePrefixes(os.Getenv(envWebPushCIDRs))
	if err != nil {
		d.Log.Error("web push address allowlist ignored", "err", err)
		allow = nil
	}
	ttl := webpush.DefaultTTL
	if v := strings.TrimSpace(os.Getenv(envWebPushTTL)); v != "" {
		if t, err := time.ParseDuration(v); err == nil && t > 0 && t <= 28*24*time.Hour {
			ttl = t
		} else {
			d.Log.Error("invalid web push TTL, using default", "env", envWebPushTTL)
		}
	}
	subject := strings.TrimSpace(os.Getenv(envWebPushSubject))
	if subject == "" && strings.HasPrefix(d.Cfg.PublicURL, "https://") {
		subject = d.Cfg.PublicURL
	}
	var (
		svc   *webpush.Service
		start sync.Once
	)
	attach := func() {
		start.Do(func() {
			notify.New(notify.Config{Sinks: []notify.Sink{svc}, Log: d.Log, Mint: actionMinter(d)}).Start(d.Ctx, d.Hub)
		})
	}
	svc, err = webpush.New(d.Ctx, webpush.Options{
		Store:   d.Store,
		Hosts:   hosts,
		Guard:   webhookout.NewGuard(allow),
		Subject: subject,
		TTL:     ttl,
		Rate:    envRate(d.Log, envRateWebPush, defaultRateWebPush),
		Log:     d.Log,

		OnSubscribe: attach,
	})
	if err != nil {
		d.Log.Error("web push disabled", "err", err)
		return
	}
	h := svc.Handler()
	d.API.Mount(d.Mux, "GET /api/v1/push/vapid-key", h)
	d.API.Mount(d.Mux, "GET /api/v1/push/subscriptions", h)
	d.API.Mount(d.Mux, "POST /api/v1/push/subscriptions", h)
	d.API.Mount(d.Mux, "DELETE /api/v1/push/subscriptions", h)
	if subs, err := svc.List(d.Ctx); err != nil {
		d.Log.Error("web push subscriptions unavailable", "err", err)
	} else if len(subs) > 0 {
		attach()
	}
}
