package server

import (
	"context"
	"math"
	"net/http"

	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/helpreq"
	"github.com/SigmaUno/subspace/internal/httpx"
	"github.com/SigmaUno/subspace/internal/mcp"
)

func init() { registerMount(mountHelpRequests) }

func mountHelpRequests(d Deps) {
	svc := helpreq.New(d.Store, d.Board)
	mcp.BindHelp(d.Board, svc)
	detach := decisions.Attach(d.Store, svc)
	context.AfterFunc(d.Ctx, detach)
	d.API.Mount(d.Mux, "GET /api/v1/help-requests", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f := helpreq.Filter{Status: q.Get("status"), BriefID: q.Get("brief_id"), TaskID: q.Get("task_id")}
		limit, err := httpx.PositiveLimit(r, 0, math.MaxInt)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		f.Limit = limit
		rs, err := svc.List(r.Context(), f)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"help_requests": rs})
	}))
}
