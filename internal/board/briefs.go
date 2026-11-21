package board

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/SigmaUno/subspace/internal/backup"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/planlint"
	"github.com/SigmaUno/subspace/internal/store"
)

type StateInfo struct {
	Paused      bool           `json:"paused"`
	PauseReason *string        `json:"pause_reason"`
	Counts      map[string]any `json:"counts"`
	Version     string         `json:"version"`
}

var Version = "dev"

func (b *Board) State(ctx context.Context) (*StateInfo, error) {
	p, why := b.Paused()
	out := &StateInfo{Paused: p, Counts: map[string]any{}, Version: Version}
	if p {
		out.PauseReason = strp(why)
	}
	err := b.read(ctx, func(tx store.Tx) error {
		ag, err := tx.ListAgents(store.AgentFilter{Limit: 100000})
		if err != nil {
			return err
		}
		active := 0
		for _, a := range ag {
			if a.Status == model.AgentActive {
				active++
			}
		}
		tc, err := tx.CountTasksByStatus()
		if err != nil {
			return err
		}
		qs, err := tx.ListQuestions("open")
		if err != nil {
			return err
		}
		ps, err := tx.ListPlans("", model.PlanProposed)
		if err != nil {
			return err
		}
		out.Counts = map[string]any{"agents": len(ag), "agents_active": active, "tasks": tc, "open_questions": len(qs), "proposed_plans": len(ps)}
		return nil
	})
	return out, err
}

func (b *Board) PauseAll(ctx context.Context, reason string) error {
	if reason == "" {
		reason = "paused by owner"
	}
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		if err := tx.SetKV("paused", reason); err != nil {
			return err
		}
		ts, err := tx.ListTasks(store.TaskFilter{Status: []string{model.StatusInProgress}, Limit: 100000})
		if err != nil {
			return err
		}
		for _, t := range ts {
			if err := b.releaseLease(tx, w, model.Owner, t, model.StatusAssigned, "pause_all: lease revoked"); err != nil {
				return err
			}
		}
		w.emit([]string{"control"}, "control.paused", map[string]any{"reason": reason})
		return w.audit(tx, model.Owner, "pause_all", "", nil, map[string]any{"paused": true}, reason)
	})
	if err != nil {
		return err
	}
	b.pausedMu.Lock()
	b.paused, b.pauseWhy = true, reason
	b.pausedMu.Unlock()
	return nil
}

func (b *Board) ResumeAll(ctx context.Context) error {
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		if err := tx.SetKV("paused", ""); err != nil {
			return err
		}
		w.emit([]string{"control"}, "control.resumed", map[string]any{"reason": ""})
		return w.audit(tx, model.Owner, "resume_all", "", nil, map[string]any{"paused": false}, "")
	})
	if err != nil {
		return err
	}
	b.pausedMu.Lock()
	b.paused, b.pauseWhy = false, ""
	b.pausedMu.Unlock()
	return nil
}

type Charter struct {
	Text      string `json:"text"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

func (b *Board) GetCharter(ctx context.Context) (Charter, error) {
	var c Charter
	err := b.read(ctx, func(tx store.Tx) error {
		v, _, e := tx.GetKV("charter")
		c.Text = v
		u, _, _ := tx.GetKV("charter_at")
		c.UpdatedAt = u
		return e
	})
	return c, err
}

func (b *Board) SetCharter(ctx context.Context, text string) (Charter, error) {
	at := b.now().Format("2006-01-02T15:04:05Z")
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		if err := tx.SetKV("charter", text); err != nil {
			return err
		}
		if err := tx.SetKV("charter_at", at); err != nil {
			return err
		}
		return w.audit(tx, model.Owner, "edit_charter", "", nil, map[string]any{"bytes": len(text)}, "")
	})
	return Charter{Text: text, UpdatedAt: at}, err
}

func (b *Board) Backup(ctx context.Context) (string, int64, error) {
	if b.o.Backups == nil {
		return "", 0, model.BadRequest("backups are not configured")
	}
	info, err := b.o.Backups.RunOnce(ctx)
	if errors.Is(err, backup.ErrExists) {
		return "", 0, model.Errf("conflict", "a backup was already taken this second")
	}
	return info.Path, info.Bytes, err
}

func (b *Board) ListBackups(ctx context.Context) ([]backup.Info, error) {
	if b.o.Backups == nil {
		return nil, model.BadRequest("backups are not configured")
	}
	return b.o.Backups.List(ctx)
}

type BriefInput struct {
	Title            string   `json:"title"`
	Body             string   `json:"body"`
	Repos            []string `json:"repos"`
	MaxCostEUR       float64  `json:"max_cost_eur"`
	MaxParallel      int      `json:"max_parallel_agents"`
	AllowedProviders []string `json:"allowed_providers"`
	CrossReview      *bool    `json:"cross_review"`
}

func (b *Board) CreateBrief(ctx context.Context, in BriefInput) (*model.Brief, error) {
	if strings.TrimSpace(in.Title) == "" {
		return nil, model.BadRequest("title is required")
	}
	if in.MaxCostEUR < 0 || in.MaxParallel < 0 {
		return nil, model.BadRequest("budgets must be >= 0")
	}
	if in.MaxParallel == 0 {
		in.MaxParallel = 10
	}
	var out *model.Brief
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		id, err := tx.NextID("B")
		if err != nil {
			return err
		}
		br := &model.Brief{ID: id, Title: in.Title, Body: in.Body, Repos: nz(in.Repos), Status: "open", PlanStatus: "none",
			Budget:           model.BriefBudget{MaxCostEUR: in.MaxCostEUR, MaxParallelAgent: in.MaxParallel},
			AllowedProviders: nz(in.AllowedProviders), CrossReview: in.CrossReview == nil || *in.CrossReview, CreatedAt: b.now()}
		if err := tx.InsertBrief(br); err != nil {
			return err
		}
		out = br
		return w.audit(tx, model.Owner, "create_brief", "", nil, map[string]any{"brief_id": id}, in.Title)
	})
	return out, err
}

func (b *Board) ListBriefs(ctx context.Context, status string) ([]*model.Brief, error) {
	var out []*model.Brief
	err := b.read(ctx, func(tx store.Tx) error {
		bs, err := tx.ListBriefs(status)
		if err != nil {
			return err
		}
		for _, br := range bs {
			if br.CostUsed, err = tx.BriefCost(br.ID); err != nil {
				return err
			}
		}
		out = bs
		return nil
	})
	return nz(out), err
}

func (b *Board) GetBrief(ctx context.Context, id string) (*model.Brief, error) {
	var out *model.Brief
	err := b.read(ctx, func(tx store.Tx) (e error) {
		if out, e = tx.GetBrief(id); e != nil {
			return
		}
		out.CostUsed, e = tx.BriefCost(id)
		return
	})
	return out, err
}

type BriefPatch struct {
	MaxCostEUR       *float64  `json:"max_cost_eur"`
	MaxParallel      *int      `json:"max_parallel_agents"`
	AllowedProviders *[]string `json:"allowed_providers"`
	CrossReview      *bool     `json:"cross_review"`
	AutoDoneOnMerge  *bool     `json:"auto_done_on_merge"`
	Status           *string   `json:"status"`
}

func (b *Board) UpdateBrief(ctx context.Context, id string, p BriefPatch) (*model.Brief, error) {
	var out *model.Brief
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		br, err := tx.GetBrief(id)
		if err != nil {
			return err
		}
		var transition func(store.Tx, *wctx, *model.Brief) error
		if p.MaxCostEUR != nil {
			if *p.MaxCostEUR < 0 {
				return model.BadRequest("max_cost_eur must be >= 0")
			}
			br.Budget.MaxCostEUR = *p.MaxCostEUR
		}
		if p.MaxParallel != nil {
			if *p.MaxParallel < 0 {
				return model.BadRequest("max_parallel_agents must be >= 0")
			}
			br.Budget.MaxParallelAgent = *p.MaxParallel
		}
		if p.AllowedProviders != nil {
			br.AllowedProviders = nz(*p.AllowedProviders)
		}
		if p.CrossReview != nil {
			br.CrossReview = *p.CrossReview
		}
		if p.AutoDoneOnMerge != nil {
			br.AutoDoneOnMerge = *p.AutoDoneOnMerge
		}
		if p.Status != nil {
			if *p.Status != "open" && *p.Status != "closed" {
				return model.BadRequest("status must be open or closed")
			}
			prev := br.Status
			br.Status = *p.Status
			if prev != br.Status {
				transition = b.closeBrief
				if br.Status == "open" {
					transition = b.reopenBrief
				}
			}
		}
		if err := tx.UpdateBrief(br); err != nil {
			return err
		}
		if transition != nil {
			if err := transition(tx, w, br); err != nil {
				return err
			}
		}
		if br.CostUsed, err = tx.BriefCost(id); err != nil {
			return err
		}
		out = br
		return w.audit(tx, model.Owner, "set_budget", "", nil, map[string]any{"brief_id": id}, "")
	})
	return out, err
}

type BoardView struct {
	Brief   *model.Brief             `json:"brief,omitempty"`
	Columns map[string][]*model.Task `json:"columns"`
}

func (b *Board) Board(ctx context.Context, briefID string) (*BoardView, error) {
	v := &BoardView{Columns: map[string][]*model.Task{}}
	for _, s := range model.AllStatuses {
		v.Columns[s] = []*model.Task{}
	}
	err := b.read(ctx, func(tx store.Tx) error {
		if briefID != "" {
			br, err := tx.GetBrief(briefID)
			if err != nil {
				return err
			}
			if br.CostUsed, err = tx.BriefCost(briefID); err != nil {
				return err
			}
			v.Brief = br
		}
		ts, err := tx.ListTasks(store.TaskFilter{BriefID: briefID, Limit: 100000})
		if err != nil {
			return err
		}
		for _, t := range ts {
			v.Columns[t.Status] = append(v.Columns[t.Status], b.overlay(t))
		}
		return nil
	})
	return v, err
}

type TaskInput struct {
	Ref                string       `json:"ref"`
	BriefID            string       `json:"brief_id"`
	Title              string       `json:"title"`
	Spec               string       `json:"spec"`
	AcceptanceCriteria []string     `json:"acceptance_criteria"`
	Repo               string       `json:"repo"`
	DependsOn          []string     `json:"depends_on"`
	Role               string       `json:"role"`
	Budget             model.Budget `json:"budget"`
	Priority           int          `json:"priority"`
	ComponentID        string       `json:"component_id"`
}

type PlanInput struct {
	BriefID string      `json:"brief_id"`
	Note    string      `json:"note"`
	Tasks   []TaskInput `json:"tasks"`
}

const maxPlanTasks = 500

func (b *Board) newTask(tx store.Tx, br *model.Brief, in TaskInput, planID, status string, refs map[string]string) (*model.Task, error) {
	if strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Spec) == "" {
		return nil, model.BadRequest("task title and spec are required")
	}
	role := in.Role
	if role == "" {
		role = model.RoleWorker
	}
	if role != model.RoleWorker && role != model.RoleTester {
		return nil, model.BadRequest("task role must be worker or tester")
	}
	if len(br.Repos) > 0 && !contains(br.Repos, in.Repo) {
		return nil, model.BadRequest("repo %q is not part of brief %s", in.Repo, br.ID)
	}
	if in.Repo == "" {
		return nil, model.BadRequest("repo is required")
	}
	if in.Budget.MaxMinutes < 0 || in.Budget.MaxCostEUR < 0 {
		return nil, model.BadRequest("budget must be >= 0")
	}
	id, err := tx.NextID("T")
	if err != nil {
		return nil, err
	}
	deps := []string{}
	for _, d := range in.DependsOn {
		if r, ok := refs[d]; ok {
			d = r
		} else if _, err := tx.GetTask(d); err != nil {
			return nil, model.BadRequest("unknown dependency %q", d)
		}
		deps = append(deps, d)
	}
	now := b.now()
	return &model.Task{ID: id, BriefID: br.ID, PlanID: planID, Title: in.Title, Spec: in.Spec,
		AcceptanceCriteria: nz(in.AcceptanceCriteria), Repo: in.Repo, Branch: "sub/" + strings.ToLower(id) + "-" + slug(in.Title),
		Role: role, DependsOn: deps, Status: status, Budget: in.Budget, Priority: in.Priority, ComponentID: strp(in.ComponentID),
		CreatedAt: now, UpdatedAt: now}, nil
}

func (b *Board) ProposePlan(ctx context.Context, a model.Actor, in PlanInput) (*model.Plan, error) {
	if len(in.Tasks) == 0 || len(in.Tasks) > maxPlanTasks {
		return nil, model.BadRequest("a plan needs 1..%d tasks", maxPlanTasks)
	}
	var out *model.Plan
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		br, err := tx.GetBrief(in.BriefID)
		if err != nil {
			return err
		}
		if br.Status != "open" {
			return model.Conflict("brief %s is closed", br.ID)
		}
		errs, warns := planlint.Split(planlint.Lint(*br, lintTasks(in.Tasks)))
		if len(errs) > 0 {
			return model.BadRequest("plan rejected by lint: %s", planlint.Format(errs))
		}
		old, err := tx.ListPlans(br.ID, model.PlanProposed)
		if err != nil {
			return err
		}
		superseded := []string{}
		for _, p := range old {
			ts, err := tx.ListTasks(store.TaskFilter{PlanID: p.ID, Limit: 100000})
			if err != nil {
				return err
			}
			for _, t := range ts {
				if t.Status != model.StatusCancelled {
					superseded = append(superseded, t.ID)
				}
				t.Status, t.UpdatedAt = model.StatusCancelled, b.now()
				if err := tx.UpdateTask(t); err != nil {
					return err
				}
				w.task(t, false)
			}
			p.Status = model.PlanSuperseded
			if err := tx.UpdatePlan(p); err != nil {
				return err
			}
		}
		pid, err := tx.NextID("P")
		if err != nil {
			return err
		}
		p := &model.Plan{ID: pid, BriefID: br.ID, Status: model.PlanProposed, ProposedBy: a.ID, Note: strp(in.Note), Warnings: nz(warns), CreatedAt: b.now()}
		if err := tx.InsertPlan(p); err != nil {
			return err
		}
		refs := map[string]string{}

		var tasks []*model.Task
		for _, ti := range in.Tasks {
			t, err := b.newTask(tx, br, TaskInput{Title: ti.Title, Spec: ti.Spec, Repo: ti.Repo, Role: ti.Role, Budget: ti.Budget, AcceptanceCriteria: ti.AcceptanceCriteria, Priority: ti.Priority, ComponentID: ti.ComponentID}, pid, model.StatusBacklog, nil)
			if err != nil {
				return err
			}
			if ti.Ref != "" {
				refs[ti.Ref] = t.ID
			}
			tasks = append(tasks, t)
		}
		for i, ti := range in.Tasks {
			for _, d := range ti.DependsOn {
				if r, ok := refs[d]; ok {
					d = r
				} else if _, err := tx.GetTask(d); err != nil {
					return model.BadRequest("unknown dependency %q", d)
				}
				if d == tasks[i].ID {
					return model.BadRequest("task cannot depend on itself")
				}
				tasks[i].DependsOn = append(tasks[i].DependsOn, d)
			}
		}
		ids := make([]string, len(tasks))
		for i, t := range tasks {
			if err := tx.InsertTask(t); err != nil {
				return err
			}
			w.task(t, true)
			ids[i] = t.ID
		}
		p.Tasks = tasks
		br.PlanStatus = model.PlanProposed
		if err := tx.UpdateBrief(br); err != nil {
			return err
		}
		out = p
		cp := *p
		w.emit([]string{"plans", "brief:" + br.ID}, "plan.proposed", &cp)
		if err := w.post(tx, model.ThreadBrief, br.ID, a, model.MsgSystem, fmt.Sprintf("Plan %s proposed with %d tasks", pid, len(tasks))); err != nil {
			return err
		}
		return w.audit(tx, a, "propose_plan", "", nil, map[string]any{"plan_id": pid, "tasks": len(tasks), "task_ids": ids, "superseded": superseded}, in.Note)
	})
	return out, err
}

func lintTasks(in []TaskInput) []planlint.Task {
	out := make([]planlint.Task, len(in))
	for i, t := range in {
		out[i] = planlint.Task{Ref: t.Ref, Title: t.Title, Spec: t.Spec, AcceptanceCriteria: t.AcceptanceCriteria,
			Repo: t.Repo, Role: t.Role, DependsOn: t.DependsOn, Budget: t.Budget}
	}
	return out
}

func (b *Board) loadPlan(tx store.Tx, id string) (*model.Plan, error) {
	p, err := tx.GetPlan(id)
	if err != nil {
		return nil, err
	}
	ts, err := tx.ListTasks(store.TaskFilter{PlanID: id, Limit: 100000})
	if err != nil {
		return nil, err
	}
	p.Tasks = nz(ts)
	p.Warnings = nz(p.Warnings)
	return p, nil
}

func (b *Board) ListPlans(ctx context.Context, briefID, status string) ([]*model.Plan, error) {
	var out []*model.Plan
	err := b.read(ctx, func(tx store.Tx) error {
		ps, err := tx.ListPlans(briefID, status)
		if err != nil {
			return err
		}
		for _, p := range ps {
			if p, err = b.loadPlan(tx, p.ID); err != nil {
				return err
			}
			out = append(out, p)
		}
		return nil
	})
	return nz(out), err
}

func (b *Board) GetPlan(ctx context.Context, id string) (*model.Plan, error) {
	var out *model.Plan
	err := b.read(ctx, func(tx store.Tx) (e error) { out, e = b.loadPlan(tx, id); return })
	return out, err
}

func (b *Board) ApprovePlan(ctx context.Context, id, note string) (*model.Plan, error) {
	return b.decidePlan(ctx, id, true, note)
}

func (b *Board) RejectPlan(ctx context.Context, id, reason string) (*model.Plan, error) {
	return b.decidePlan(ctx, id, false, reason)
}

func (b *Board) decidePlan(ctx context.Context, id string, approve bool, note string) (*model.Plan, error) {
	var out *model.Plan
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		p, err := b.loadPlan(tx, id)
		if err != nil {
			return err
		}
		if p.Status != model.PlanProposed {
			return model.Conflict("plan %s is %s, not proposed", id, p.Status)
		}
		br, err := tx.GetBrief(p.BriefID)
		if err != nil {
			return err
		}
		now := b.now()
		p.DecidedAt = &now
		p.Note = strp(note)
		target, typ, tool := model.StatusReady, "plan.approved", "approve_plan"
		p.Status = model.PlanApproved
		br.PlanStatus = model.PlanApproved
		if !approve {
			target, typ, tool = model.StatusCancelled, "plan.rejected", "reject_plan"
			p.Status = model.PlanRejected
			br.PlanStatus = model.PlanRejected
		}
		moved := []string{}
		for _, t := range p.Tasks {
			if t.Status != model.StatusBacklog {
				continue
			}
			moved = append(moved, t.ID)
			t.Status, t.UpdatedAt = target, now
			if !approve {
				t.BlockedReason = strp("plan rejected")
			}
			if err := tx.UpdateTask(t); err != nil {
				return err
			}
			w.task(t, false)
		}
		if err := tx.UpdatePlan(p); err != nil {
			return err
		}
		if err := tx.UpdateBrief(br); err != nil {
			return err
		}
		cp := *p
		w.emit([]string{"plans", "brief:" + br.ID}, typ, &cp)
		msg := fmt.Sprintf("Plan %s %s", id, p.Status)
		if note != "" {
			msg += ": " + note
		}
		if err := w.post(tx, model.ThreadBrief, br.ID, model.Owner, model.MsgSystem, msg); err != nil {
			return err
		}
		out = p
		return w.audit(tx, model.Owner, tool, "", nil, map[string]any{"plan_id": id, "status": p.Status, "task_ids": moved}, note)
	})
	return out, err
}
