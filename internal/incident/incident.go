package incident

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	PauseAll           = "pause_all"
	RevokeAgents       = "revoke_agents"
	RotateLeaderTokens = "rotate_leader_tokens"
	Snapshot           = "snapshot"
	Notify             = "notify"

	StatusOK     = "ok"
	StatusFailed = "failed"

	maxReason = 500
	idPrefix  = "INC"
)

var order = []string{PauseAll, RevokeAgents, RotateLeaderTokens, Snapshot, Notify}

type Control interface {
	PauseAll(ctx context.Context, reason string) error
	ResumeAll(ctx context.Context) error
	Paused() (bool, string)
	ListAgents(ctx context.Context, role, status string) ([]*model.Agent, error)
	RevokeAgent(ctx context.Context, id, reason string) (*model.Agent, error)
	RotateToken(ctx context.Context, id string) (*board.Registered, error)
	ResumeAgent(ctx context.Context, id string) (*model.Agent, error)
}

type Options struct {
	Store   store.Store
	Control Control
	Pub     events.Publisher
	Dir     string
	Now     func() time.Time
	Log     *slog.Logger
}

type Service struct {
	o  Options
	mu sync.Mutex
}

func New(o Options) *Service {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Service{o: o}
}

type Scope struct {
	Role     string `json:"role,omitempty"`
	Provider string `json:"provider,omitempty"`
}

type Request struct {
	Reason  string   `json:"reason"`
	Actions []string `json:"actions"`
	Revoke  Scope    `json:"revoke"`
}

type Failure struct {
	AgentID string `json:"agent_id"`
	Error   string `json:"error"`
}

type Result struct {
	Action   string    `json:"action"`
	Status   string    `json:"status"`
	Error    string    `json:"error,omitempty"`
	Detail   string    `json:"detail,omitempty"`
	Agents   []string  `json:"agents,omitempty"`
	Failures []Failure `json:"failures,omitempty"`
}

type Incident struct {
	ID        string     `json:"id"`
	Reason    string     `json:"reason"`
	Actions   []string   `json:"actions"`
	Results   []Result   `json:"results"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
	Actor     string     `json:"actor"`
}

type StartOutcome struct {
	Incident
	Created   bool            `json:"-"`
	Skipped   []string        `json:"skipped,omitempty"`
	Tokens    []board.Connect `json:"tokens,omitempty"`
	Succeeded int             `json:"succeeded"`
	Failed    int             `json:"failed"`
}

type EndRequest struct {
	ResumeAgents bool   `json:"resume_agents"`
	Note         string `json:"note"`
}

type EndOutcome struct {
	Incident
	ResumedAll    bool      `json:"resumed_all"`
	ResumedAgents []string  `json:"resumed_agents,omitempty"`
	Failures      []Failure `json:"failures,omitempty"`
}

func (s *Service) now() time.Time {
	if s.o.Now != nil {
		return s.o.Now().UTC().Truncate(time.Millisecond)
	}
	return time.Now().UTC().Truncate(time.Millisecond)
}

func (in *Incident) result(action string) *Result {
	for i := range in.Results {
		if in.Results[i].Action == action {
			return &in.Results[i]
		}
	}
	return nil
}

func (in *Incident) done(action string) bool {
	r := in.result(action)
	return r != nil && r.Status == StatusOK
}

func (in *Incident) record(r Result) {
	if old := in.result(r.Action); old != nil {
		*old = r
		return
	}
	in.Results = append(in.Results, r)
}

func (r *Request) normalize() error {
	r.Reason = strings.TrimSpace(r.Reason)
	switch {
	case r.Reason == "":
		return model.BadRequest("reason is required")
	case len([]rune(r.Reason)) > maxReason:
		return model.BadRequest("reason must be at most %d characters", maxReason)
	case len(r.Actions) == 0:
		return model.BadRequest("actions must list at least one of %s", strings.Join(order, ", "))
	}
	for _, a := range r.Actions {
		if !slices.Contains(order, a) {
			return model.BadRequest("unknown action %q, expected one of %s", a, strings.Join(order, ", "))
		}
	}
	r.Actions = sortedActions(r.Actions)
	r.Revoke.Role = strings.TrimSpace(r.Revoke.Role)
	r.Revoke.Provider = strings.TrimSpace(r.Revoke.Provider)
	if r.Revoke != (Scope{}) && !slices.Contains(r.Actions, RevokeAgents) {
		return model.BadRequest("revoke applies to the revoke_agents action only")
	}
	if r.Revoke.Role != "" && !model.ValidAgentRole(r.Revoke.Role) {
		return model.BadRequest("revoke.role must be leader, worker, tester or reviewer")
	}
	return nil
}

func sortedActions(in []string) []string {
	out := make([]string, 0, len(in))
	for _, a := range order {
		if slices.Contains(in, a) {
			out = append(out, a)
		}
	}
	return out
}

func actor(ctx context.Context) string {
	if p, ok := auth.PrincipalFrom(ctx); ok && p.TokenID != "" {
		return p.TokenID
	}
	return model.Owner.ID
}

func (s *Service) Start(ctx context.Context, req Request) (*StartOutcome, error) {
	if err := req.normalize(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var inc *Incident
	created := false
	err := s.o.Store.Atomic(ctx, func(tx store.Tx) error {
		active, err := activeTx(tx)
		if err != nil {
			return err
		}
		if active != nil {
			inc = active
			merged := sortedActions(append(slices.Clone(active.Actions), req.Actions...))
			if slices.Equal(merged, active.Actions) {
				return nil
			}
			inc.Actions = merged
			return updateTx(tx, inc)
		}
		id, err := tx.NextID(idPrefix)
		if err != nil {
			return err
		}
		inc = &Incident{ID: id, Reason: req.Reason, Actions: req.Actions, Results: []Result{}, StartedAt: s.now(), Actor: actor(ctx)}
		created = true
		if err := insertTx(tx, inc); err != nil {
			return err
		}
		return s.audit(ctx, tx, "incident_start", id, nil, map[string]any{"id": id, "reason": inc.Reason, "actions": inc.Actions})
	})
	if err != nil {
		return nil, err
	}

	out := &StartOutcome{Created: created}
	for _, action := range req.Actions {
		if action != RevokeAgents && inc.done(action) {
			out.Skipped = append(out.Skipped, action)
			continue
		}
		res := s.run(ctx, inc, action, req.Revoke, out)
		inc.record(res)
		if err := s.o.Store.Atomic(ctx, func(tx store.Tx) error { return updateTx(tx, inc) }); err != nil {
			s.o.Log.Error("incident: recording result", "incident", inc.ID, "action", action, "err", err)
		}
	}
	for _, r := range inc.Results {
		if r.Status == StatusOK {
			out.Succeeded++
		} else {
			out.Failed++
		}
	}
	out.Incident = *inc
	return out, nil
}

func (s *Service) run(ctx context.Context, inc *Incident, action string, scope Scope, out *StartOutcome) Result {
	res := Result{Action: action, Status: StatusOK}
	var err error
	switch action {
	case PauseAll:
		err = s.pauseAll(ctx, inc, &res)
	case RevokeAgents:
		s.revoke(ctx, inc, scope, &res)
	case RotateLeaderTokens:
		s.rotate(ctx, inc, &res, out)
	case Snapshot:
		res.Detail, err = s.snapshot(ctx, inc)
	case Notify:
		s.notify(inc)
		res.Detail = "published"
	}
	if err != nil {
		res.Status, res.Error = StatusFailed, err.Error()
	}
	if len(res.Failures) > 0 {
		res.Status = StatusFailed
	}
	return res
}

func (s *Service) pauseAll(ctx context.Context, inc *Incident, res *Result) error {
	if paused, why := s.o.Control.Paused(); paused {
		res.Detail = "already paused: " + why
		return nil
	}
	return s.o.Control.PauseAll(ctx, fmt.Sprintf("incident %s: %s", inc.ID, inc.Reason))
}

func (s *Service) revoke(ctx context.Context, inc *Incident, scope Scope, res *Result) {
	if prev := inc.result(RevokeAgents); prev != nil {
		res.Agents = slices.Clone(prev.Agents)
	}
	agents, err := s.o.Control.ListAgents(ctx, scope.Role, "")
	if err != nil {
		res.Status, res.Error = StatusFailed, err.Error()
		return
	}
	for _, a := range agents {
		if a.Status == model.AgentRevoked || scope.Provider != "" && a.Provider != scope.Provider {
			continue
		}
		if _, err := s.o.Control.RevokeAgent(ctx, a.ID, "incident "+inc.ID); err != nil {
			res.Failures = append(res.Failures, Failure{AgentID: a.ID, Error: err.Error()})
			continue
		}
		res.Agents = append(res.Agents, a.ID)
	}
	res.Detail = scopeText(scope)
}

func scopeText(s Scope) string {
	var parts []string
	if s.Role != "" {
		parts = append(parts, "role="+s.Role)
	}
	if s.Provider != "" {
		parts = append(parts, "provider="+s.Provider)
	}
	if len(parts) == 0 {
		return "all agents"
	}
	return strings.Join(parts, " ")
}

func (s *Service) rotate(ctx context.Context, inc *Incident, res *Result, out *StartOutcome) {
	leaders, err := s.o.Control.ListAgents(ctx, model.RoleLeader, "")
	if err != nil {
		res.Status, res.Error = StatusFailed, err.Error()
		return
	}
	for _, a := range leaders {
		if a.Status == model.AgentRevoked {
			continue
		}
		reg, err := s.o.Control.RotateToken(ctx, a.ID)
		if err != nil {
			res.Failures = append(res.Failures, Failure{AgentID: a.ID, Error: err.Error()})
			continue
		}
		res.Agents = append(res.Agents, a.ID)
		out.Tokens = append(out.Tokens, reg.Connect)
	}
}

func (s *Service) notify(inc *Incident) {
	if s.o.Pub == nil {
		return
	}
	s.o.Pub.Publish([]string{"alerts"}, "alert.raised", notify.Notification{
		ID: "incident:" + inc.ID, Kind: "incident", Priority: 5, TS: s.now(),
		Title: fmt.Sprintf("Incident %s: %s", inc.ID, inc.Reason),
		Text:  summary(inc),
	})
}

func summary(inc *Incident) string {
	parts := make([]string, 0, len(inc.Results))
	for _, r := range inc.Results {
		p := r.Action + " " + r.Status
		if n := len(r.Agents); n > 0 {
			p += fmt.Sprintf(" (%d agents)", n)
		}
		if n := len(r.Failures); n > 0 {
			p += fmt.Sprintf(" (%d failed)", n)
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "; ")
}

func (s *Service) End(ctx context.Context, id string, req EndRequest) (*EndOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	inc, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	out := &EndOutcome{}
	if inc.EndedAt != nil {
		out.Incident = *inc
		return out, nil
	}
	if inc.done(PauseAll) {
		if paused, _ := s.o.Control.Paused(); paused {
			if err := s.o.Control.ResumeAll(ctx); err != nil {
				return nil, err
			}
			out.ResumedAll = true
		}
	}
	if req.ResumeAgents {
		s.resumeAgents(ctx, out)
	}
	now := s.now()
	inc.EndedAt = &now
	err = s.o.Store.Atomic(ctx, func(tx store.Tx) error {
		if err := updateTx(tx, inc); err != nil {
			return err
		}
		return s.audit(ctx, tx, "incident_end", id,
			map[string]any{"id": id, "active": true},
			map[string]any{"id": id, "resumed_all": out.ResumedAll, "resumed_agents": len(out.ResumedAgents), "note": req.Note})
	})
	if err != nil {
		return nil, err
	}
	out.Incident = *inc
	return out, nil
}

func (s *Service) resumeAgents(ctx context.Context, out *EndOutcome) {
	agents, err := s.o.Control.ListAgents(ctx, "", model.AgentPaused)
	if err != nil {
		out.Failures = append(out.Failures, Failure{Error: err.Error()})
		return
	}
	for _, a := range agents {
		if a.Status != model.AgentPaused {
			continue
		}
		if _, err := s.o.Control.ResumeAgent(ctx, a.ID); err != nil {
			out.Failures = append(out.Failures, Failure{AgentID: a.ID, Error: err.Error()})
			continue
		}
		out.ResumedAgents = append(out.ResumedAgents, a.ID)
	}
}
