package server

import (
	"context"
	"fmt"

	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/policy"
)

func init() { registerMount(mountPolicy) }

func mountPolicy(d Deps) {
	b := d.Board
	svc := policy.New(policy.Options{
		Store: d.Store, Pub: d.Hub, Now: d.Cfg.Now, Log: d.Log,
		Notify: func(ctx context.Context, agentID, body string) error {
			_, err := b.PostSystemToAgents(ctx, []string{agentID}, body)
			return err
		},
	})
	svc.SetReplayer(policy.ActAssign, func(ctx context.Context, h *policy.Hold) (string, error) {
		actor := model.Actor{ID: h.ActorID, Role: h.ActorRole}
		agent := h.Args["agent_id"]
		var (
			t   *model.Task
			err error
		)
		if h.Args["tool"] == "reassign" {
			t, err = b.Reassign(ctx, actor, h.TaskID, agent, h.Args["reason"])
		} else {
			t, err = b.Assign(ctx, actor, h.TaskID, agent, h.Args["as"])
		}
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s is %s with %s", t.ID, t.Status, agent), nil
	})
	svc.SetReplayer(policy.ActApprovePlan, func(ctx context.Context, h *policy.Hold) (string, error) {
		p, err := b.ApprovePlan(ctx, h.PlanID, h.Args["note"])
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("plan %s %s", p.ID, p.Status), nil
	})
	b.SetPolicy(svc)
	h := policy.Handler(svc)
	for _, route := range []string{
		"GET /api/v1/policies",
		"POST /api/v1/policies",
		"GET /api/v1/policies/facts",
		"POST /api/v1/policies/lint",
		"POST /api/v1/policies/evaluate",
		"GET /api/v1/policies/decisions",
		"GET /api/v1/policy-holds",
		"GET /api/v1/policy-holds/{id}",
		"POST /api/v1/policy-holds/{id}/approve",
		"POST /api/v1/policy-holds/{id}/reject",
		"GET /api/v1/policies/{id}",
		"PUT /api/v1/policies/{id}",
		"DELETE /api/v1/policies/{id}",
		"GET /api/v1/policies/{id}/versions",
	} {
		d.API.Mount(d.Mux, route, h)
	}
	detach := decisions.Attach(d.Store, svc.DecisionSource())
	go func() {
		<-d.Ctx.Done()
		detach()
	}()
	d.Leader.Go(d.Ctx, func(ctx context.Context) {
		svc.Run(ctx, envDuration(d.Log, "SUBSPACE_POLICY_SWEEP", policy.DefaultSweep))
	})
}
