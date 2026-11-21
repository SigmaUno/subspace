package server

import (
	"encoding/json"
	"net/http"

	"github.com/SigmaUno/subspace/internal/httpx"
	"github.com/SigmaUno/subspace/internal/model"
)

func init() {
	registerMount(func(d Deps) {
		d.API.Mount(d.Mux, "POST /api/v1/briefs/{id}/leader", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				AgentID json.RawMessage `json:"agent_id"`
			}
			raw, err := httpx.Body(w, r, 1<<16)
			if err != nil {
				httpx.WriteError(w, err)
				return
			}
			if len(raw) > 0 && json.Unmarshal(raw, &in) != nil {
				httpx.WriteError(w, model.BadRequest(`invalid JSON body: expected {"agent_id":"..."}`))
				return
			}
			if in.AgentID == nil {
				httpx.WriteError(w, model.BadRequest("agent_id is required (null for any leader)"))
				return
			}
			id := ""
			if string(in.AgentID) != "null" && json.Unmarshal(in.AgentID, &id) != nil {
				httpx.WriteError(w, model.BadRequest("agent_id must be a string or null"))
				return
			}
			br, err := d.Board.SetBriefLeader(r.Context(), r.PathValue("id"), id)
			if err != nil {
				httpx.WriteError(w, err)
				return
			}
			httpx.WriteJSON(w, http.StatusOK, br)
		}))
	})
}
