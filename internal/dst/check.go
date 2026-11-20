package dst

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
)

type Violation struct {
	Invariant string
	Detail    string
}

func (v *Violation) Error() string { return v.Invariant + ": " + v.Detail }

func violation(inv, format string, a ...any) *Violation {
	return &Violation{Invariant: inv, Detail: fmt.Sprintf(format, a...)}
}

const (
	InvSingleLease  = "single_live_lease"
	InvLeaseHonored = "lease_honored_until_expiry"
	InvLeaseExpiry  = "lease_expiry_returns_task"
	InvPaused       = "no_claims_while_paused"
	InvTransition   = "allowed_transition"
	InvReview       = "review_rounds_to_owner"
	InvBudget       = "budget_not_exceeded"
	InvAudit        = "audit_append_only_complete"
	InvClaim        = "claim_preconditions"
	InvAtomic       = "failed_call_no_effect"
)

var edges = map[[2]string]bool{}

func init() {
	s := func(from string, to ...string) {
		for _, t := range to {
			edges[[2]string{from, t}] = true
		}
	}
	s(model.StatusBacklog, model.StatusReady)
	s(model.StatusReady, model.StatusAssigned)
	s(model.StatusAssigned, model.StatusInProgress)
	s(model.StatusInProgress, model.StatusAssigned, model.StatusBlocked, model.StatusSubmitted)
	s(model.StatusBlocked, model.StatusInProgress, model.StatusReady, model.StatusAssigned)
	s(model.StatusSubmitted, model.StatusInReview, model.StatusApproved, model.StatusChangesRequested)
	s(model.StatusInReview, model.StatusApproved, model.StatusChangesRequested)
	s(model.StatusChangesRequested, model.StatusInProgress)
	s(model.StatusApproved, model.StatusDone)
	s(model.StatusNeedsOwner, model.StatusReady, model.StatusAssigned, model.StatusDone)
	for _, st := range model.AllStatuses {
		if st != model.StatusDone && st != model.StatusCancelled {
			s(st, model.StatusNeedsOwner, model.StatusCancelled)
		}
	}
}

type lease struct {
	agent  string
	expiry time.Time
}

type Checker struct {
	leases map[string]lease
	audit  []*model.AuditEvent
}

func NewChecker() *Checker { return &Checker{leases: map[string]lease{}} }

func live(l lease, now time.Time) bool { return !l.expiry.Before(now) }

func holds(t *model.Task, agent string) bool {
	return t != nil && t.Status == model.StatusInProgress && t.Assignee != nil && *t.Assignee == agent
}

func (c *Checker) Check(op Op, res Result, pre, post *State) *Violation {
	for _, f := range []func(Op, Result, *State, *State) *Violation{
		c.checkAudit, c.checkAtomic, c.checkTransitions, c.checkClaim, c.checkLeases, c.checkState,
	} {
		if v := f(op, res, pre, post); v != nil {
			return v
		}
	}
	c.audit = post.Audit
	return nil
}

func changed(pre, post *State) []string {
	var out []string
	for _, t := range post.Tasks {
		p := pre.Task(t.ID)
		if p == nil || p.Status != t.Status || deref(p.Assignee) != deref(t.Assignee) || deref(p.Reviewer) != deref(t.Reviewer) ||
			p.ReviewRounds != t.ReviewRounds || p.CostUsed != t.CostUsed || p.ProgressPct != t.ProgressPct {
			out = append(out, t.ID)
		}
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (c *Checker) checkAudit(op Op, res Result, pre, post *State) *Violation {
	if len(post.Audit) < len(c.audit) {
		return violation(InvAudit, "audit shrank from %d to %d events", len(c.audit), len(post.Audit))
	}
	for i, e := range c.audit {
		if !reflect.DeepEqual(e, post.Audit[i]) {
			return violation(InvAudit, "audit event %d changed", e.ID)
		}
	}
	added := post.Audit[len(c.audit):]
	last := int64(0)
	if len(c.audit) > 0 {
		last = c.audit[len(c.audit)-1].ID
	}
	for _, e := range added {
		if e.ID <= last {
			return violation(InvAudit, "audit id %d not increasing after %d", e.ID, last)
		}
		last = e.ID
	}
	if res.Err == nil && op.Mutating() && len(added) == 0 {
		return violation(InvAudit, "successful %s appended no audit event", op.Kind)
	}
	for _, id := range changed(pre, post) {
		p, t := pre.Task(id), post.Task(id)
		covered := false
		for _, e := range added {
			if e.TaskID == nil || *e.TaskID == id {
				covered = true
				break
			}
		}
		if !covered {
			return violation(InvAudit, "task %s changed %s → %s without audit", id, status(p), t.Status)
		}
	}
	return nil
}

func ts(t *time.Time) string {
	if t == nil {
		return "null"
	}
	return t.Format("15:04:05.000")
}

func status(t *model.Task) string {
	if t == nil {
		return "(none)"
	}
	return t.Status
}

func (c *Checker) checkAtomic(op Op, res Result, pre, post *State) *Violation {
	if res.Err == nil {
		return nil
	}
	if op.Kind == OpUpdate && res.Code() == "budget_exceeded" {
		return nil
	}
	if ch := changed(pre, post); len(ch) > 0 {
		return violation(InvAtomic, "%s failed (%v) but changed %v", op.Kind, res.Err, ch)
	}
	if len(post.Audit) != len(c.audit) {
		return violation(InvAtomic, "%s failed (%v) but appended audit", op.Kind, res.Err)
	}
	if pre.Paused != post.Paused {
		return violation(InvAtomic, "%s failed (%v) but changed pause state", op.Kind, res.Err)
	}
	return nil
}

func (c *Checker) checkTransitions(op Op, _ Result, pre, post *State) *Violation {
	for _, t := range post.Tasks {
		p := pre.Task(t.ID)
		if p == nil || p.Status == t.Status {
			continue
		}
		if !edges[[2]string{p.Status, t.Status}] {
			return violation(InvTransition, "task %s %s → %s via %s", t.ID, p.Status, t.Status, op.Kind)
		}
	}
	return nil
}

func (c *Checker) checkClaim(op Op, res Result, pre, post *State) *Violation {
	if op.Kind != OpClaim || res.Err != nil {
		return nil
	}
	if pre.Paused {
		return violation(InvPaused, "claim of %s by %s succeeded while paused", op.Task, op.Actor)
	}
	t := pre.Task(op.Task)
	if t == nil {
		return violation(InvClaim, "claim of unknown task %s succeeded", op.Task)
	}
	if deref(t.Assignee) != op.Actor {
		return violation(InvClaim, "%s claimed %s assigned to %q", op.Actor, t.ID, deref(t.Assignee))
	}
	if t.Status != model.StatusAssigned && t.Status != model.StatusChangesRequested {
		return violation(InvClaim, "%s claimed %s in status %s", op.Actor, t.ID, t.Status)
	}
	for _, d := range t.DependsOn {
		if dt := pre.Task(d); dt == nil || dt.Status != model.StatusDone {
			return violation(InvClaim, "%s claimed %s before dependency %s was done", op.Actor, t.ID, d)
		}
	}
	if t.Budget.MaxCostEUR > 0 && t.CostUsed >= t.Budget.MaxCostEUR {
		return violation(InvBudget, "claim of %s with cost %.2f >= budget %.2f", t.ID, t.CostUsed, t.Budget.MaxCostEUR)
	}
	if t.Budget.MaxMinutes > 0 && t.MinutesUsed >= t.Budget.MaxMinutes {
		return violation(InvBudget, "claim of %s with %d minutes >= budget %d", t.ID, t.MinutesUsed, t.Budget.MaxMinutes)
	}
	if br := pre.Brief(t.BriefID); br != nil && br.Budget.MaxCostEUR > 0 && br.CostUsed >= br.Budget.MaxCostEUR {
		return violation(InvBudget, "claim of %s while brief %s cost %.2f >= %.2f", t.ID, br.ID, br.CostUsed, br.Budget.MaxCostEUR)
	}
	if l, ok := c.leases[t.ID]; ok && l.agent != op.Actor && live(l, pre.Now) {
		return violation(InvSingleLease, "%s got a lease on %s while %s holds one until %s", op.Actor, t.ID, l.agent, ts(&l.expiry))
	}
	if res.Lease == nil {
		return violation(InvClaim, "claim of %s returned no lease", t.ID)
	}
	return nil
}

func budgetBlocked(t *model.Task) bool {
	return t != nil && t.Status == model.StatusBlocked && strings.HasPrefix(deref(t.BlockedReason), "budget_exceeded")
}

func (c *Checker) checkLeases(op Op, res Result, pre, post *State) *Violation {
	now := post.Now
	if res.Err == nil {
		switch op.Kind {
		case OpClaim:
			c.leases[op.Task] = lease{op.Actor, *res.Lease}
		case OpHeartbeat:
			if res.Paused && !pre.Paused {
				return violation(InvPaused, "heartbeat reported paused while not paused")
			}
			if !res.Paused && pre.Paused {
				return violation(InvPaused, "heartbeat did not report pause")
			}
			if res.Lease != nil {
				for id, l := range c.leases {
					if l.agent == op.Actor && (op.Task == "" || op.Task == id) && holds(post.Task(id), op.Actor) {
						c.leases[id] = lease{op.Actor, *res.Lease}
					}
				}
			}
		case OpRelease, OpSubmit, OpBlock, OpReassign:
			delete(c.leases, op.Task)
		case OpPauseAll:
			c.leases = map[string]lease{}
		}
	}
	for id, l := range c.leases {
		t := post.Task(id)
		if holds(t, l.agent) {
			continue
		}
		delete(c.leases, id)
		if !live(l, now) {
			continue
		}
		if op.Kind == OpUpdate && op.Actor == l.agent && budgetBlocked(t) {
			continue
		}
		if op.Kind == OpSweep && budgetBlocked(t) {
			continue
		}
		return violation(InvLeaseHonored, "task %s left %s's lease (valid until %s) via %s, now %s/%s",
			id, l.agent, ts(&l.expiry), op.Kind, status(t), deref(t.Assignee))
	}
	if op.Kind == OpSweep && res.Err == nil {
		for _, t := range post.Tasks {
			if t.Status != model.StatusInProgress {
				continue
			}
			if t.LeaseExpiresAt == nil || t.LeaseExpiresAt.Before(now) {
				return violation(InvLeaseExpiry, "task %s still in_progress after sweep with lease %s at %s", t.ID, ts(t.LeaseExpiresAt), ts(&now))
			}
			if t.Budget.MaxMinutes > 0 && t.ClaimedAt != nil && t.MinutesUsed+int(now.Sub(*t.ClaimedAt)/time.Minute) >= t.Budget.MaxMinutes {
				return violation(InvBudget, "task %s in_progress after sweep beyond time budget %d", t.ID, t.Budget.MaxMinutes)
			}
		}
	}
	return nil
}

func (c *Checker) checkState(op Op, _ Result, _, post *State) *Violation {
	parallel := map[string]map[string]bool{}
	for _, t := range post.Tasks {
		if t.Status == model.StatusInProgress {
			if post.Paused {
				return violation(InvPaused, "task %s in_progress while paused", t.ID)
			}
			if t.Assignee == nil || t.LeaseExpiresAt == nil {
				return violation(InvSingleLease, "task %s in_progress without assignee or lease", t.ID)
			}
			if t.Budget.MaxCostEUR > 0 && t.CostUsed >= t.Budget.MaxCostEUR {
				return violation(InvBudget, "task %s in_progress with cost %.2f >= budget %.2f", t.ID, t.CostUsed, t.Budget.MaxCostEUR)
			}
			if parallel[t.BriefID] == nil {
				parallel[t.BriefID] = map[string]bool{}
			}
			parallel[t.BriefID][*t.Assignee] = true
		} else if t.LeaseExpiresAt != nil {
			return violation(InvSingleLease, "task %s is %s but still has a lease", t.ID, t.Status)
		}
		if t.ReviewRounds > board.MaxReviewRounds {
			return violation(InvReview, "task %s has %d review rounds", t.ID, t.ReviewRounds)
		}
		if t.ReviewRounds == board.MaxReviewRounds {
			switch t.Status {
			case model.StatusNeedsOwner, model.StatusDone, model.StatusCancelled:
			default:
				return violation(InvReview, "task %s has %d review rounds but is %s after %s", t.ID, t.ReviewRounds, t.Status, op.Kind)
			}
		}
	}
	for _, br := range post.Briefs {
		if n := len(parallel[br.ID]); br.Budget.MaxParallelAgent > 0 && n > br.Budget.MaxParallelAgent {
			return violation(InvBudget, "brief %s has %d parallel agents, max %d", br.ID, n, br.Budget.MaxParallelAgent)
		}
	}
	return nil
}
