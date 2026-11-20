package board

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

func (b *Board) statusChange(tx store.Tx, w *wctx, actor model.Actor, tool string, t *model.Task, to, note string) error {
	from := t.Status
	t.Status, t.UpdatedAt = to, b.now()
	if err := tx.UpdateTask(t); err != nil {
		return err
	}
	w.task(t, false)
	msg := fmt.Sprintf("%s → %s", from, to)
	if note != "" {
		msg += ": " + note
	}
	if err := w.post(tx, model.ThreadTask, t.ID, actor, model.MsgStatus, msg); err != nil {
		return err
	}
	return w.audit(tx, actor, tool, t.ID, map[string]any{"status": from}, map[string]any{"status": to}, note)
}

func (b *Board) endLease(t *model.Task) {
	now := b.now()
	if t.ClaimedAt != nil {
		t.MinutesUsed += int(now.Sub(*t.ClaimedAt) / time.Minute)
	}
	t.ClaimedAt, t.LeaseExpiresAt = nil, nil
	b.forget(t.ID)
}

func (b *Board) releaseLease(tx store.Tx, w *wctx, actor model.Actor, t *model.Task, to, note string) error {
	b.endLease(t)
	return b.statusChange(tx, w, actor, "lease_release", t, to, note)
}

func (b *Board) block(tx store.Tx, w *wctx, actor model.Actor, t *model.Task, reason string) error {
	b.endLease(t)
	t.BlockedReason = strp(reason)
	return b.statusChange(tx, w, actor, "block", t, model.StatusBlocked, reason)
}

func canSee(a model.Actor, t *model.Task) bool {
	switch a.Role {
	case model.RoleOwner, model.RoleLeader:
		return true
	}
	return deref(t.Assignee) == a.ID || deref(t.Reviewer) == a.ID
}

func (b *Board) GetTask(ctx context.Context, a model.Actor, id string) (*model.Task, error) {
	var out *model.Task
	err := b.read(ctx, func(tx store.Tx) error {
		t, err := tx.GetTask(id)
		if err != nil {
			return err
		}
		if !canSee(a, t) {
			return model.NotFound("not found")
		}
		out = b.overlay(t)
		return nil
	})
	return out, err
}

type TaskQuery struct {
	BriefID, Status, Assignee, Role string
	Limit                           int
}

func (b *Board) ListTasks(ctx context.Context, q TaskQuery) ([]*model.Task, error) {
	f := store.TaskFilter{BriefID: q.BriefID, Assignee: q.Assignee, Role: q.Role, Limit: q.Limit}
	if q.Status != "" {
		f.Status = strings.Split(q.Status, ",")
	}
	var out []*model.Task
	err := b.read(ctx, func(tx store.Tx) error {
		ts, err := tx.ListTasks(f)
		for _, t := range ts {
			out = append(out, b.overlay(t))
		}
		return err
	})
	return nz(out), err
}

func (b *Board) ListMyTasks(ctx context.Context, a model.Actor, statuses []string) ([]*model.Task, error) {
	var out []*model.Task
	err := b.read(ctx, func(tx store.Tx) error {
		seen := map[string]bool{}
		for _, f := range []store.TaskFilter{{Assignee: a.ID, Status: statuses}, {Reviewer: a.ID, Status: statuses}} {
			ts, err := tx.ListTasks(f)
			if err != nil {
				return err
			}
			for _, t := range ts {
				if !seen[t.ID] {
					seen[t.ID] = true
					out = append(out, b.overlay(t))
				}
			}
		}
		return nil
	})
	return nz(out), err
}

func (b *Board) CreateTask(ctx context.Context, a model.Actor, in TaskInput) (*model.Task, error) {
	var out *model.Task
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		br, err := tx.GetBrief(in.BriefID)
		if err != nil {
			return err
		}
		if br.Status != "open" {
			return model.Conflict("brief %s is closed", br.ID)
		}
		status := model.StatusBacklog
		if br.PlanStatus == model.PlanApproved {
			status = model.StatusReady
		}
		t, err := b.newTask(tx, br, in, "", status, nil)
		if err != nil {
			return err
		}
		if err := tx.InsertTask(t); err != nil {
			return err
		}
		w.task(t, true)
		out = t
		return w.audit(tx, a, "create_task", t.ID, nil, map[string]any{"status": status}, in.Title)
	})
	return out, err
}

type TaskPatch struct {
	Title              *string       `json:"title"`
	Spec               *string       `json:"spec"`
	AcceptanceCriteria *[]string     `json:"acceptance_criteria"`
	DependsOn          *[]string     `json:"depends_on"`
	Budget             *model.Budget `json:"budget"`
	Status             *string       `json:"status"`
}

func (b *Board) UpdateTask(ctx context.Context, a model.Actor, id string, p TaskPatch) (*model.Task, error) {
	var out *model.Task
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		t, err := tx.GetTask(id)
		if err != nil {
			return err
		}
		switch t.Status {
		case model.StatusDone, model.StatusCancelled:
			return model.Conflict("task is %s", t.Status)
		}
		if p.Title != nil {
			t.Title = *p.Title
		}
		if p.Spec != nil {
			t.Spec = *p.Spec
		}
		if p.AcceptanceCriteria != nil {
			t.AcceptanceCriteria = nz(*p.AcceptanceCriteria)
		}
		if p.DependsOn != nil {
			for _, d := range *p.DependsOn {
				if d == id {
					return model.BadRequest("task cannot depend on itself")
				}
				if _, err := tx.GetTask(d); err != nil {
					return model.BadRequest("unknown dependency %q", d)
				}
			}
			t.DependsOn = nz(*p.DependsOn)
		}
		if p.Budget != nil {
			if p.Budget.MaxMinutes < 0 || p.Budget.MaxCostEUR < 0 {
				return model.BadRequest("budget must be >= 0")
			}
			t.Budget = *p.Budget
		}
		t.UpdatedAt = b.now()
		if p.Status != nil {
			switch *p.Status {
			case model.StatusBlocked:
				switch t.Status {
				case model.StatusReady, model.StatusAssigned, model.StatusInProgress:
				default:
					return model.Conflict("cannot block a %s task", t.Status)
				}
				if t.Status == model.StatusInProgress {
					b.endLease(t)
				}
				t.BlockedReason = strp("blocked by leader")
				return b.finishUpdate(tx, w, a, "update_task", t, model.StatusBlocked, &out)
			case model.StatusCancelled:
				if t.Status == model.StatusInProgress {
					b.endLease(t)
				}
				return b.finishUpdate(tx, w, a, "update_task", t, model.StatusCancelled, &out)
			case model.StatusReady:
				if t.Status != model.StatusBlocked && t.Status != model.StatusBacklog && t.Status != model.StatusReady {
					return model.Conflict("cannot make a %s task ready", t.Status)
				}
				to := model.StatusReady
				if t.Assignee != nil {
					to = model.StatusAssigned
				}
				if t.Status == model.StatusBacklog {

					return model.Forbidden("backlog tasks become ready through owner plan approval")
				}
				t.BlockedReason = nil
				return b.finishUpdate(tx, w, a, "update_task", t, to, &out)
			default:
				return model.BadRequest("status must be blocked, ready or cancelled")
			}
		}
		if err := tx.UpdateTask(t); err != nil {
			return err
		}
		w.task(t, false)
		out = t
		return w.audit(tx, a, "update_task", t.ID, nil, map[string]any{"title": t.Title}, "")
	})
	return out, err
}

func (b *Board) finishUpdate(tx store.Tx, w *wctx, a model.Actor, tool string, t *model.Task, to string, out **model.Task) error {
	if err := b.statusChange(tx, w, a, tool, t, to, ""); err != nil {
		return err
	}
	*out = t
	return nil
}

func (b *Board) providerAllowed(br *model.Brief, provider string) bool {
	return len(br.AllowedProviders) == 0 || contains(br.AllowedProviders, provider)
}

func (b *Board) Assign(ctx context.Context, a model.Actor, taskID, agentID, as string) (*model.Task, error) {
	return b.assign(ctx, a, "assign", taskID, agentID, as, "")
}

func (b *Board) Reassign(ctx context.Context, a model.Actor, taskID, agentID, reason string) (*model.Task, error) {
	return b.assign(ctx, a, "reassign", taskID, agentID, "assignee", reason)
}

func (b *Board) assign(ctx context.Context, a model.Actor, tool, taskID, agentID, as, reason string) (*model.Task, error) {
	if as == "" {
		as = "assignee"
	}
	if as != "assignee" && as != "reviewer" {
		return nil, model.BadRequest("as must be assignee or reviewer")
	}
	var out *model.Task
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		t, err := tx.GetTask(taskID)
		if err != nil {
			return err
		}
		ag, err := tx.GetAgent(agentID)
		if err != nil {
			return model.NotFound("agent %s not found", agentID)
		}
		if ag.Status != model.AgentActive {
			return model.Conflict("agent %s is %s", agentID, ag.Status)
		}
		br, err := tx.GetBrief(t.BriefID)
		if err != nil {
			return err
		}
		if !b.providerAllowed(br, ag.Provider) {
			return model.Forbidden("provider %q is not allowed for brief %s", ag.Provider, br.ID)
		}
		if as == "reviewer" {
			if tool == "reassign" {
				return model.BadRequest("reassign is for assignees")
			}
			if ag.Role != model.RoleReviewer {
				return model.BadRequest("agent %s is not a reviewer", agentID)
			}
			if t.Status != model.StatusSubmitted && t.Status != model.StatusInReview {
				return model.Conflict("task is %s; reviewer can be set on submitted tasks", t.Status)
			}
			if t.Assignee != nil {
				if wk, err := tx.GetAgent(*t.Assignee); err == nil && wk.Provider == ag.Provider {
					return model.Forbidden("reviewer must use a different provider than the worker (%s)", wk.Provider)
				}
			}
			t.Reviewer = &agentID
			return b.finishUpdate(tx, w, a, tool, t, model.StatusInReview, &out)
		}
		if ag.Role != t.Role {
			return model.BadRequest("task needs a %s, agent %s is a %s", t.Role, agentID, ag.Role)
		}
		from := t.Status
		if tool == "assign" {
			if t.Status != model.StatusReady {
				return model.Conflict("task is %s, only ready tasks can be assigned (use reassign)", t.Status)
			}
		} else {
			switch t.Status {
			case model.StatusReady, model.StatusAssigned, model.StatusInProgress, model.StatusChangesRequested, model.StatusBlocked:
			default:
				return model.Conflict("cannot reassign a %s task", t.Status)
			}
		}
		if t.Status == model.StatusInProgress {
			b.endLease(t)
		}
		t.Assignee, t.BlockedReason = &agentID, nil
		if from == model.StatusChangesRequested {

			t.UpdatedAt = b.now()
			if err := tx.UpdateTask(t); err != nil {
				return err
			}
			w.task(t, false)
			out = t
			if err := w.post(tx, model.ThreadTask, t.ID, a, model.MsgSystem, "reassigned to "+agentID); err != nil {
				return err
			}
			return w.audit(tx, a, tool, t.ID, map[string]any{"status": from}, map[string]any{"assignee": agentID}, reason)
		}
		if err := b.finishUpdate(tx, w, a, tool, t, model.StatusAssigned, &out); err != nil {
			return err
		}
		if err := w.post(tx, model.ThreadAgent, agentID, a, model.MsgSystem, fmt.Sprintf("Task %s assigned to you: %s", t.ID, t.Title)); err != nil {
			return err
		}
		return nil
	})
	return out, err
}

func (b *Board) checkBudget(tx store.Tx, t *model.Task, br *model.Brief) error {
	if t.Budget.MaxCostEUR > 0 && t.CostUsed >= t.Budget.MaxCostEUR {
		return model.Errf("budget_exceeded", "task cost budget exhausted")
	}
	if t.Budget.MaxMinutes > 0 && t.MinutesUsed >= t.Budget.MaxMinutes {
		return model.Errf("budget_exceeded", "task time budget exhausted")
	}
	if br.Budget.MaxCostEUR > 0 {
		c, err := tx.BriefCost(br.ID)
		if err != nil {
			return err
		}
		if c >= br.Budget.MaxCostEUR {
			return model.Errf("budget_exceeded", "brief cost budget exhausted")
		}
	}
	return nil
}

func (b *Board) ClaimTask(ctx context.Context, a model.Actor, taskID string) (*model.Task, error) {
	if p, why := b.Paused(); p {
		return nil, model.Errf("paused", "Subspace is paused: %s", why)
	}
	var out *model.Task
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		if err := pausedTx(tx); err != nil {
			return err
		}
		t, err := tx.GetTask(taskID)
		if err != nil || deref(t.Assignee) != a.ID {
			return model.Errf("not_assigned", "task %s is not assigned to you", taskID)
		}
		if t.Status != model.StatusAssigned && t.Status != model.StatusChangesRequested {
			return model.Conflict("task is %s; only assigned or changes_requested tasks can be claimed", t.Status)
		}
		for _, d := range t.DependsOn {
			dt, err := tx.GetTask(d)
			if err != nil || dt.Status != model.StatusDone {
				return model.Conflict("dependency %s is not done yet", d)
			}
		}
		br, err := tx.GetBrief(t.BriefID)
		if err != nil {
			return err
		}
		if br.Status != "open" {
			return model.Conflict("brief %s is closed", br.ID)
		}
		if err := b.checkBudget(tx, t, br); err != nil {
			return err
		}
		if br.Budget.MaxParallelAgent > 0 {
			ts, err := tx.ListTasks(store.TaskFilter{BriefID: br.ID, Status: []string{model.StatusInProgress}, Limit: 100000})
			if err != nil {
				return err
			}
			active := map[string]bool{}
			for _, x := range ts {
				active[deref(x.Assignee)] = true
			}
			if !active[a.ID] && len(active) >= br.Budget.MaxParallelAgent {
				return model.Errf("capacity", "brief %s already has %d parallel agents", br.ID, len(active))
			}
		}
		now := b.now()
		lease := now.Add(b.o.Lease)
		t.ClaimedAt, t.LeaseExpiresAt, t.LastHeartbeatAt, t.BlockedReason = &now, &lease, &now, nil
		if err := b.statusChange(tx, w, a, "claim_task", t, model.StatusInProgress, ""); err != nil {
			return err
		}
		out = t
		return nil
	})
	return out, err
}

func (b *Board) ReleaseTask(ctx context.Context, a model.Actor, taskID, reason string) (*model.Task, error) {
	var out *model.Task
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		t, err := tx.GetTask(taskID)
		if err != nil || deref(t.Assignee) != a.ID {
			return model.Errf("not_assigned", "task %s is not assigned to you", taskID)
		}
		if t.Status != model.StatusInProgress {
			return model.Conflict("task is %s, not in_progress", t.Status)
		}
		if err := b.releaseLease(tx, w, a, t, model.StatusAssigned, "released: "+reason); err != nil {
			return err
		}
		out = t
		return nil
	})
	return out, err
}

type SubmitInput struct {
	TaskID  string `json:"task_id"`
	PRURL   string `json:"pr_url"`
	Summary string `json:"summary"`
}

func (b *Board) Submit(ctx context.Context, a model.Actor, in SubmitInput) (*model.Task, error) {
	if in.PRURL != "" {
		u, err := url.Parse(in.PRURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, model.BadRequest("pr_url must be an http(s) URL")
		}
	} else if a.Role == model.RoleWorker {
		return nil, model.BadRequest("pr_url is required")
	}
	var out *model.Task
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		t, err := tx.GetTask(in.TaskID)
		if err != nil || deref(t.Assignee) != a.ID {
			return model.Errf("not_assigned", "task %s is not assigned to you", in.TaskID)
		}
		if t.Status != model.StatusInProgress {
			return model.Conflict("task is %s, not in_progress (claim it first; the lease may have expired)", t.Status)
		}
		br, err := tx.GetBrief(t.BriefID)
		if err != nil {
			return err
		}
		to := model.StatusSubmitted
		if !br.CrossReview {
			to = model.StatusApproved
		}
		b.endLease(t)
		t.PRURL, t.ProgressPct = strp(in.PRURL), 100
		if err := b.statusChange(tx, w, a, "submit", t, to, "PR "+in.PRURL); err != nil {
			return err
		}
		if in.Summary != "" {
			if err := w.post(tx, model.ThreadTask, t.ID, a, model.MsgNote, "Submission summary: "+in.Summary); err != nil {
				return err
			}
		}
		out = t
		return nil
	})
	return out, err
}

type StatusInput struct {
	TaskID      string   `json:"task_id"`
	Status      string   `json:"status"`
	ProgressPct *int     `json:"progress_pct"`
	Note        string   `json:"note"`
	CostEUR     *float64 `json:"cost_eur"`
}

func (b *Board) UpdateStatus(ctx context.Context, a model.Actor, in StatusInput) (*model.Task, error) {
	if in.Status != model.StatusInProgress && in.Status != model.StatusBlocked {
		return nil, model.BadRequest("status must be in_progress or blocked")
	}
	if in.ProgressPct != nil && (*in.ProgressPct < 0 || *in.ProgressPct > 100) {
		return nil, model.BadRequest("progress_pct must be 0..100")
	}
	if in.CostEUR != nil && *in.CostEUR < 0 {
		return nil, model.BadRequest("cost_eur must be >= 0")
	}
	var out *model.Task
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		t, err := tx.GetTask(in.TaskID)
		if err != nil || deref(t.Assignee) != a.ID {
			return model.Errf("not_assigned", "task %s is not assigned to you", in.TaskID)
		}
		if t.Status != model.StatusInProgress {
			return model.Conflict("task is %s; update_status needs an in_progress task (claim it first)", t.Status)
		}
		if in.ProgressPct != nil {
			t.ProgressPct = *in.ProgressPct
		}
		if in.CostEUR != nil && *in.CostEUR > t.CostUsed {
			t.CostUsed = *in.CostEUR
		}
		br, err := tx.GetBrief(t.BriefID)
		if err != nil {
			return err
		}
		out = t
		if in.Status == model.StatusBlocked {
			return b.block(tx, w, a, t, "blocked by agent: "+in.Note)
		}
		if berr := b.checkBudget(tx, t, br); berr != nil {
			return b.block(tx, w, a, t, "budget_exceeded: "+berr.Error())
		}
		t.UpdatedAt = b.now()
		if err := tx.UpdateTask(t); err != nil {
			return err
		}
		w.task(t, false)
		if in.Note != "" {
			if err := w.post(tx, model.ThreadTask, t.ID, a, model.MsgStatus, fmt.Sprintf("%d%%: %s", t.ProgressPct, in.Note)); err != nil {
				return err
			}
		}
		return w.audit(tx, a, "update_status", t.ID, nil, map[string]any{"progress_pct": t.ProgressPct, "cost_used": t.CostUsed}, in.Note)
	})
	if err == nil && out != nil && out.Status == model.StatusBlocked && in.Status != model.StatusBlocked {
		return out, model.Errf("budget_exceeded", "budget exceeded: task blocked")
	}
	return out, err
}

type HeartbeatResult struct {
	OK             bool       `json:"ok"`
	Paused         bool       `json:"paused"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at"`
}

func (b *Board) Heartbeat(ctx context.Context, a model.Actor, taskID string) (*HeartbeatResult, error) {
	b.Touch(a.ID)
	if p, _ := b.Paused(); p {
		return &HeartbeatResult{OK: true, Paused: true}, nil
	}
	var mine []*model.Task
	err := b.read(ctx, func(tx store.Tx) error {
		ts, err := tx.ListTasks(store.TaskFilter{Assignee: a.ID, Status: []string{model.StatusInProgress}})
		mine = ts
		return err
	})
	if err != nil {
		return nil, err
	}
	now := b.now()
	lease := now.Add(b.o.Lease)
	res := &HeartbeatResult{OK: true}
	b.mu.Lock()
	for _, t := range mine {
		if taskID != "" && t.ID != taskID {
			continue
		}
		b.tasks[t.ID] = hotTask{hb: now, lease: lease}
		b.dirtyT[t.ID] = struct{}{}
		l := lease
		res.LeaseExpiresAt = &l
	}
	b.mu.Unlock()
	if taskID != "" && res.LeaseExpiresAt == nil {
		return nil, model.Errf("not_assigned", "task %s is not an in_progress task of yours", taskID)
	}
	return res, nil
}

func (b *Board) ReportTests(ctx context.Context, a model.Actor, taskID string, passed, failed int, log string) (*model.Task, error) {
	if passed < 0 || failed < 0 {
		return nil, model.BadRequest("counts must be >= 0")
	}
	if len(log) > 8000 {
		log = log[:8000] + "…"
	}
	var out *model.Task
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		t, err := tx.GetTask(taskID)
		if err != nil || deref(t.Assignee) != a.ID {
			return model.Errf("not_assigned", "task %s is not assigned to you", taskID)
		}
		t.Tests = &model.TestResult{Passed: passed, Failed: failed, ReportedAt: b.now()}
		t.UpdatedAt = b.now()
		if err := tx.UpdateTask(t); err != nil {
			return err
		}
		w.task(t, false)
		body := fmt.Sprintf("Tests: %d passed, %d failed", passed, failed)
		if log != "" {
			body += "\n" + log
		}
		if err := w.post(tx, model.ThreadTask, t.ID, a, model.MsgNote, body); err != nil {
			return err
		}
		out = t
		return w.audit(tx, a, "report_tests", t.ID, nil, map[string]any{"passed": passed, "failed": failed}, "")
	})
	return out, err
}

const MaxReviewRounds = 3

func (b *Board) ListReviews(ctx context.Context, a model.Actor) ([]*model.Task, error) {
	var out []*model.Task
	err := b.read(ctx, func(tx store.Tx) error {
		me, err := tx.GetAgent(a.ID)
		if err != nil {
			return err
		}
		ts, err := tx.ListTasks(store.TaskFilter{Status: []string{model.StatusSubmitted, model.StatusInReview}})
		if err != nil {
			return err
		}
		prov := map[string]string{}
		allowed := map[string]bool{}
		for _, t := range ts {
			if t.Reviewer != nil && *t.Reviewer != a.ID {
				continue
			}
			ok, seen := allowed[t.BriefID]
			if !seen {
				br, err := tx.GetBrief(t.BriefID)
				if err != nil {
					return err
				}
				ok = b.providerAllowed(br, me.Provider)
				allowed[t.BriefID] = ok
			}
			if !ok {
				continue
			}
			p, ok := prov[deref(t.Assignee)]
			if !ok && t.Assignee != nil {
				if wk, e := tx.GetAgent(*t.Assignee); e == nil {
					p = wk.Provider
				}
				prov[*t.Assignee] = p
			}
			if p != "" && p == me.Provider {
				continue
			}
			out = append(out, b.overlay(t))
		}
		return nil
	})
	return nz(out), err
}

func (b *Board) Review(ctx context.Context, a model.Actor, taskID, verdict, comments string) (*model.Task, error) {
	if verdict != "approve" && verdict != "changes_requested" {
		return nil, model.BadRequest("verdict must be approve or changes_requested")
	}
	if verdict == "changes_requested" && strings.TrimSpace(comments) == "" {
		return nil, model.BadRequest("comments are required when requesting changes")
	}
	var out *model.Task
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		t, err := tx.GetTask(taskID)
		if err != nil {
			return model.NotFound("task %s not found", taskID)
		}
		if t.Status != model.StatusSubmitted && t.Status != model.StatusInReview {
			return model.Conflict("task is %s, not awaiting review", t.Status)
		}
		if t.Reviewer != nil && *t.Reviewer != a.ID {
			return model.Errf("not_assigned", "task %s is assigned to another reviewer", taskID)
		}
		me, err := tx.GetAgent(a.ID)
		if err != nil {
			return err
		}
		br, err := tx.GetBrief(t.BriefID)
		if err != nil {
			return err
		}
		if !b.providerAllowed(br, me.Provider) {
			return model.Forbidden("provider %q is not allowed for brief %s", me.Provider, br.ID)
		}
		if t.Assignee != nil {
			if wk, e := tx.GetAgent(*t.Assignee); e == nil && wk.Provider == me.Provider {
				return model.Forbidden("reviewer must use a different provider than the worker (%s)", wk.Provider)
			}
		}
		t.Reviewer = &a.ID
		if err := w.post(tx, model.ThreadTask, t.ID, a, model.MsgNote, "Review ("+verdict+"): "+comments); err != nil {
			return err
		}
		if verdict == "approve" {
			out = t
			return b.statusChange(tx, w, a, "review", t, model.StatusApproved, comments)
		}
		t.ReviewRounds++
		to := model.StatusChangesRequested
		if t.ReviewRounds >= MaxReviewRounds {
			to = model.StatusNeedsOwner
			t.BlockedReason = strp(fmt.Sprintf("%d review rounds without approval", t.ReviewRounds))
		}
		out = t
		return b.statusChange(tx, w, a, "review", t, to, comments)
	})
	return out, err
}

func (b *Board) MarkDone(ctx context.Context, id, note string) (*model.Task, error) {
	return b.ownerMove(ctx, "mark_done", id, note, model.StatusDone, model.StatusApproved, model.StatusNeedsOwner)
}

func (b *Board) Cancel(ctx context.Context, id, reason string) (*model.Task, error) {
	return b.ownerMove(ctx, "cancel", id, reason, model.StatusCancelled)
}

func (b *Board) ownerMove(ctx context.Context, tool, id, note, to string, from ...string) (*model.Task, error) {
	var out *model.Task
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		t, err := tx.GetTask(id)
		if err != nil {
			return err
		}
		if len(from) > 0 && !contains(from, t.Status) {
			return model.Conflict("task is %s; allowed from %s", t.Status, strings.Join(from, ", "))
		}
		if t.Status == model.StatusDone || t.Status == model.StatusCancelled {
			return model.Conflict("task is already %s", t.Status)
		}
		if t.Status == model.StatusInProgress {
			b.endLease(t)
		}
		out = t
		return b.statusChange(tx, w, model.Owner, tool, t, to, note)
	})
	return out, err
}

func (b *Board) Reopen(ctx context.Context, id, to, note string) (*model.Task, error) {
	if to == "" {
		to = model.StatusReady
	}
	if to != model.StatusReady && to != model.StatusAssigned {
		return nil, model.BadRequest("status must be ready or assigned")
	}
	var out *model.Task
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		t, err := tx.GetTask(id)
		if err != nil {
			return err
		}
		switch t.Status {
		case model.StatusNeedsOwner, model.StatusBlocked, model.StatusCancelled:
		default:
			return model.Conflict("cannot reopen a %s task", t.Status)
		}
		if to == model.StatusAssigned && t.Assignee == nil {
			return model.BadRequest("task has no assignee")
		}
		if t.Status == model.StatusNeedsOwner {
			t.ReviewRounds = 0
		}
		t.BlockedReason = nil
		out = t
		return b.statusChange(tx, w, model.Owner, "reopen", t, to, note)
	})
	return out, err
}

func (b *Board) SetTaskBudget(ctx context.Context, id string, bud model.Budget) (*model.Task, error) {
	if bud.MaxMinutes < 0 || bud.MaxCostEUR < 0 {
		return nil, model.BadRequest("budget must be >= 0")
	}
	var out *model.Task
	err := b.write(ctx, func(tx store.Tx, w *wctx) error {
		t, err := tx.GetTask(id)
		if err != nil {
			return err
		}
		t.Budget = bud
		out = t
		if t.Status == model.StatusBlocked && strings.HasPrefix(deref(t.BlockedReason), "budget_exceeded") {
			br, err := tx.GetBrief(t.BriefID)
			if err != nil {
				return err
			}
			if b.checkBudget(tx, t, br) == nil {
				to := model.StatusReady
				if t.Assignee != nil {
					to = model.StatusAssigned
				}
				t.BlockedReason = nil
				return b.statusChange(tx, w, model.Owner, "set_budget", t, to, "budget raised")
			}
		}
		t.UpdatedAt = b.now()
		if err := tx.UpdateTask(t); err != nil {
			return err
		}
		w.task(t, false)
		return w.audit(tx, model.Owner, "set_budget", id, nil, map[string]any{"max_minutes": bud.MaxMinutes, "max_cost_eur": bud.MaxCostEUR}, "")
	})
	return out, err
}

func (b *Board) Sweep(ctx context.Context) error {
	var ts []*model.Task
	if err := b.read(ctx, func(tx store.Tx) (e error) {
		ts, e = tx.ListTasks(store.TaskFilter{Status: []string{model.StatusInProgress}, Limit: 100000})
		return
	}); err != nil {
		return err
	}
	now := b.now()
	for _, t := range ts {
		ov := b.overlay(t)
		expired := ov.LeaseExpiresAt != nil && ov.LeaseExpiresAt.Before(now)
		over := t.Budget.MaxMinutes > 0 && t.ClaimedAt != nil && t.MinutesUsed+int(now.Sub(*t.ClaimedAt)/time.Minute) >= t.Budget.MaxMinutes
		if !expired && !over {
			continue
		}
		id := t.ID
		err := b.write(ctx, func(tx store.Tx, w *wctx) error {
			cur, err := tx.GetTask(id)
			if err != nil || cur.Status != model.StatusInProgress {
				return nil
			}
			ov := b.overlay(cur)
			sys := model.Actor{ID: "system", Role: "system"}
			if over {
				return b.block(tx, w, sys, cur, "budget_exceeded: task time budget exhausted")
			}
			if ov.LeaseExpiresAt != nil && !ov.LeaseExpiresAt.Before(b.now()) {
				return nil
			}
			return b.releaseLease(tx, w, sys, cur, model.StatusAssigned, "lease expired")
		})
		if err != nil {
			return err
		}
	}
	return nil
}
