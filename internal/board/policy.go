package board

import (
	"context"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	CodePolicyDenied = "policy_denied"
	CodePolicyHold   = "policy_hold"
)

type PolicyRequest struct {
	Action string
	Actor  model.Actor
	Task   *model.Task
	Brief  *model.Brief
	Plan   *model.Plan
	Target *model.Agent
	As     string
	Spawn  *SpawnInput
	Args   map[string]string
	Now    time.Time
}

type PolicyWarning struct {
	Policy  string
	Rule    string
	Message string
}

type PolicyVerdict struct {
	Deny     error
	Hold     bool
	Warnings []PolicyWarning
	State    any
}

type Policy interface {
	Evaluate(tx store.Tx, req *PolicyRequest) (*PolicyVerdict, error)
	Settle(ctx context.Context, req *PolicyRequest, v *PolicyVerdict, committed bool) error
}

type policyCheck struct {
	req *PolicyRequest
	v   *PolicyVerdict
}

func (b *Board) SetPolicy(p Policy) {
	b.mu.Lock()
	b.policy = p
	b.mu.Unlock()
}

func (b *Board) checkPolicy(tx store.Tx, w *wctx, req PolicyRequest) error {
	b.mu.Lock()
	p := b.policy
	b.mu.Unlock()
	if p == nil {
		return nil
	}
	req.Now = b.now()
	v, err := p.Evaluate(unscoped(tx), &req)
	if err != nil || v == nil {
		return err
	}
	w.policy = append(w.policy, policyCheck{&req, v})
	if v.Deny != nil {
		return v.Deny
	}
	if v.Hold {
		return model.Errf(CodePolicyHold, "%s is held for owner approval", req.Action)
	}
	for _, warn := range v.Warnings {
		body := "Policy warning (" + warn.Policy + "/" + warn.Rule + ") on " + req.Action + ": " + warn.Message
		kind, id := model.ThreadAgent, req.Actor.ID
		switch {
		case req.Task != nil && req.Task.ID != "":
			kind, id = model.ThreadTask, req.Task.ID
		case req.Brief != nil:
			kind, id = model.ThreadBrief, req.Brief.ID
		}
		if err := w.post(tx, kind, id, model.Actor{ID: "policy", Role: "system"}, model.MsgSystem, clip(body, maxBody)); err != nil {
			return err
		}
		taskID := ""
		if req.Task != nil {
			taskID = req.Task.ID
		}
		after := map[string]any{"action": req.Action, "policy": warn.Policy, "rule": warn.Rule, "message": warn.Message}
		if err := w.audit(tx, req.Actor, "policy_warn", taskID, nil, after, ""); err != nil {
			return err
		}
	}
	return nil
}

func (b *Board) settlePolicy(ctx context.Context, checks []policyCheck, err error) error {
	b.mu.Lock()
	p := b.policy
	b.mu.Unlock()
	if p == nil {
		return err
	}
	committed := err == nil
	held := !committed && model.AsError(err).Code == CodePolicyHold
	for _, c := range checks {
		serr := p.Settle(context.WithoutCancel(ctx), c.req, c.v, committed)
		if serr == nil {
			continue
		}
		if held && c.v.Hold && model.AsError(serr).Code == CodePolicyHold {
			err = serr
			continue
		}
		b.log.Error("settle policy decision", "action", c.req.Action, "err", serr)
	}
	return err
}
