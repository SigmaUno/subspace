package board

import (
	"context"
	"fmt"
	"slices"
	"strings"

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
}

func (e TaskEdit) budgetOnly() bool {
	return e.Budget != nil && e.Title == nil && e.Spec == nil && e.AcceptanceCriteria == nil &&
		e.DependsOn == nil && e.Repo == nil && e.Role == nil
}

func (e TaskEdit) empty() bool {
	return e.Budget == nil && e.Title == nil && e.Spec == nil && e.AcceptanceCriteria == nil &&
		e.DependsOn == nil && e.Repo == nil && e.Role == nil
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
		before, after := map[string]any{}, map[string]any{}
		var changed []string
		set := func(field string, from, to any, differs bool) {
			if differs {
				before[field], after[field] = from, to
				changed = append(changed, field)
			}
		}
		if e.Title != nil {
			if strings.TrimSpace(*e.Title) == "" {
				return model.BadRequest("title must not be empty")
			}
			set("title", t.Title, *e.Title, t.Title != *e.Title)
			t.Title = *e.Title
		}
		if e.Spec != nil {
			if strings.TrimSpace(*e.Spec) == "" {
				return model.BadRequest("spec must not be empty")
			}
			set("spec", t.Spec, *e.Spec, t.Spec != *e.Spec)
			t.Spec = *e.Spec
		}
		if e.AcceptanceCriteria != nil {
			ac := nz(*e.AcceptanceCriteria)
			set("acceptance_criteria", t.AcceptanceCriteria, ac, !slices.Equal(t.AcceptanceCriteria, ac))
			t.AcceptanceCriteria = ac
		}
		if e.Repo != nil {
			if err := checkRepo(br, *e.Repo); err != nil {
				return err
			}
			set("repo", t.Repo, *e.Repo, t.Repo != *e.Repo)
			t.Repo = *e.Repo
		}
		if e.Role != nil {
			if *e.Role != model.RoleWorker && *e.Role != model.RoleTester {
				return model.BadRequest("task role must be worker or tester")
			}
			if t.Assignee != nil && *e.Role != t.Role {
				return model.Conflict("task %s is assigned to %s; role cannot change", t.ID, *t.Assignee)
			}
			set("role", t.Role, *e.Role, t.Role != *e.Role)
			t.Role = *e.Role
		}
		if e.Budget != nil {
			if e.Budget.MaxMinutes < 0 || e.Budget.MaxCostEUR < 0 {
				return model.BadRequest("budget must be >= 0")
			}
			set("budget", t.Budget, *e.Budget, t.Budget != *e.Budget)
			t.Budget = *e.Budget
		}
		if e.DependsOn != nil {
			deps, err := b.checkDeps(tx, t, *e.DependsOn)
			if err != nil {
				return err
			}
			set("depends_on", t.DependsOn, deps, !slices.Equal(t.DependsOn, deps))
			t.DependsOn = deps
		}
		if len(changed) == 0 {
			out = t
			return nil
		}
		if _, ok := after["title"]; ok {
			t.Branch = "sub/" + strings.ToLower(t.ID) + "-" + slug(t.Title)
		}
		t.UpdatedAt = b.now()
		if err := tx.UpdateTask(t); err != nil {
			return err
		}
		w.task(t, false)
		out = t
		msg := fmt.Sprintf("Owner edited %s: %s", t.ID, strings.Join(changed, ", "))
		if err := w.post(tx, model.ThreadBrief, t.BriefID, model.Owner, model.MsgSystem, msg); err != nil {
			return err
		}
		return w.audit(tx, model.Owner, "edit_task", t.ID, before, after, "")
	})
	return out, err
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
		return w.audit(tx, model.Owner, "add_plan_task", t.ID, nil, map[string]any{"plan_id": p.ID, "title": t.Title}, "")
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
		t.Status, t.BlockedReason, t.UpdatedAt = model.StatusCancelled, strp("dropped from plan"), b.now()
		if err := tx.UpdateTask(t); err != nil {
			return err
		}
		w.task(t, false)
		out = p
		msg := fmt.Sprintf("Owner dropped %s from plan %s: %s", t.ID, p.ID, t.Title)
		if err := w.post(tx, model.ThreadBrief, p.BriefID, model.Owner, model.MsgSystem, msg); err != nil {
			return err
		}
		return w.audit(tx, model.Owner, "drop_plan_task", t.ID, map[string]any{"status": model.StatusBacklog}, map[string]any{"status": model.StatusCancelled, "plan_id": p.ID}, "")
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
	all, err := tx.ListTasks(store.TaskFilter{BriefID: t.BriefID, Limit: 100000})
	if err != nil {
		return nil, err
	}
	graph := []*model.Task{{ID: t.ID, DependsOn: out}}
	for _, x := range liveTasks(all) {
		if x.ID != t.ID {
			graph = append(graph, x)
		}
	}
	if hasCycle(graph) {
		return nil, model.BadRequest("dependency cycle through %s", t.ID)
	}
	return out, nil
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
