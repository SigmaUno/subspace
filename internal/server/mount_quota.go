package server

import (
	"net/http"
	"sync"

	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/quota"
)

var quotaServices sync.Map

func init() {
	registerMount(mountQuota)
	wraps = append(wraps, func(next http.Handler, d Deps) http.Handler {
		v, ok := quotaServices.Load(d.Mux)
		if !ok {
			return next
		}
		return quota.Enrich(v.(*quota.Service), next)
	})
}

func mountQuota(d Deps) {
	svc := quota.New(quota.Options{Store: d.Store, Pub: d.Hub, Now: d.Cfg.Now})
	d.Board.SetGate(svc.Gate)
	h := quota.Handler(svc)
	d.API.Mount(d.Mux, "PUT /api/v1/agents/{id}/quota", h)
	d.API.Mount(d.Mux, "POST /api/v1/agents/quota-bulk", h)
	detach := decisions.Attach(d.Store, svc)
	quotaServices.Store(d.Mux, svc)
	go func() {
		<-d.Ctx.Done()
		detach()
		quotaServices.Delete(d.Mux)
	}()
}
