package policy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

type settleState struct {
	req    *Request
	res    *Result
	key    string
	holdID string
}

type cache struct {
	mu     sync.Mutex
	loaded bool
	rev    string
	set    []*Compiled
}

func (c *cache) current(t store.Tx) ([]*Compiled, error) {
	rev, _, err := t.GetKV(revKey)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded && c.rev == rev {
		return c.set, nil
	}
	q, err := sqlOf(t)
	if err != nil {
		return nil, err
	}
	ps, err := listPolicies(q)
	if err != nil {
		return nil, err
	}
	set := make([]*Compiled, 0, len(ps))
	for _, p := range ps {
		if !p.Enabled {
			continue
		}
		cp, _ := compile(p.ID, p.Version, p.Document)
		if cp != nil {
			set = append(set, cp)
		}
	}
	c.set, c.rev, c.loaded = set, rev, true
	return set, nil
}

func (c *cache) invalidate() {
	c.mu.Lock()
	c.loaded = false
	c.mu.Unlock()
}

func toRequest(br *board.PolicyRequest) *Request {
	r := &Request{Action: br.Action, Actor: br.Actor, Task: br.Task, Brief: br.Brief, Plan: br.Plan, Target: br.Target,
		As: br.As, Args: br.Args, Now: br.Now}
	if br.Spawn != nil {
		r.Spawn = &Spawn{Kind: br.Spawn.Kind, Provider: br.Spawn.Provider, Model: br.Spawn.Model, Role: br.Spawn.Role, N: br.Spawn.N}
	}
	return r
}

func holdKey(r *Request) string {
	return strings.Join([]string{r.Action, r.Actor.ID, r.ref(), r.target(), r.As, callDigest(r)}, "|")
}

func callDigest(r *Request) string {
	call := struct {
		Args map[string]string `json:"args"`
		Task any               `json:"task,omitempty"`
	}{Args: r.Args}
	if t := r.Task; t != nil && (r.Action == ActCreateTask || r.Action == ActUpdateTask) {
		call.Task = []any{t.Title, t.Spec, t.Repo, t.Role, t.Paths, t.AcceptanceCriteria, t.DependsOn, t.Budget, t.Priority, t.ComponentID}
	}
	b, _ := json.Marshal(call)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

func (s *Service) Evaluate(tx store.Tx, breq *board.PolicyRequest) (*board.PolicyVerdict, error) {
	set, err := s.cache.current(tx)
	if err != nil {
		return nil, err
	}
	if !relevant(set, breq.Action) {
		return nil, nil
	}
	req := toRequest(breq)
	rv := newResolver(tx, req)
	res := Decide(set, req.Action, rv, req.Now, nil)
	if rv.err != nil {
		return nil, rv.err
	}
	if !res.applied() {
		return nil, nil
	}
	st := &settleState{req: req, res: res, key: holdKey(req)}
	v := &board.PolicyVerdict{State: st}
	switch res.Outcome {
	case OutcomeDeny:
		v.Deny = &model.Error{Code: board.CodePolicyDenied, Message: "denied by policy: " + res.Message, Details: denyDetails(res)}
		return v, nil
	case OutcomeHold:
		q, err := sqlOf(tx)
		if err != nil {
			return nil, err
		}
		h, err := openHold(q, st.key)
		if err != nil {
			return nil, err
		}
		switch {
		case h != nil && h.Status == HoldApproved:
			h.Status, h.Result = HoldConsumed, "released by "+req.Action
			if err := setHold(q, h); err != nil {
				return nil, err
			}
			st.holdID, res.Outcome = h.ID, OutcomeApproved
		case h != nil && req.Now.Before(h.ExpiresAt):
			st.holdID, v.Hold = h.ID, true
			return v, nil
		default:
			v.Hold = true
			return v, nil
		}
	}
	for _, m := range res.warnings() {
		v.Warnings = append(v.Warnings, board.PolicyWarning{Policy: m.Policy, Rule: m.Rule, Message: m.Message})
	}
	return v, nil
}

func denyDetails(res *Result) map[string]any {
	for _, m := range res.Matched {
		if m.Applied && m.Effect != EffectWarn && m.Effect != EffectApproval {
			return map[string]any{"policy_id": m.PolicyID, "policy": m.Policy, "rule": m.Rule, "effect": m.Effect}
		}
	}
	return nil
}

func (s *Service) Settle(ctx context.Context, _ *board.PolicyRequest, v *board.PolicyVerdict, committed bool) error {
	st, ok := v.State.(*settleState)
	if !ok {
		return nil
	}
	res, req := st.res, st.req
	if committed == (v.Deny != nil || v.Hold) {
		return nil
	}
	d := &Decision{TS: req.Now, Action: req.Action, ActorID: req.Actor.ID, ActorRole: req.Actor.Role, Target: req.target(),
		Outcome: res.Outcome, Message: res.Message, HoldID: st.holdID, Matched: res.Matched, Facts: res.Facts, Errors: res.Errors}
	if req.Task != nil {
		d.TaskID = req.Task.ID
	}
	if req.Brief != nil {
		d.BriefID = req.Brief.ID
	}
	var hold *Hold
	err := s.st.Atomic(ctx, withSQL(func(t store.Tx, q sqlTx) error {
		hold = nil
		if v.Hold {
			h, err := s.ensureHold(t, q, st)
			if err != nil {
				return err
			}
			hold, d.HoldID = h, h.ID
		}
		return insertDecision(t, q, d)
	}))
	if err != nil {
		return err
	}
	if hold == nil {
		return nil
	}
	s.publish("policy.hold", hold)
	return &model.Error{Code: board.CodePolicyHold,
		Message: fmt.Sprintf("%s is held for owner approval as %s: %s; it proceeds once the owner approves (expires %s)",
			req.Action, hold.ID, res.Message, hold.ExpiresAt.Format(time.RFC3339)),
		Details: map[string]any{"hold_id": hold.ID, "expires_at": hold.ExpiresAt}}
}

func (s *Service) ensureHold(t store.Tx, q sqlTx, st *settleState) (*Hold, error) {
	if st.holdID != "" {
		h, err := getHold(q, st.holdID)
		if err == nil && h.Status == HoldPending {
			return h, nil
		}
	}
	if h, err := openHold(q, st.key); err != nil || (h != nil && h.Status == HoldPending && st.req.Now.Before(h.ExpiresAt)) {
		return h, err
	}
	req := st.req
	timeout := st.res.timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	h := &Hold{key: st.key, Status: HoldPending, Action: req.Action, ActorID: req.Actor.ID, ActorRole: req.Actor.Role,
		Target: req.target(), Args: map[string]string{}, Message: st.res.Message, Matched: st.res.Matched,
		CreatedAt: req.Now, ExpiresAt: req.Now.Add(timeout)}
	for k, v := range req.Args {
		h.Args[k] = v
	}
	if req.As != "" {
		h.Args["as"] = req.As
	}
	if req.Target != nil {
		h.Args["agent_id"] = req.Target.ID
	}
	if req.Task != nil {
		h.TaskID, h.BriefID = req.Task.ID, req.Task.BriefID
	}
	if req.Brief != nil {
		h.BriefID = req.Brief.ID
	}
	if req.Plan != nil {
		h.PlanID = req.Plan.ID
	}
	if err := insertHold(t, q, h); err != nil {
		return nil, err
	}
	note := h.ID
	return h, t.InsertAudit(&model.AuditEvent{TS: req.Now, AgentID: req.Actor.ID, Role: req.Actor.Role, Tool: "policy_hold",
		TaskID: optString(h.TaskID), Note: &note,
		After: map[string]any{"hold_id": h.ID, "action": h.Action, "message": h.Message, "expires_at": h.ExpiresAt}})
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
