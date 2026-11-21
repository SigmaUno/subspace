package server

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/SigmaUno/subspace/internal/model"
)

func init() {
	registerMount(func(d Deps) {
		d.API.Mount(d.Mux, "POST /api/v1/briefs/{id}/leader", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				AgentID json.RawMessage `json:"agent_id"`
			}
			raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
			if err != nil {
				writeAPIError(w, model.BadRequest("request body too large or unreadable"))
				return
			}
			if len(raw) > 0 && json.Unmarshal(raw, &in) != nil {
				writeAPIError(w, model.BadRequest(`invalid JSON body: expected {"agent_id":"..."}`))
				return
			}
			if in.AgentID == nil {
				writeAPIError(w, model.BadRequest("agent_id is required (null for any leader)"))
				return
			}
			id := ""
			if string(in.AgentID) != "null" && json.Unmarshal(in.AgentID, &id) != nil {
				writeAPIError(w, model.BadRequest("agent_id must be a string or null"))
				return
			}
			br, err := d.Board.SetBriefLeader(r.Context(), r.PathValue("id"), id)
			if err != nil {
				writeAPIError(w, err)
				return
			}
			writeAPIJSON(w, http.StatusOK, br)
		}))
	})
}
