package board

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/SigmaUno/subspace/internal/conflict"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

type TaskEdit struct {
	Title              *string       `json:"title"`
	Spec               *string       `json:"spec"`
	AcceptanceCriteria *[]string     `json:"acceptance_criteria"`
	DependsOn          *[]string     `json:"depends_on"`
	Repo               *string       `json:"repo"`
	Role               *string       `json:"role"`
	Budget             *model.Budget `json:"budget"`
	Paths              *[]string     `json:"paths"`
}

func (e TaskEdit) budgetOnly() bool {
	rest := e
	rest.Budget = nil
	return e.Budget != nil && rest.empty()
}

func (e TaskEdit) empty() bool {
	return e == TaskEdit{}
}

type changeSet struct {
	before, after map[string]any
	fields        []string
}

func (c *changeSet) set(field string, from, to any, differs bool) {
	if differs {
		c.before[field], c.after[field] = from, to
		c.fields = append(c.fields, field)
	}
}

func (b *Board) EditTask(ctx context.Context, id string, e TaskEdit) (*model.Task, error) {
	if e.empty() {
		return nil, model.BadRequest("no editable fields in request")
	}
	if e.budgetOnly() {
		return b.SetTaskBudget(ctx, id, *e.Budget)
	}
	var out *model.Task
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		t, err := tx.GetTask(id)
		if err != nil {
			return err
		}
		if t.Status != model.StatusBacklog && t.Status != model.StatusReady {
			return model.Conflict("task is %s; only backlog or ready tasks can be edited", t.Status)
		}
		br, err := tx.GetBrief(t.BriefID)
		if err != nil {
			return err
		}
		c, err := b.applyEdit(tx, br, t, e)
		if err != nil {
			return err
		}
		if len(c.fields) == 0 {
			out = t
			return nil
		}
		if _, ok := c.after["title"]; ok {
			t.Branch = "sub/" + strings.ToLower(t.ID) + "-" + slug(t.Title)
			if err := b.checkRegistry(tx, t.Repo, t.Branch); err != nil {
				return err
			}
		}
		if err := b.saveTask(tx, w, t); err != nil {
			return err
		}
		out = t
		msg := fmt.Sprintf("Owner edited %s: %s", t.ID, strings.Join(c.fields, ", "))
		if err := w.post(tx, model.ThreadBrief, t.BriefID, model.Owner, model.MsgSystem, msg); err != nil {
			return err
		}
		return w.audit(tx, model.Owner, "edit_task", t.ID, c.before, c.after, "")
	})
	return out, err
}

func (b *Board) applyEdit(tx store.Tx, br *model.Brief, t *model.Task, e TaskEdit) (*changeSet, error) {
	c := &changeSet{before: map[string]any{}, after: map[string]any{}}
	if e.Title != nil {
		if strings.TrimSpace(*e.Title) == "" {
			return nil, model.BadRequest("title must not be empty")
		}
		c.set("title", t.Title, *e.Title, t.Title != *e.Title)
		t.Title = *e.Title
	}
	if e.Spec != nil {
		if strings.TrimSpace(*e.Spec) == "" {
			return nil, model.BadRequest("spec must not be empty")
		}
		c.set("spec", t.Spec, *e.Spec, t.Spec != *e.Spec)
		t.Spec = *e.Spec
	}
	if e.AcceptanceCriteria != nil {
		ac := nz(*e.AcceptanceCriteria)
		c.set("acceptance_criteria", t.AcceptanceCriteria, ac, !slices.Equal(t.AcceptanceCriteria, ac))
		t.AcceptanceCriteria = ac
	}
	if e.Repo != nil {
		if err := checkRepo(br, *e.Repo); err != nil {
			return nil, err
		}
		if err := b.checkRegistry(tx, *e.Repo, t.Branch); err != nil {
			return nil, err
		}
		c.set("repo", t.Repo, *e.Repo, t.Repo != *e.Repo)
		t.Repo = *e.Repo
	}
	if e.Role != nil {
		if *e.Role != model.RoleWorker && *e.Role != model.RoleTester {
			return nil, model.BadRequest("task role must be worker or tester")
		}
		if t.Assignee != nil && *e.Role != t.Role {
			return nil, model.Conflict("task %s is assigned to %s; role cannot change", t.ID, *t.Assignee)
		}
		c.set("role", t.Role, *e.Role, t.Role != *e.Role)
		t.Role = *e.Role
	}
	if e.Budget != nil {
		if e.Budget.MaxMinutes < 0 || e.Budget.MaxCostEUR < 0 {
			return nil, model.BadRequest("budget must be >= 0")
		}
		c.set("budget", t.Budget, *e.Budget, t.Budget != *e.Budget)
		t.Budget = *e.Budget
	}
	if e.Paths != nil {
		ps, err := conflict.NormalizeAll(*e.Paths)
		if err != nil {
			return nil, model.BadRequest("paths: %v", err)
		}
		np := model.Paths(ps)
		if len(np) == 0 {
			np = nil
		}
		c.set("paths", t.Paths, np, !slices.Equal(t.Paths, np))
		t.Paths = np
	}
	if e.DependsOn != nil {
		deps, err := b.checkDeps(tx, t, *e.DependsOn)
		if err != nil {
			return nil, err
		}
		c.set("depends_on", t.DependsOn, deps, !slices.Equal(t.DependsOn, deps))
		t.DependsOn = deps
	}
	return c, nil
}

func (b *Board) AddPlanTask(ctx context.Context, planID string, in TaskInput) (*model.Plan, error) {
	var out *model.Plan
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		p, err := b.loadPlan(tx, planID)
		if err != nil {
			return err
		}
		if p.Status != model.PlanProposed {
			return model.Conflict("plan %s is %s, not proposed", planID, p.Status)
		}
		if len(liveTasks(p.Tasks)) >= maxPlanTasks {
			return model.BadRequest("a plan holds at most %d tasks", maxPlanTasks)
		}
		br, err := tx.GetBrief(p.BriefID)
		if err != nil {
			return err
		}
		deps := in.DependsOn
		in.DependsOn = nil
		t, err := b.newTask(tx, br, in, p.ID, model.StatusBacklog, nil)
		if err != nil {
			return err
		}
		if t.DependsOn, err = b.checkDeps(tx, t, deps); err != nil {
			return err
		}
		if err := tx.InsertTask(t); err != nil {
			return err
		}
		w.task(t, true)
		p.Tasks = append(p.Tasks, t)
		out = p
		msg := fmt.Sprintf("Owner added %s to plan %s: %s", t.ID, p.ID, t.Title)
		if err := w.post(tx, model.ThreadBrief, br.ID, model.Owner, model.MsgSystem, msg); err != nil {
			return err
		}
		return w.audit(tx, model.Owner, "add_plan_task", t.ID, nil, map[string]any{"plan_id": p.ID, "title": t.Title, "state": replayState(t)}, "")
	})
	return out, err
}

func (b *Board) DropPlanTask(ctx context.Context, planID, taskID string) (*model.Plan, error) {
	var out *model.Plan
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		p, err := b.loadPlan(tx, planID)
		if err != nil {
			return err
		}
		if p.Status != model.PlanProposed {
			return model.Conflict("plan %s is %s, not proposed", planID, p.Status)
		}
		var t *model.Task
		for _, x := range p.Tasks {
			if x.ID == taskID {
				t = x
			}
		}
		if t == nil {
			return model.NotFound("task %s is not part of plan %s", taskID, planID)
		}
		if t.Status != model.StatusBacklog {
			return model.Conflict("task is %s; only backlog tasks can be dropped", t.Status)
		}
		all, err := tx.ListTasks(store.TaskFilter{BriefID: p.BriefID, Limit: 100000})
		if err != nil {
			return err
		}
		var refs []string
		for _, x := range liveTasks(all) {
			if x.ID != t.ID && contains(x.DependsOn, t.ID) {
				refs = append(refs, x.ID)
			}
		}
		if len(refs) > 0 {
			return model.Conflict("task %s is still a dependency of %s", t.ID, strings.Join(refs, ", "))
		}
		t.Status, t.BlockedReason = model.StatusCancelled, strp("dropped from plan")
		if err := b.saveTask(tx, w, t); err != nil {
			return err
		}
		out = p
		msg := fmt.Sprintf("Owner dropped %s from plan %s: %s", t.ID, p.ID, t.Title)
		if err := w.post(tx, model.ThreadBrief, p.BriefID, model.Owner, model.MsgSystem, msg); err != nil {
			return err
		}
		return w.audit(tx, model.Owner, "drop_plan_task", t.ID, map[string]any{"status": model.StatusBacklog}, map[string]any{"status": model.StatusCancelled, "plan_id": p.ID, "state": replayState(t)}, "")
	})
	return out, err
}

func (b *Board) checkDeps(tx store.Tx, t *model.Task, deps []string) ([]string, error) {
	out := []string{}
	for _, d := range deps {
		if d == t.ID {
			return nil, model.BadRequest("task cannot depend on itself")
		}
		dep, err := tx.GetTask(d)
		if err != nil {
			return nil, model.BadRequest("unknown dependency %q", d)
		}
		if dep.BriefID != t.BriefID {
			return nil, model.BadRequest("dependency %s belongs to brief %s", d, dep.BriefID)
		}
		if dep.Status == model.StatusCancelled {
			return nil, model.BadRequest("dependency %s is cancelled", d)
		}
		if !contains(out, d) {
			out = append(out, d)
		}
	}
	cyc, err := reaches(tx, t, out)
	if err != nil {
		return nil, err
	}
	if cyc {
		return nil, model.BadRequest("dependency cycle through %s", t.ID)
	}
	return out, nil
}

func reaches(tx store.Tx, t *model.Task, from []string) (bool, error) {
	seen := map[string]bool{}
	stack := append([]string(nil), from...)
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if id == t.ID {
			return true, nil
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		x, err := tx.GetTask(id)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return false, err
		}
		if x.BriefID != t.BriefID || x.Status == model.StatusCancelled {
			continue
		}
		stack = append(stack, x.DependsOn...)
	}
	return false, nil
}

func checkRepo(br *model.Brief, repo string) error {
	if repo == "" {
		return model.BadRequest("repo is required")
	}
	if len(br.Repos) > 0 && !contains(br.Repos, repo) {
		return model.BadRequest("repo %q is not part of brief %s", repo, br.ID)
	}
	return nil
}

func liveTasks(ts []*model.Task) []*model.Task {
	var out []*model.Task
	for _, t := range ts {
		if t.Status != model.StatusCancelled {
			out = append(out, t)
		}
	}
	return out
}

func hasCycle(ts []*model.Task) bool {
	deps := map[string][]string{}
	for _, t := range ts {
		deps[t.ID] = t.DependsOn
	}
	state := map[string]int{}
	var visit func(string) bool
	visit = func(id string) bool {
		switch state[id] {
		case 1:
			return true
		case 2:
			return false
		}
		state[id] = 1
		for _, d := range deps[id] {
			if visit(d) {
				return true
			}
		}
		state[id] = 2
		return false
	}
	for id := range deps {
		if visit(id) {
			return true
		}
	}
	return false
}
