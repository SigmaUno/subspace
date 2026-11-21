package server

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/actionlink"
	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/ratelimit"
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
	secret := os.Getenv(envActionSecret)
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

func actionLinkRateLimit(next http.Handler, d Deps) http.Handler {
	lim := envRate(d.Log, envRateActionLink, defaultRateActionLink)
	if lim.PerMinute <= 0 {
		return next
	}
	ips := &allowlisted{}
	if v := strings.TrimSpace(os.Getenv(envTrustedProxies)); v != "" {
		ips.trusted, _ = parsePrefixes(v)
	}
	limiter := ratelimit.New(ratelimit.Options{Default: lim})
	retry := time.Duration(60 / lim.PerMinute * float64(time.Second))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/a/") {
			key := "invalid"
			if ip, ok := ips.client(r); ok {
				key = ip.String()
			}
			if !limiter.Allow(key) {
				deny(w, retry)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
