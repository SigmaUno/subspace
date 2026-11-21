package server

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/actionlink"
	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/ratelimit"
	"github.com/SigmaUno/subspace/internal/reload"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	envActionSecret       = "SUBSPACE_ACTION_SECRET"
	envRateActionLink     = "SUBSPACE_RATE_ACTIONLINK"
	defaultRateActionLink = "30/m:10"
)

func init() {
	registerMount(mountActionLinks)
	wraps = append(wraps, actionLinkRateLimit)
}

func actionIssuer(d Deps) (*actionlink.Issuer, error) {
	secret := d.getenv()(envActionSecret)
	if secret == "" {
		secret = d.Cfg.OwnerToken
	}
	return actionlink.NewIssuer([]byte(secret), nil)
}

func mountActionLinks(d Deps) {
	nonces, ok := d.Store.(actionlink.Nonces)
	if !ok || d.Feed == nil {
		d.Log.Error("action links disabled", "err", "store or decision feed unavailable")
		return
	}
	iss, err := actionIssuer(d)
	if err != nil {
		d.Log.Error("action links disabled", "err", err)
		return
	}
	h := &actionlink.Handler{
		Issuer: iss, Inbox: d.Feed, Nonces: nonces, PublicURL: d.Cfg.PublicURL, Log: d.Log,
		Options: func(ctx context.Context, id string) ([]string, error) {
			var opts []string
			err := d.Store.View(ctx, func(tx store.Tx) error {
				q, err := tx.GetQuestion(id)
				if err == nil {
					opts = q.Options
				}
				return err
			})
			return opts, err
		},
	}
	d.Mux.Handle("/a/", h.Page())
	d.API.Mount(d.Mux, "/api/v1/action-links", h.REST())
}

func actionMinter(d Deps) notify.Minter {
	if !strings.HasPrefix(d.Cfg.PublicURL, "https://") {
		return nil
	}
	iss, err := actionIssuer(d)
	if err != nil {
		return nil
	}
	h := &actionlink.Handler{Issuer: iss, PublicURL: d.Cfg.PublicURL}
	return func(decision, action string, option int) (string, error) {
		u, _, err := h.URL(decision, action, option, actionlink.DefaultTTL)
		return u, err
	}
}

type actionRate struct {
	next    http.Handler
	ips     *allowlisted
	limiter *ratelimit.Limiter
	lim     atomic.Pointer[ratelimit.Limit]
}

func actionLinkRateLimit(next http.Handler, d Deps) http.Handler {
	trust := d.Trust
	if trust == nil {
		trust = newProxyTrust(d.Log, d.getenv())
	}
	lim := envRate(d.Log, d.getenv(), envRateActionLink, defaultRateActionLink)
	h := &actionRate{next: next, ips: &allowlisted{trust: trust}, limiter: ratelimit.New(ratelimit.Options{Default: lim})}
	h.lim.Store(&lim)
	d.reloadable("actionlink-ratelimit", []string{envRateActionLink}, h)
	return h
}

func (h *actionRate) PrepareReload(getenv func(string) string) (reload.Plan, error) {
	lim, err := rateFrom(getenv, envRateActionLink, defaultRateActionLink)
	if err != nil {
		return reload.Plan{}, err
	}
	return reload.Plan{Commit: func() {
		h.limiter.SetDefault(lim)
		h.lim.Store(&lim)
	}}, nil
}

func (h *actionRate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	lim := *h.lim.Load()
	if lim.PerMinute > 0 && strings.HasPrefix(r.URL.Path, "/a/") {
		key := "invalid"
		if ip, ok := h.ips.client(r); ok {
			key = ip.String()
		}
		if !h.limiter.Allow(key) {
			deny(w, time.Duration(60/lim.PerMinute*float64(time.Second)))
			return
		}
	}
	h.next.ServeHTTP(w, r)
}
