package insights

import (
	"net/http"

	"github.com/SigmaUno/subspace/internal/httpx"
	"github.com/SigmaUno/subspace/internal/model"
)

func Handler(svc *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /insights", func(w http.ResponseWriter, r *http.Request) {
		sev := r.URL.Query().Get("severity")
		if sev != "" && sev != SeverityHigh && sev != SeverityWarning {
			httpx.WriteError(w, model.BadRequest("severity must be %s or %s", SeverityWarning, SeverityHigh))
			return
		}
		rep, err := svc.All(r.Context(), sev)
		httpx.Reply(w, http.StatusOK, rep, err)
	})
	mux.HandleFunc("GET /agents/{id}/insights", func(w http.ResponseWriter, r *http.Request) {
		rep, err := svc.Agent(r.Context(), r.PathValue("id"))
		httpx.Reply(w, http.StatusOK, rep, err)
	})
	return mux
}
