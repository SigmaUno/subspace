package dst

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/events"
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
	InvReviewer     = "reviewer_provider_differs"
	InvBudget       = "budget_not_exceeded"
	InvAudit        = "audit_append_only_complete"
	InvClaim        = "claim_preconditions"
	InvAtomic       = "failed_call_no_effect"
	InvMonotonic    = "counters_monotonic"
	InvRestart      = "restart_preserves_state"
	InvToken        = "token_rotation"
	InvSSE          = "sse_delivery_accounted"
	InvRegister     = "registration_atomic"
	InvCapacity     = "spawn_within_capacity"
	InvPlan         = "single_plan_decision"
	InvMerge        = "mergequeue_merges_only_approved"
	InvAutoEnqueue  = "approved_task_enqueued"
	InvBackup       = "backup_verifies"
	InvNotify       = "notification_once_per_decision"
	InvAutopilot    = "autopilot_within_limits"
	InvWebhook      = "webhook_delivery"
	InvInstr        = "instructions_versioned"
	InvSearch       = "search_matches"
	InvHarness      = "harness"
	InvLeaderScope  = "leader_sees_only_own_briefs"
	InvQuota        = "agent_quota_never_exceeded"
	InvAnnounce     = "announcement_reaches_audience_once"
	InvSchedule     = "schedule_runs_once_per_instant"
	InvRetention    = "audit_chain_valid_across_archive"
	InvContext      = "handoff_context_for_assignee_only"
	InvSplit        = "split_acceptance_atomic"
	InvKnowledge    = "knowledge_moderated"
	InvHint         = "tool_errors_carry_hints"
	InvEstimate     = "estimates_consistent"
	InvIncident     = "incident_containment"
	InvGates        = "no_approval_with_failing_gates"
)

const spawnGrace = 10 * time.Minute

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
	s(model.StatusCancelled, model.StatusReady, model.StatusAssigned)
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
	lease    time.Duration
	leases   map[string]lease
	tokens   map[string]int
	instr    map[string][]string
	auditLen int
	faults   int
	fm       *featureModel
}

func NewChecker(d time.Duration) *Checker {
	return &Checker{lease: d, leases: map[string]lease{}, tokens: map[string]int{}, instr: map[string][]string{}, fm: newFeatureModel()}
}

func (c *Checker) checkFaults(_ *step, _, post *State) *Violation {
	if len(post.Faults) > c.faults {
		v := post.Faults[c.faults]
		c.faults = len(post.Faults)
		return v
	}
	return nil
}

func (c *Checker) Init(s *State) {
	c.auditLen = len(s.Audit)
	for id, n := range s.Tokens {
		c.tokens[id] = n
	}
}

func live(l lease, now time.Time) bool { return !l.expiry.Before(now) }

func holds(t *model.Task, agent string) bool {
	return t != nil && t.Status == model.StatusInProgress && t.Assignee != nil && *t.Assignee == agent
}

type step struct {
	op    Op
	res   Result
	ops   []Op
	rs    []Result
	added []*model.AuditEvent
}

func (s *step) each(fn func(Op, Result) bool) bool {
	for i, op := range s.ops {
		if fn(op, s.rs[i]) {
			return true
		}
	}
	return false
}

func (s *step) succeeded(k Kind) bool {
	return s.each(func(op Op, r Result) bool { return op.Kind == k && r.Err == nil })
}

func (c *Checker) Check(op Op, res Result, pre, post *State) *Violation {
	if len(post.Audit) < c.auditLen {
		return violation(InvAudit, "audit shrank from %d to %d events", c.auditLen, len(post.Audit))
	}
	st := &step{op: op, res: res, ops: []Op{op}, rs: []Result{res}, added: post.Audit[c.auditLen:]}
	if op.Kind == OpBatch {
		st.ops, st.rs = op.Sub, res.Sub
	}
	checks := []func(*step, *State, *State) *Violation{c.checkFaults, c.checkAudit, c.checkReplay, c.checkMonotonic}
	if op.Kind == OpBatch {
		checks = append(checks, c.checkBatchClaims, c.checkBatchLeases)
	} else {
		checks = append(checks, c.checkAtomic, c.checkTransitions, c.checkClaim, c.checkLeases, c.checkRestart)
	}
	checks = append(checks, c.checkTokens, c.checkReviewers, c.checkRegistration, c.checkCapacity, c.checkPlans, c.checkSSE, c.checkState, c.checkLeaderScope, c.checkServer, c.checkFeatures)
	for _, f := range checks {
		if v := f(st, pre, post); v != nil {
			return v
		}
	}
	c.auditLen = len(post.Audit)
	return nil
}

func changed(pre, post *State) []string {
	var out []string
	for _, t := range post.Tasks {
		p := pre.Task(t.ID)
		if p == nil || p.Status != t.Status || deref(p.Assignee) != deref(t.Assignee) || deref(p.Reviewer) != deref(t.Reviewer) ||
			p.ReviewRounds != t.ReviewRounds || p.CostUsed != t.CostUsed || p.ProgressPct != t.ProgressPct || p.MinutesUsed != t.MinutesUsed {
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

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

func (c *Checker) checkAudit(st *step, pre, post *State) *Violation {
	last := int64(0)
	if c.auditLen > 0 {
		last = post.Audit[c.auditLen-1].ID
	}
	for _, e := range st.added {
		if e.ID <= last {
			return violation(InvAudit, "audit id %d not increasing after %d", e.ID, last)
		}
		last = e.ID
	}
	ok := 0
	st.each(func(op Op, r Result) bool {
		if r.Err == nil && op.Mutating() {
			ok++
		}
		return false
	})
	if len(st.added) < ok {
		return violation(InvAudit, "%d successful mutating calls appended %d audit events", ok, len(st.added))
	}
	for _, id := range changed(pre, post) {
		p, t := pre.Task(id), post.Task(id)
		covered := false
		for _, e := range st.added {
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

func (c *Checker) checkReplay(st *step, pre, post *State) *Violation {
	stat, cost := map[string]string{}, map[string]float64{}
	for _, t := range pre.Tasks {
		stat[t.ID], cost[t.ID] = t.Status, t.CostUsed
	}
	planned := false
	for _, e := range st.added {
		switch e.Tool {
		case "approve_plan", "reject_plan", "propose_plan":
			planned = true
		}
		if e.TaskID == nil {
			continue
		}
		id := *e.TaskID
		from, to := str(e.Before["status"]), str(e.After["status"])
		if from != "" && to != "" {
			cur, known := stat[id]
			if known && cur != from && !planSkip(planned, cur, from) {
				return violation(InvTransition, "task %s audited %s → %s (%s) while it was %s", id, from, to, e.Tool, cur)
			}
			if from != to && !edges[[2]string{from, to}] {
				return violation(InvTransition, "task %s audited %s → %s via %s", id, from, to, e.Tool)
			}
			stat[id] = to
		}
		switch e.Tool {
		case "block":
			if t := post.Task(id); t != nil {
				cost[id] = t.CostUsed
			}
		case "update_status":
			cost[id] = num(e.After["cost_used"])
			t := post.Task(id)
			if t == nil {
				continue
			}
			if t.Budget.MaxCostEUR > 0 && cost[id] >= t.Budget.MaxCostEUR {
				return violation(InvBudget, "update_status on %s accepted cost %.2f >= task budget %.2f", id, cost[id], t.Budget.MaxCostEUR)
			}
			br := post.Brief(t.BriefID)
			if br == nil || br.Budget.MaxCostEUR <= 0 {
				continue
			}
			sum := 0.0
			for _, x := range post.Tasks {
				if x.BriefID == br.ID {
					sum += cost[x.ID]
				}
			}
			if sum >= br.Budget.MaxCostEUR+1e-9 {
				return violation(InvBudget, "update_status on %s accepted while brief %s cost %.2f >= %.2f", id, br.ID, sum, br.Budget.MaxCostEUR)
			}
		}
	}
	for _, t := range post.Tasks {
		cur, known := stat[t.ID]
		if !known || cur == t.Status {
			continue
		}
		if planSkip(planned, cur, t.Status) {
			continue
		}
		return violation(InvTransition, "task %s is %s but its audit trail ends at %s", t.ID, t.Status, cur)
	}
	return nil
}

func planSkip(planned bool, from, to string) bool {
	return planned && from == model.StatusBacklog && (to == model.StatusReady || to == model.StatusCancelled)
}

func (c *Checker) checkMonotonic(st *step, pre, post *State) *Violation {
	reopened := map[string]bool{}
	st.each(func(op Op, r Result) bool {
		if op.Kind == OpReopen && r.Err == nil {
			reopened[op.Task] = true
		}
		return false
	})
	for _, t := range post.Tasks {
		if t.MinutesUsed < 0 {
			return violation(InvMonotonic, "task %s has %d minutes used after %s", t.ID, t.MinutesUsed, st.op.Kind)
		}
		p := pre.Task(t.ID)
		if p == nil {
			continue
		}
		if t.MinutesUsed < p.MinutesUsed {
			return violation(InvMonotonic, "task %s minutes_used %d → %d via %s", t.ID, p.MinutesUsed, t.MinutesUsed, st.op.Kind)
		}
		if t.CostUsed < p.CostUsed {
			return violation(InvMonotonic, "task %s cost_used %.2f → %.2f via %s", t.ID, p.CostUsed, t.CostUsed, st.op.Kind)
		}
		if t.ReviewRounds < p.ReviewRounds && (!reopened[t.ID] || (p.Status != model.StatusNeedsOwner && p.ReviewRounds < board.MaxReviewRounds)) {
			return violation(InvMonotonic, "task %s review_rounds %d → %d via %s", t.ID, p.ReviewRounds, t.ReviewRounds, st.op.Kind)
		}
	}
	return nil
}

func agentIDs(s *State) map[string]*model.Agent {
	out := make(map[string]*model.Agent, len(s.Agents))
	for _, a := range s.Agents {
		out[a.ID] = a
	}
	return out
}

func (c *Checker) checkAtomic(st *step, pre, post *State) *Violation {
	op, res := st.op, st.res
	switch op.Kind {
	case OpRestart:
		if res.Err != nil {
			return violation(InvRestart, "restart failed: %v", res.Err)
		}
		return nil
	case OpUnsubscribe:
		if res.Err != nil {
			return violation(InvSSE, "%v", res.Err)
		}
		return nil
	}
	if res.Err == nil {
		return nil
	}
	if op.Kind == OpUpdate && res.Code() == "budget_exceeded" {
		return nil
	}
	if op.Kind == OpReview && res.Code() == "gates_failed" {
		return nil
	}
	switch op.Kind {
	case OpAcceptSplit, OpScheduleTick, OpIncidentStart, OpIncidentEnd, OpRetention, OpRestore, OpAnnounce:
		return nil
	}
	if ch := changed(pre, post); len(ch) > 0 {
		return violation(InvAtomic, "%s failed (%v) but changed %v", op.Kind, res.Err, ch)
	}
	if len(st.added) > 0 {
		return violation(InvAtomic, "%s failed (%v) but appended audit", op.Kind, res.Err)
	}
	if pre.Paused != post.Paused {
		return violation(InvAtomic, "%s failed (%v) but changed pause state", op.Kind, res.Err)
	}
	if len(pre.Agents) != len(post.Agents) {
		return violation(InvAtomic, "%s failed (%v) but agents went %d → %d", op.Kind, res.Err, len(pre.Agents), len(post.Agents))
	}
	if !reflect.DeepEqual(pre.CapacityConfig(), post.CapacityConfig()) {
		return violation(InvAtomic, "%s failed (%v) but changed capacity", op.Kind, res.Err)
	}
	if !reflect.DeepEqual(pre.Tokens, post.Tokens) {
		return violation(InvAtomic, "%s failed (%v) but issued a token", op.Kind, res.Err)
	}
	return nil
}

func (c *Checker) checkTransitions(st *step, pre, post *State) *Violation {
	for _, t := range post.Tasks {
		p := pre.Task(t.ID)
		if p == nil || p.Status == t.Status {
			continue
		}
		if !edges[[2]string{p.Status, t.Status}] {
			return violation(InvTransition, "task %s %s → %s via %s", t.ID, p.Status, t.Status, st.op.Kind)
		}
	}
	return nil
}

func (c *Checker) checkClaim(st *step, pre, _ *State) *Violation {
	op, res := st.op, st.res
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
	if v := claimBudget(pre, t, op); v != nil {
		return v
	}
	if t.Budget.MaxMinutes > 0 && t.MinutesUsed >= t.Budget.MaxMinutes {
		return violation(InvBudget, "claim of %s with %d minutes >= budget %d", t.ID, t.MinutesUsed, t.Budget.MaxMinutes)
	}
	if br := pre.Brief(t.BriefID); br != nil && br.Budget.MaxCostEUR > 0 && br.CostUsed >= br.Budget.MaxCostEUR+1e-9 {
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

func claimBudget(s *State, t *model.Task, op Op) *Violation {
	for _, d := range t.DependsOn {
		if dt := s.Task(d); dt == nil || dt.Status != model.StatusDone {
			return violation(InvClaim, "%s claimed %s before dependency %s was done", op.Actor, t.ID, d)
		}
	}
	if t.Budget.MaxCostEUR > 0 && t.CostUsed >= t.Budget.MaxCostEUR {
		return violation(InvBudget, "claim of %s with cost %.2f >= budget %.2f", t.ID, t.CostUsed, t.Budget.MaxCostEUR)
	}
	return nil
}

func (c *Checker) checkBatchClaims(st *step, pre, post *State) *Violation {
	resumed, cancelled := st.succeeded(OpResumeAll), st.succeeded(OpCancel)
	var v *Violation
	st.each(func(op Op, r Result) bool {
		if op.Kind != OpClaim || r.Err != nil {
			return false
		}
		if pre.Paused && !resumed {
			v = violation(InvPaused, "claim of %s by %s succeeded in a batch while paused", op.Task, op.Actor)
			return true
		}
		t, p := post.Task(op.Task), pre.Task(op.Task)
		if t == nil || p == nil {
			v = violation(InvClaim, "claim of unknown task %s succeeded", op.Task)
			return true
		}
		if v = claimBudget(post, p, op); v != nil {
			return true
		}
		if br := pre.Brief(t.BriefID); !cancelled && br != nil && br.Budget.MaxCostEUR > 0 && br.CostUsed >= br.Budget.MaxCostEUR+1e-9 {
			v = violation(InvBudget, "claim of %s in a batch while brief %s cost %.2f >= %.2f", t.ID, br.ID, br.CostUsed, br.Budget.MaxCostEUR)
			return true
		}
		return false
	})
	return v
}

func budgetBlocked(t *model.Task) bool {
	return t != nil && t.Status == model.StatusBlocked && strings.HasPrefix(deref(t.BlockedReason), "budget_exceeded")
}

func (c *Checker) checkLeases(st *step, pre, post *State) *Violation {
	op, res := st.op, st.res
	now := post.Now
	if op.Kind == OpRestart && op.Crash && res.Err == nil {
		for id, l := range c.leases {
			if t := post.Task(id); holds(t, l.agent) && t.LeaseExpiresAt != nil {
				c.leases[id] = lease{l.agent, *t.LeaseExpiresAt}
			}
		}
	}
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
			if res.Lease != nil && res.Lease.Before(post.Now.Add(c.lease)) {
				return violation(InvLeaseHonored, "heartbeat by %s returned lease %s, less than a full lease from %s", op.Actor, ts(res.Lease), ts(&post.Now))
			}
			if res.Lease != nil {
				for id, l := range c.leases {
					t := post.Task(id)
					if l.agent != op.Actor || (op.Task != "" && op.Task != id) || !holds(t, op.Actor) {
						continue
					}
					if t.LeaseExpiresAt.Before(l.expiry) || t.LeaseExpiresAt.Before(post.Now.Add(c.lease)) {
						return violation(InvLeaseHonored, "heartbeat by %s left %s with lease %s (was %s)", op.Actor, id, ts(t.LeaseExpiresAt), ts(&l.expiry))
					}
					c.leases[id] = lease{op.Actor, *t.LeaseExpiresAt}
				}
			}
		case OpRelease, OpSubmit, OpBlock, OpReassign, OpCancel, OpAcceptSplit:
			delete(c.leases, op.Task)
		case OpPauseAll, OpIncidentStart:
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

func (c *Checker) checkBatchLeases(st *step, _, post *State) *Violation {
	explained := func(id string) bool {
		return st.each(func(op Op, r Result) bool {
			switch op.Kind {
			case OpPauseAll:
				return r.Err == nil
			case OpRelease, OpSubmit, OpBlock, OpReassign, OpCancel, OpUpdate:
				return op.Task == id && (r.Err == nil || r.Code() == "budget_exceeded")
			}
			return false
		})
	}
	for id, l := range c.leases {
		t := post.Task(id)
		if !live(l, post.Now) || explained(id) {
			continue
		}
		if !holds(t, l.agent) {
			return violation(InvLeaseHonored, "task %s left %s's lease (valid until %s) in a batch, now %s/%s",
				id, l.agent, ts(&l.expiry), status(t), deref(t.Assignee))
		}
		if t.LeaseExpiresAt == nil || t.LeaseExpiresAt.Before(l.expiry) {
			return violation(InvLeaseHonored, "task %s lease of %s shrank from %s to %s in a batch", id, l.agent, ts(&l.expiry), ts(t.LeaseExpiresAt))
		}
	}
	c.leases = map[string]lease{}
	for _, t := range post.Tasks {
		if t.Status == model.StatusInProgress && t.Assignee != nil && t.LeaseExpiresAt != nil {
			c.leases[t.ID] = lease{*t.Assignee, *t.LeaseExpiresAt}
		}
	}
	return nil
}

func (c *Checker) checkRestart(st *step, pre, post *State) *Violation {
	if st.op.Kind != OpRestart || st.res.Err != nil {
		return nil
	}
	if ch := changed(pre, post); len(ch) > 0 {
		return violation(InvRestart, "tasks %v changed across restart", ch)
	}
	if len(st.added) > 0 {
		return violation(InvRestart, "restart appended %d audit events", len(st.added))
	}
	if pre.Paused != post.Paused {
		return violation(InvRestart, "paused %t → %t across restart", pre.Paused, post.Paused)
	}
	if !reflect.DeepEqual(pre.CapacityConfig(), post.CapacityConfig()) {
		return violation(InvRestart, "capacity %+v → %+v across restart", pre.CapacityConfig(), post.CapacityConfig())
	}
	pa := agentIDs(pre)
	for _, a := range post.Agents {
		if p := pa[a.ID]; p == nil || p.Status != a.Status || p.TokenHash != a.TokenHash {
			return violation(InvRestart, "agent %s changed across restart", a.ID)
		}
	}
	if len(pre.Agents) != len(post.Agents) {
		return violation(InvRestart, "agents %d → %d across restart", len(pre.Agents), len(post.Agents))
	}
	for _, t := range post.Tasks {
		p := pre.Task(t.ID)
		if t.Status != model.StatusInProgress {
			continue
		}
		if t.LeaseExpiresAt == nil {
			return violation(InvRestart, "task %s lost its lease across restart", t.ID)
		}
		if !st.op.Crash && !t.LeaseExpiresAt.Equal(*p.LeaseExpiresAt) {
			return violation(InvRestart, "task %s lease %s → %s across graceful restart", t.ID, ts(p.LeaseExpiresAt), ts(t.LeaseExpiresAt))
		}
	}
	if len(post.Subs) != 0 || post.Subscribers != 0 {
		return violation(InvRestart, "%d subscribers survived restart", post.Subscribers)
	}
	return nil
}

func (c *Checker) checkTokens(st *step, _, post *State) *Violation {
	rotated := map[string]bool{}
	st.each(func(op Op, r Result) bool {
		if op.Kind == OpRotate {
			rotated[op.Target] = true
		}
		return false
	})
	var v *Violation
	st.each(func(op Op, r Result) bool {
		if !op.Authenticated() || (st.op.Kind == OpBatch && rotated[op.Actor]) {
			return false
		}
		if a := post.Agent(op.Actor); a != nil && a.Status == model.AgentRevoked {
			return false
		}
		cur := c.tokens[op.Actor]
		if op.Token < cur && r.Code() != "unauthorized" {
			v = violation(InvToken, "%s by %s with token #%d accepted after rotation to #%d (%v)", op.Kind, op.Actor, op.Token, cur, r.Err)
			return true
		}
		if op.Token == cur && r.Code() == "unauthorized" {
			v = violation(InvToken, "%s by %s with current token #%d rejected", op.Kind, op.Actor, cur)
			return true
		}
		return false
	})
	if v != nil {
		return v
	}
	st.each(func(op Op, r Result) bool {
		if op.Kind == OpRotate && r.Err == nil {
			c.tokens[op.Target]++
		}
		for _, id := range r.Rotated {
			c.tokens[id]++
		}
		return false
	})
	for id, n := range c.tokens {
		if post.Tokens[id] != n {
			return violation(InvToken, "%s has token #%d, expected #%d", id, post.Tokens[id], n)
		}
	}
	return nil
}

func (c *Checker) checkReviewers(st *step, pre, post *State) *Violation {
	ag := agentIDs(post)
	var v *Violation
	st.each(func(op Op, r Result) bool {
		if r.Err != nil || (op.Kind != OpReview && op.Kind != OpAssignReviewer) {
			return false
		}
		p, t := pre.Task(op.Task), post.Task(op.Task)
		if p == nil || t == nil {
			return false
		}
		rev := op.Actor
		if op.Kind == OpAssignReviewer {
			rev = op.Target
		}
		if w, rv := ag[deref(p.Assignee)], ag[rev]; w != nil && rv != nil && w.Provider == rv.Provider {
			v = violation(InvReviewer, "%s by %s on %s: reviewer %s and worker %s share provider %s", op.Kind, op.Actor, t.ID, rev, w.ID, w.Provider)
			return true
		}
		if op.Kind == OpReview && p.Reviewer != nil && *p.Reviewer != op.Actor {
			v = violation(InvReviewer, "%s reviewed %s reserved for %s", op.Actor, t.ID, *p.Reviewer)
			return true
		}
		if op.Kind == OpReview && deref(t.Reviewer) != op.Actor {
			v = violation(InvReviewer, "%s reviewed %s but reviewer is %q", op.Actor, t.ID, deref(t.Reviewer))
			return true
		}
		return false
	})
	return v
}

func (c *Checker) checkRegistration(st *step, pre, post *State) *Violation {
	old, cur := agentIDs(pre), agentIDs(post)
	want := map[string]bool{}
	var v *Violation
	st.each(func(op Op, r Result) bool {
		if r.Err != nil || (op.Kind != OpRegister && op.Kind != OpBulk) {
			return false
		}
		for _, id := range op.bulkIDs() {
			if old[id] != nil || cur[id] == nil {
				v = violation(InvRegister, "%s %s succeeded but %s was not newly created", op.Kind, op.Target, id)
				return true
			}
			want[id] = true
		}
		return false
	})
	if v != nil {
		return v
	}
	for id, a := range cur {
		if old[id] == nil && !a.Spawned && !want[id] {
			return violation(InvRegister, "agent %s appeared without a successful registration", id)
		}
	}
	for id := range old {
		if cur[id] == nil {
			return violation(InvRegister, "agent %s disappeared", id)
		}
	}
	return nil
}

func (c *Checker) checkCapacity(st *step, pre, post *State) *Violation {
	var setOp *Op
	st.each(func(op Op, r Result) bool {
		if op.Kind == OpSetCapacity && r.Err == nil {
			setOp = &op
		}
		return false
	})
	if setOp != nil {
		if want := setOp.capacityConfig(); !reflect.DeepEqual(want, post.CapacityConfig()) {
			return violation(InvCapacity, "set_capacity %+v but capacity is %+v", want, post.CapacityConfig())
		}
	}
	known := map[string]bool{}
	for _, s := range pre.Spawns {
		known[s.ID] = true
	}
	var fresh []*model.Spawn
	for _, s := range post.Spawns {
		if !known[s.ID] {
			fresh = append(fresh, s)
		}
	}
	ag := agentIDs(post)
	busy := map[string]bool{}
	if st.op.Kind != OpBatch {
		for _, t := range post.Tasks {
			if t.Status == model.StatusInProgress {
				busy[deref(t.Assignee)] = true
			}
		}
	}
	cfg := pre.CapacityConfig()
	used := 0
	for _, e := range st.added {
		switch e.Tool {
		case "set_capacity":
			if setOp != nil {
				cfg = setOp.capacityConfig()
			}
		case "request_spawn":
			n := int(num(e.After["n"]))
			if used+n > len(fresh) {
				return violation(InvCapacity, "request_spawn audited %d spawns, only %d new", n, len(fresh)-used)
			}
			used += n
			later := map[string]bool{}
			for _, s := range fresh[used:] {
				later[s.AgentID] = true
			}
			u := capacity.Usage{}
			for _, a := range post.Agents {
				if a.Status == model.AgentRevoked || later[a.ID] {
					continue
				}
				if busy[a.ID] || (a.Spawned && post.Now.Sub(a.CreatedAt) < spawnGrace) {
					u[[2]string{a.Provider, a.Model}]++
				}
			}
			s := fresh[used-1]
			if a := ag[s.AgentID]; a != nil {
				if err := cfg.Check(a.Provider, a.Model, 0, u); err != nil {
					return violation(InvCapacity, "request_spawn of %d %s accepted beyond capacity %+v: %v", n, a.Provider, cfg, err)
				}
			}
		}
	}
	return nil
}

func (c *Checker) checkPlans(st *step, _, post *State) *Violation {
	decided := map[string]Kind{}
	var v *Violation
	st.each(func(op Op, r Result) bool {
		if r.Err != nil || (op.Kind != OpApprove && op.Kind != OpReject) {
			return false
		}
		if k, ok := decided[op.Target]; ok {
			v = violation(InvPlan, "plan %s both %s and %s", op.Target, k, op.Kind)
			return true
		}
		decided[op.Target] = op.Kind
		want := model.PlanApproved
		if op.Kind == OpReject {
			want = model.PlanRejected
		}
		for _, p := range post.Plans {
			if p.ID == op.Target && p.Status != want {
				v = violation(InvPlan, "%s on %s succeeded but plan is %s", op.Kind, p.ID, p.Status)
				return true
			}
		}
		return false
	})
	return v
}

func (c *Checker) checkSSE(_ *step, _, post *State) *Violation {
	for _, s := range post.Subs {
		if s.Disorder != "" {
			return violation(InvSSE, "subscriber %d %v: %s", s.ID, s.Topics, s.Disorder)
		}
		if s.Pending > events.SubscriberBuffer {
			return violation(InvSSE, "subscriber %d buffers %d events, max %d", s.ID, s.Pending, events.SubscriberBuffer)
		}
		if got := s.Received + s.Dropped + s.Pending; got != s.Expected {
			return violation(InvSSE, "subscriber %d %v: %d published, %d received + %d dropped + %d pending",
				s.ID, s.Topics, s.Expected, s.Received, s.Dropped, s.Pending)
		}
	}
	if post.Subscribers != len(post.Subs) {
		return violation(InvSSE, "hub reports %d subscribers, %d open", post.Subscribers, len(post.Subs))
	}
	return nil
}

func (c *Checker) checkState(st *step, _, post *State) *Violation {
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
				return violation(InvReview, "task %s has %d review rounds but is %s after %s", t.ID, t.ReviewRounds, t.Status, st.op.Kind)
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

func (c *Checker) checkLeaderScope(st *step, pre, _ *State) *Violation {
	var v *Violation
	st.each(func(op Op, r Result) bool {
		briefID := ""
		switch op.Kind {
		case OpAssign, OpAssignReviewer, OpReassign, OpAcceptSplit:
			if t := pre.Task(op.Task); t != nil {
				briefID = t.BriefID
			}
		case OpLeaderRead:
			briefID = op.Target
			if op.Text == "get_task" {
				if t := pre.Task(op.Task); t != nil {
					briefID = t.BriefID
				}
			}
		case OpPropose, OpSpawn:
			if len(pre.Briefs) > 0 {
				briefID = pre.Briefs[0].ID
			}
		}
		br := pre.Brief(briefID)
		if br == nil || br.LedBy(op.Actor) {
			return false
		}
		switch r.Code() {
		case "not_found", "unauthorized", "agent_paused":
			return false
		case "paused":
			if op.Kind == OpSpawn {
				return false
			}
		}
		v = violation(InvLeaderScope, "%s by %s on brief %s led by %s: got %q, want not_found",
			op.Kind, op.Actor, br.ID, deref(br.LeaderID), r.Code())
		return true
	})
	return v
}
