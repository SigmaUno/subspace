package server

import (
	"time"

	"github.com/SigmaUno/subspace/internal/activity"
)

const activityInterval = 500 * time.Millisecond

func init() {
	registerMount(func(d Deps) {
		d.API.Mount(d.Mux, "GET /api/v1/activity", activity.Handler(activity.New(d.Store)))
		go activity.NewTailer(d.Store, d.Hub, d.Log).Run(d.Ctx, activityInterval)
	})
}
