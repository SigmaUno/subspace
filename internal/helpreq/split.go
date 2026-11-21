package helpreq

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	minSplitTasks = 2
	maxSplitTasks = 20
)

func normalize(orig *model.Task, br *model.Brief, in []Proposed) ([]Proposed, error) {
	if len(in) < minSplitTasks || len(in) > maxSplitTasks {
		return nil, model.BadRequest("proposed_tasks needs %d to %d tasks", minSplitTasks, maxSplitTasks)
	}
	out := make([]Proposed, len(in))
	refs := map[string]bool{}
	for i, p := range in {
		p.Title, p.Spec = strings.TrimSpace(p.Title), strings.TrimSpace(p.Spec)
		if p.Title == "" || p.Spec == "" {
			return nil, model.BadRequest("proposed task %d needs a title and a spec", i+1)
		}
		if len(p.Title) > 200 || len(p.Spec) > 20000 {
			return nil, model.BadRequest("proposed task %d is too long", i+1)
		}
		if p.Repo == "" {
			p.Repo = orig.Repo
		}
		if len(br.Repos) > 0 && !slices.Contains(br.Repos, p.Repo) {
			return nil, model.BadRequest("repo %q is not part of brief %s", p.Repo, br.ID)
		}
		if p.Role == "" {
			p.Role = model.RoleWorker
		}
		if p.Role != model.RoleWorker && p.Role != model.RoleTester {
			return nil, model.BadRequest("proposed task %d role must be worker or tester", i+1)
		}
		if p.Budget.MaxMinutes < 0 || p.Budget.MaxCostEUR < 0 {
			return nil, model.BadRequest("budget must be >= 0")
		}
		if p.Ref != "" {
			if refs[p.Ref] {
				return nil, model.BadRequest("duplicate ref %q", p.Ref)
			}
			refs[p.Ref] = true
		}
		p.Title, p.Spec = board.Redact(p.Title), board.Redact(p.Spec)
		p.AcceptanceCriteria, p.DependsOn = nzStrings(p.AcceptanceCriteria), nzStrings(p.DependsOn)
		out[i] = p
	}
	for i, p := range out {
		for _, d := range p.DependsOn {
			if d == orig.ID {
				return nil, model.BadRequest("proposed task %d cannot depend on the task being split", i+1)
			}
		}
	}
	if _, err := order(out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) checkDeps(ctx context.Context, orig *model.Task, ps []Proposed) error {
	all, err := s.b.ListTasks(ctx, board.TaskQuery{BriefID: orig.BriefID, Limit: 100000})
	if err != nil {
		return err
	}
	byID := make(map[string]*model.Task, len(all))
	for _, t := range all {
		byID[t.ID] = t
	}
	refs := map[string]bool{}
	for _, p := range ps {
		if p.Ref != "" {
			refs[p.Ref] = true
		}
	}
	for _, p := range ps {
		for _, d := range p.DependsOn {
			if refs[d] {
				continue
			}
			t, ok := byID[d]
			if !ok || t.Status == model.StatusCancelled {
				return model.BadRequest("unknown dependency %q", d)
			}
			if dependsOn(byID, t, orig.ID, map[string]bool{}) {
				return model.BadRequest("dependency %q depends on the task being split", d)
			}
		}
	}
	return nil
}

func dependsOn(byID map[string]*model.Task, t *model.Task, target string, seen map[string]bool) bool {
	if seen[t.ID] {
		return false
	}
	seen[t.ID] = true
	for _, d := range t.DependsOn {
		if d == target {
			return true
		}
		if n, ok := byID[d]; ok && dependsOn(byID, n, target, seen) {
			return true
		}
	}
	return false
}

func order(ps []Proposed) ([]int, error) {
	byRef := map[string]int{}
	for i, p := range ps {
		if p.Ref != "" {
			byRef[p.Ref] = i
		}
	}
	const (
		unseen = iota
		active
		done
	)
	state := make([]int, len(ps))
	var out []int
	var visit func(i int) error
	visit = func(i int) error {
		switch state[i] {
		case done:
			return nil
		case active:
			return model.BadRequest("proposed tasks have a dependency cycle")
		}
		state[i] = active
		for _, d := range ps[i].DependsOn {
			if j, ok := byRef[d]; ok {
				if err := visit(j); err != nil {
					return err
				}
			}
		}
		state[i] = done
		out = append(out, i)
		return nil
	}
	for i := range ps {
		if err := visit(i); err != nil {
			return nil, err
		}
	}
	return out, nil
}

type SplitResult struct {
	Request  *Request      `json:"request"`
	Tasks    []*model.Task `json:"tasks"`
	Original *model.Task   `json:"original"`
	Rewired  []string      `json:"rewired"`
}

func (s *Service) AcceptSplit(ctx context.Context, a model.Actor, id string) (*SplitResult, error) {
	var r *Request
	if err := s.view(ctx, func(_ store.Tx, q sqlTx) (err error) { r, err = get(q, id); return }); err != nil {
		return nil, err
	}
	if ok, err := s.b.BriefVisible(ctx, a, r.BriefID); err != nil {
		return nil, err
	} else if !ok {
		return nil, model.NotFound("help request %s not found", id)
	}
	if r.Type != TypeSplit {
		return nil, model.BadRequest("%s is not a split proposal", id)
	}
	orig, err := s.b.GetTask(ctx, a, r.TaskID)
	if err != nil {
		return nil, err
	}
	if r.Status != StatusOpen {
		return nil, model.Conflict("split %s is %s", id, r.Status)
	}
	if terminal(orig) {
		return nil, model.Conflict("task %s is %s", orig.ID, orig.Status)
	}
	if err := s.checkDeps(ctx, orig, r.ProposedTasks); err != nil {
		return nil, err
	}
	if err := s.atomic(ctx, func(_ store.Tx, q sqlTx) error { return setStatus(q, id, StatusOpen, StatusAccepted) }); err != nil {
		return nil, err
	}
	res, err := s.apply(ctx, a, orig, r)
	if err != nil {
		reopenErr := s.atomic(ctx, func(_ store.Tx, q sqlTx) error { return setStatus(q, id, StatusAccepted, StatusOpen) })
		if reopenErr != nil {
			return nil, fmt.Errorf("%w (and reopening the proposal failed: %v)", err, reopenErr)
		}
		return nil, err
	}
	ids := make([]string, len(res.Tasks))
	for i, t := range res.Tasks {
		ids[i] = t.ID
	}
	r.Status, r.CreatedTasks = StatusAccepted, ids
	res.Request = r
	err = s.atomic(ctx, func(tx store.Tx, q sqlTx) error {
		if _, err := q.Exec(`UPDATE help_requests SET created_tasks=? WHERE id=?`, marshal(ids), id); err != nil {
			return err
		}
		return s.audit(tx, a, "accept_split", orig.ID, map[string]any{"status": orig.Status},
			map[string]any{"request_id": id, "created": ids, "status": model.StatusCancelled}, "")
	})
	if err != nil {
		return nil, err
	}
	body := fmt.Sprintf("[%s] split accepted: %s replaced by %s", id, orig.ID, strings.Join(ids, ", "))
	return res, s.announce(ctx, a, orig, body)
}

func (s *Service) apply(ctx context.Context, a model.Actor, orig *model.Task, r *Request) (*SplitResult, error) {
	ps := r.ProposedTasks
	seq, err := order(ps)
	if err != nil {
		return nil, err
	}
	created := make([]*model.Task, len(ps))
	rollback := func(cause error) error {
		cancelled := model.StatusCancelled
		for _, t := range created {
			if t != nil {
				_, _ = s.b.UpdateTask(ctx, a, t.ID, board.TaskPatch{Status: &cancelled})
			}
		}
		return cause
	}
	byRef := map[string]string{}
	depended := map[string]bool{}
	for _, i := range seq {
		p := ps[i]
		deps, inner := []string{}, false
		for _, d := range p.DependsOn {
			if id, ok := byRef[d]; ok {
				d, inner = id, true
				depended[id] = true
			}
			if !slices.Contains(deps, d) {
				deps = append(deps, d)
			}
		}
		if !inner {
			for _, d := range orig.DependsOn {
				if !slices.Contains(deps, d) {
					deps = append(deps, d)
				}
			}
		}
		t, err := s.b.CreateTask(ctx, a, board.TaskInput{
			BriefID: orig.BriefID, Title: p.Title, Spec: p.Spec, AcceptanceCriteria: p.AcceptanceCriteria, Repo: p.Repo,
			DependsOn: deps, Role: p.Role, Budget: p.Budget, Priority: p.Priority, ComponentID: p.ComponentID,
		})
		if err != nil {
			return nil, rollback(err)
		}
		created[i] = t
		if p.Ref != "" {
			byRef[p.Ref] = t.ID
		}
	}
	var leaves []string
	for _, t := range created {
		if !depended[t.ID] {
			leaves = append(leaves, t.ID)
		}
	}
	rewired, err := s.rewire(ctx, a, orig, created, leaves)
	if err != nil {
		return nil, rollback(err)
	}
	cancelled := model.StatusCancelled
	if _, err := s.b.UpdateTask(ctx, a, orig.ID, board.TaskPatch{Status: &cancelled}); err != nil {
		return nil, rollback(err)
	}
	final, err := s.b.GetTask(ctx, a, orig.ID)
	if err != nil {
		return nil, err
	}
	return &SplitResult{Tasks: created, Original: final, Rewired: nzStrings(rewired)}, nil
}

func (s *Service) rewire(ctx context.Context, a model.Actor, orig *model.Task, created []*model.Task, leaves []string) ([]string, error) {
	all, err := s.b.ListTasks(ctx, board.TaskQuery{BriefID: orig.BriefID, Limit: 100000})
	if err != nil {
		return nil, err
	}
	fresh := map[string]bool{}
	for _, t := range created {
		fresh[t.ID] = true
	}
	var out []string
	for _, t := range all {
		if fresh[t.ID] || terminal(t) || !slices.Contains(t.DependsOn, orig.ID) {
			continue
		}
		deps := []string{}
		for _, d := range t.DependsOn {
			next := []string{d}
			if d == orig.ID {
				next = leaves
			}
			for _, n := range next {
				if !slices.Contains(deps, n) {
					deps = append(deps, n)
				}
			}
		}
		if _, err := s.b.UpdateTask(ctx, a, t.ID, board.TaskPatch{DependsOn: &deps}); err != nil {
			return out, err
		}
		out = append(out, t.ID)
	}
	return out, nil
}
