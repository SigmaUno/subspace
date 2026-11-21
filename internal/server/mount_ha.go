package server

import (
	"github.com/SigmaUno/subspace/internal/httpx"
	"net/http"

	"github.com/SigmaUno/subspace/internal/ha"
)

func init() {
	registerSchema(ha.EventsSchema, ha.EventsTable)
	registerMount(func(d Deps) {
		d.API.Mount(d.Mux, "GET /api/v1/ha", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			st := d.Leader.Status()
			if b, ok := d.Hub.(*ha.Bridge); ok {
				st.Events = b.Stats()
			}
			httpx.WriteJSON(w, http.StatusOK, st)
		}))
	})
}
