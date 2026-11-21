package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/SigmaUno/subspace/internal/breaker"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/usage"
)

func init() {
	registerMount(func(d Deps) {
		d.Board.SetCapacityGate(d.Breakers.Admit)
		h := breaker.Handler(d.Breakers)
		d.API.Mount(d.Mux, "GET /api/v1/breakers", h)
		d.API.Mount(d.Mux, "POST /api/v1/breakers/{provider}/reset", h)

		svc := usage.New(breakerUsageStore{usage.NewPersisted(d.Store, 1), d.Breakers}, usage.Config{})
		svc.Attrib = usage.StoreAttributor(d.Store)
		svc.Allow = func(ctx context.Context, a model.Actor, taskID, briefID string) error {
			if taskID != "" {
				t, err := d.Board.GetTask(ctx, a, taskID)
				if err != nil {
					return err
				}
				if briefID != "" && briefID != t.BriefID {
					return model.BadRequest("brief_id %s does not match the brief of task %s", briefID, taskID)
				}
				return nil
			}
			ok, err := d.Board.BriefVisible(ctx, a, briefID)
			if err == nil && !ok {
				err = model.NotFound("not found")
			}
			return err
		}
		svc.BriefOf = func(ctx context.Context, taskID string) (id string, err error) {
			err = d.Store.View(ctx, func(tx store.Tx) error {
				t, e := tx.GetTask(taskID)
				if e == nil {
					id = t.BriefID
				}
				return e
			})
			return
		}
		for _, t := range svc.Tools() {
			d.Registry.Register(t)
		}
		d.Registry.Register(providerErrorTool(d))

		detach := decisions.Attach(d.Store, breakerDecisions{d.Breakers})
		go func() {
			<-d.Ctx.Done()
			detach()
		}()
	})
}

func breakerLog(log *slog.Logger) func(breaker.Status) {
	return func(s breaker.Status) {
		log.Warn("provider breaker changed", "provider", s.Provider, "model", s.Model, "state", s.State, "error_rate", s.ErrorRate)
	}
}

type breakerUsageStore struct {
	usage.Store
	set *breaker.Set
}

func (s breakerUsageStore) Put(ctx context.Context, recs []usage.Record) error {
	if err := s.Store.Put(ctx, recs); err != nil {
		return err
	}
	for _, r := range recs {
		if r.Provider == "" {
			continue
		}
		s.set.Record(r.Provider, r.Model, r.RateLimited)
	}
	return nil
}

type breakerDecisions struct{ set *breaker.Set }

func (b breakerDecisions) Decisions(context.Context) ([]*decisions.Decision, error) {
	var out []*decisions.Decision
	for _, s := range b.set.List() {
		if s.State == breaker.Closed {
			continue
		}
		title := fmt.Sprintf("Provider %s/%s breaker is %s", s.Provider, s.Model, s.State)
		out = append(out, &decisions.Decision{
			ID: "breaker:" + s.Provider + ":" + s.Model, Kind: decisions.KindBreaker, Title: title,
			Summary:   fmt.Sprintf("no new agents; reset with POST /api/v1/breakers/%s/reset once the provider recovers", s.Provider),
			CreatedAt: *s.OpenedAt,
		})
	}
	return out, nil
}

func providerErrorTool(d Deps) *mcp.Tool {
	return &mcp.Tool{
		Name:        "report_provider_error",
		Description: "Report a failed model request (HTTP 5xx, 429, timeout) so Subspace can stop starting agents on an unhealthy provider.",
		Roles:       mcp.AllRoles,
		ReadOnly:    true,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"provider": map[string]any{"type": "string", "description": "defaults to your provider"},
				"model":    map[string]any{"type": "string", "description": "defaults to your model"},
				"status":   map[string]any{"type": "integer", "description": "HTTP status of the failed request, if any"},
			},
			"additionalProperties": false,
		},
		Handler: func(ctx context.Context, c *mcp.Call) (any, error) {
			var in struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
				Status   int    `json:"status"`
			}
			if len(c.Args) > 0 {
				if err := json.Unmarshal(c.Args, &in); err != nil {
					return nil, model.BadRequest("invalid arguments: %v", err)
				}
			}
			if in.Provider == "" || in.Model == "" {
				a, err := d.Board.GetAgent(ctx, c.Actor.ID)
				if err != nil {
					return nil, err
				}
				if in.Provider == "" {
					in.Provider = a.Provider
				}
				if in.Model == "" {
					in.Model = a.Model
				}
			}
			d.Breakers.Record(in.Provider, in.Model, true)
			return map[string]any{"ok": true, "provider": in.Provider, "model": in.Model, "state": d.Breakers.State(in.Provider, in.Model)}, nil
		},
	}
}
