package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/helpreq"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/model"
)

func init() { registerMount(mountHelpRequests) }

func mountHelpRequests(d Deps) {
	svc := helpreq.New(d.Store, d.Board)
	mcp.BindHelp(d.Board, svc)
	detach := decisions.Attach(d.Store, svc)
	go func() {
		<-d.Ctx.Done()
		detach()
	}()
	d.API.Mount(d.Mux, "GET /api/v1/help-requests", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f := helpreq.Filter{Status: q.Get("status"), BriefID: q.Get("brief_id"), TaskID: q.Get("task_id")}
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				writeHelp(w, http.StatusBadRequest, helpErrBody(model.BadRequest("limit must be a positive integer")))
				return
			}
			f.Limit = n
		}
		rs, err := svc.List(r.Context(), f)
		if err != nil {
			e := model.AsError(err)
			writeHelp(w, e.HTTPStatus(), helpErrBody(e))
			return
		}
		writeHelp(w, http.StatusOK, map[string]any{"help_requests": rs})
	}))
}

func helpErrBody(e error) map[string]any {
	me := model.AsError(e)
	return map[string]any{"error": map[string]string{"code": me.Code, "message": me.Message}}
}

func writeHelp(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
