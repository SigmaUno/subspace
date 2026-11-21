package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/SigmaUno/subspace/internal/briefs"
	"github.com/SigmaUno/subspace/internal/components"
	"github.com/SigmaUno/subspace/internal/model"
)

func init() {
	registerMount(func(d Deps) {
		h := briefs.Importer{
			CreateBrief: d.Board.CreateBrief,
			SetPlan: func(ctx context.Context, briefID string, specs []components.Spec) ([]components.Component, error) {
				return putComponents(ctx, d, briefID, specs)
			},
		}.Handler()
		d.API.Mount(d.Mux, "POST /api/v1/briefs/import", h)
		d.API.Mount(d.Mux, "GET /api/v1/brief-templates", h)
		d.API.Mount(d.Mux, "GET /api/v1/brief-templates/{id}/render", h)
	})
}

type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *recorder) WriteHeader(status int)      { r.status = status }

func putComponents(ctx context.Context, d Deps, briefID string, specs []components.Spec) ([]components.Component, error) {
	payload, err := json.Marshal(map[string]any{"components": specs})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "/api/v1/briefs/"+briefID+"/components", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+d.Cfg.OwnerToken)
	req.Header.Set("Content-Type", "application/json")
	rec := &recorder{header: http.Header{}, status: http.StatusOK}
	d.Mux.ServeHTTP(rec, req)
	var out struct {
		Components []components.Component `json:"components"`
		Error      *model.Error           `json:"error"`
	}
	if err := json.Unmarshal(rec.body.Bytes(), &out); err != nil {
		return nil, model.Errf("internal", "component plan for %s: %v", briefID, err)
	}
	if rec.status != http.StatusOK {
		if out.Error != nil {
			return nil, out.Error
		}
		return nil, model.Errf("internal", "component plan for %s: status %d", briefID, rec.status)
	}
	return out.Components, nil
}
