package policy

import (
	"context"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

type DryRunInput struct {
	Action  string         `json:"action"`
	ActorID string         `json:"actor_id"`
	TaskID  string         `json:"task_id"`
	AgentID string         `json:"agent_id"`
	BriefID string         `json:"brief_id"`
	PlanID  string         `json:"plan_id"`
	As      string         `json:"as"`
	Spawn   *Spawn         `json:"spawn"`
	At      *time.Time     `json:"at"`
	Facts   map[string]any `json:"facts"`
	Policy  *Document      `json:"policy"`
}

type DryRunResult struct {
	Result
	Action   string    `json:"action"`
	At       time.Time `json:"at"`
	Findings []Finding `json:"findings"`
}

func (s *Service) DryRun(ctx context.Context, in DryRunInput) (*DryRunResult, error) {
	if !containsStr(Actions, in.Action) {
		return nil, model.BadRequest("action must be one of %v", Actions)
	}
	overrides := map[string]Value{}
	for name, raw := range in.Facts {
		info, ok := factIndex[name]
		if !ok {
			return nil, model.BadRequest("unknown fact %q", name)
		}
		v, ok := FromAny(info.typ, raw)
		if !ok {
			return nil, model.BadRequest("fact %q must be a %s", name, info.Type)
		}
		overrides[name] = v
	}
	out := &DryRunResult{Action: in.Action, At: s.now().UTC(), Findings: []Finding{}}
	if in.At != nil {
		out.At = in.At.UTC()
	}
	var draft *Compiled
	if in.Policy != nil {
		d := *in.Policy
		d.Enabled = true
		var fs []Finding
		draft, fs = compile("draft", 0, d)
		out.Findings = fs
		if draft == nil {
			return nil, &model.Error{Code: "bad_request", Message: "the policy has errors", Details: map[string]any{"findings": fs}}
		}
	}
	err := s.st.View(ctx, func(tx store.Tx) error {
		req, err := sampleRequest(tx, in)
		if err != nil {
			return err
		}
		req.Now = out.At
		set := []*Compiled{draft}
		if draft == nil {
			if set, err = s.cache.current(tx); err != nil {
				return err
			}
		}
		rv := newResolver(tx, req)
		out.Result = *Decide(set, in.Action, rv, out.At, overrides)
		return rv.err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func sampleRequest(tx store.Tx, in DryRunInput) (*Request, error) {
	req := &Request{Action: in.Action, Actor: model.Owner, As: in.As, Args: map[string]string{}}
	if in.ActorID != "" && in.ActorID != model.Owner.ID {
		a, err := tx.GetAgent(in.ActorID)
		if err != nil {
			return nil, model.NotFound("agent %s not found", in.ActorID)
		}
		req.Actor = model.Actor{ID: a.ID, Role: a.Role, Kind: a.Kind}
	}
	if in.TaskID != "" {
		t, err := tx.GetTask(in.TaskID)
		if err != nil {
			return nil, model.NotFound("task %s not found", in.TaskID)
		}
		req.Task = t
		if in.BriefID == "" {
			in.BriefID = t.BriefID
		}
	}
	if in.PlanID != "" {
		p, err := tx.GetPlan(in.PlanID)
		if err != nil {
			return nil, model.NotFound("plan %s not found", in.PlanID)
		}
		if p.Tasks, err = tx.ListTasks(store.TaskFilter{PlanID: p.ID, Limit: maxScannedTasks}); err != nil {
			return nil, err
		}
		req.Plan = p
		if in.BriefID == "" {
			in.BriefID = p.BriefID
		}
	}
	if in.BriefID != "" {
		br, err := tx.GetBrief(in.BriefID)
		if err != nil {
			return nil, model.NotFound("brief %s not found", in.BriefID)
		}
		req.Brief = br
	}
	if in.AgentID != "" {
		a, err := tx.GetAgent(in.AgentID)
		if err != nil {
			return nil, model.NotFound("agent %s not found", in.AgentID)
		}
		req.Target = a
		if req.As == "" {
			req.As = "assignee"
		}
	}
	if in.Spawn != nil {
		sp := *in.Spawn
		if sp.Provider == "" {
			sp.Provider = model.ProviderForKind(sp.Kind)
		}
		if sp.Role == "" {
			sp.Role = model.RoleWorker
		}
		if sp.N == 0 {
			sp.N = 1
		}
		req.Spawn = &sp
	}
	return req, nil
}
