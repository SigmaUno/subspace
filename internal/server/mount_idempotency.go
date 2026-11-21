package server

import (
	"net/http"

	"github.com/SigmaUno/subspace/internal/idempotency"
)

func init() {
	wraps = append(wraps, func(next http.Handler, d Deps) http.Handler {
		st, ok := d.Store.(idempotency.Store)
		if !ok {
			d.Log.Error("idempotency keys disabled", "err", "store does not support idempotency keys")
			return next
		}
		return idempotency.Wrap(next, idempotency.Options{Store: st, Agents: d.Auth, Log: d.Log})
	})
}
