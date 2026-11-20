package dst

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
)

var edges = map[[2]string]struct{}{
	{model.StatusBacklog, model.StatusReady}:               {},
	{model.StatusReady, model.StatusReady}:                 {},
	{model.StatusReady, model.StatusAssigned}:              {},
	{model.StatusAssigned, model.StatusAssigned}:           {},
	{model.StatusAssigned, model.StatusInProgress}:         {},
	{model.StatusInProgress, model.StatusAssigned}:         {},
	{model.StatusInProgress, model.StatusBlocked}:          {},
	{model.StatusInProgress, model.StatusSubmitted}:        {},
	{model.StatusBlocked, model.StatusBlocked}:             {},
	{model.StatusBlocked, model.StatusReady}:               {},
	{model.StatusBlocked, model.StatusAssigned}:            {},
	{model.StatusSubmitted, model.StatusInReview}:          {},
	{model.StatusSubmitted, model.StatusApproved}:          {},
	{model.StatusSubmitted, model.StatusChangesRequested}:  {},
	{model.StatusInReview, model.StatusInReview}:           {},
	{model.StatusInReview, model.StatusApproved}:           {},
	{model.StatusInReview, model.StatusChangesRequested}:   {},
	{model.StatusChangesRequested, model.StatusInProgress}: {},
	{model.StatusApproved, model.StatusDone}:               {},
	{model.StatusNeedsOwner, model.StatusReady}:            {},
	{model.StatusNeedsOwner, model.StatusAssigned}:         {},
	{model.StatusCancelled, model.StatusReady}:             {},
	{model.StatusCancelled, model.StatusAssigned}:          {},
}

func allowed(from, to string) bool {
	switch {
	case from == model.StatusDone:
		return false
	case to == model.StatusCancelled:
		return from != model.StatusCancelled
	case to == model.StatusNeedsOwner:
		return from != model.StatusCancelled && from != model.StatusNeedsOwner
	}
	_, ok := edges[[2]string{from, to}]
	return ok
}

func (s *sim) fetchAudit(since int64) ([]model.AuditEvent, []json.RawMessage, error) {
	var evs []model.AuditEvent
	var raws []json.RawMessage
	for {
		var out struct {
			Events []json.RawMessage `json:"events"`
		}
		code, ec, err := s.c.rest(s.ctx, "GET", fmt.Sprintf("/api/v1/audit?since=%d&limit=1000", since), nil, &out)
		if err != nil {
			return nil, nil, err
		}
		if code != 200 {
			return nil, nil, fmt.Errorf("audit: %d %s", code, ec)
		}
		for _, r := range out.Events {
			var e model.AuditEvent
			if err := json.Unmarshal(r, &e); err != nil {
				return nil, nil, err
			}
			evs, raws = append(evs, e), append(raws, r)
			since = max(since, e.ID)
		}
		if len(out.Events) < 1000 {
			return evs, raws, nil
		}
	}
}

func (s *sim) snapshot() error {
	var tl struct {
		Tasks []*model.Task `json:"tasks"`
	}
	var al struct {
		Agents []*model.Agent `json:"agents"`
	}
	var bl struct {
		Briefs []*model.Brief `json:"briefs"`
	}
	for _, q := range []struct {
		path string
		out  any
	}{{"/api/v1/tasks?limit=1000", &tl}, {"/api/v1/agents?limit=1000", &al}, {"/api/v1/briefs?limit=1000", &bl}} {
		code, ec, err := s.c.rest(s.ctx, "GET", q.path, nil, q.out)
		if err != nil {
			return err
		}
		if code != 200 {
			return fmt.Errorf("GET %s: %d %s", q.path, code, ec)
		}
	}
	slices.SortFunc(tl.Tasks, func(a, b *model.Task) int { return taskNum(a.ID) - taskNum(b.ID) })
	s.tasks, s.taskBy = tl.Tasks, map[string]*model.Task{}
	for _, t := range s.tasks {
		s.taskBy[t.ID] = t
	}
	slices.SortFunc(bl.Briefs, func(a, b *model.Brief) int { return cmp.Or(len(a.ID)-len(b.ID), strings.Compare(a.ID, b.ID)) })
	s.briefs, s.briefBy = bl.Briefs, map[string]*model.Brief{}
	for _, b := range s.briefs {
		s.briefBy[b.ID] = b
	}
	for _, sa := range al.Agents {
		a := s.by[sa.ID]
		if a == nil {
			return s.violation("agent_status", "unknown agent %s", sa.ID)
		}
		if a.status != sa.Status {
			return s.violation("agent_status", "%s is %s, simulation expected %s", a.id, sa.Status, a.status)
		}
	}
	return nil
}

func (s *sim) check() error {
	var last int64
	if n := len(s.auditIDs); n > 0 {
		last = s.auditIDs[n-1]
	}
	evs, raws, err := s.fetchAudit(last)
	if err != nil {
		return err
	}
	if err := s.snapshot(); err != nil {
		return err
	}
	for i, e := range evs {
		if e.ID <= last {
			return s.violation("audit_order", "audit id %d after %d", e.ID, last)
		}
		last = e.ID
		s.auditIDs, s.auditRaw = append(s.auditIDs, e.ID), append(s.auditRaw, raws[i])
		if err := s.apply(e); err != nil {
			return err
		}
	}
	now := s.clock.Now()
	holders := map[string]map[string]bool{}
	for _, t := range s.tasks {
		if got, ok := s.replay[t.ID]; !ok || got != t.Status {
			return s.violation("unaudited_status", "%s is %s but its audit history ends at %q", t.ID, t.Status, got)
		}
		if t.ReviewRounds > board.MaxReviewRounds ||
			t.ReviewRounds == board.MaxReviewRounds && t.Status != model.StatusNeedsOwner && t.Status != model.StatusCancelled {
			return s.violation("review_rounds", "%s has %d review rounds and is %s", t.ID, t.ReviewRounds, t.Status)
		}
		if t.Status != model.StatusInProgress {
			continue
		}
		a := s.by[deref(t.Assignee)]
		switch {
		case a == nil:
			return s.violation("lease", "%s is in_progress without an assignee", t.ID)
		case a.status != model.AgentActive:
			return s.violation("lease", "%s is held by %s agent %s", t.ID, a.status, a.id)
		case t.LeaseExpiresAt == nil:
			return s.violation("lease", "%s is in_progress without a lease", t.ID)
		case t.LeaseExpiresAt.Before(now):
			return s.violation("lease", "%s lease expired at %s, now %s", t.ID, t.LeaseExpiresAt.Format("15:04:05"), now.Format("15:04:05"))
		case s.paused:
			return s.violation("pause", "%s is in_progress during pause_all", t.ID)
		case t.Budget.MaxCostEUR > 0 && t.CostUsed >= t.Budget.MaxCostEUR:
			return s.violation("task_budget", "%s in_progress with cost %.2f of %.2f", t.ID, t.CostUsed, t.Budget.MaxCostEUR)
		case t.Budget.MaxMinutes > 0 && t.MinutesUsed >= t.Budget.MaxMinutes:
			return s.violation("task_budget", "%s in_progress with %d of %d minutes", t.ID, t.MinutesUsed, t.Budget.MaxMinutes)
		}
		if holders[t.BriefID] == nil {
			holders[t.BriefID] = map[string]bool{}
		}
		holders[t.BriefID][a.id] = true
	}
	for _, b := range s.briefs {
		if n := len(holders[b.ID]); b.Budget.MaxParallelAgent > 0 && n > b.Budget.MaxParallelAgent {
			return s.violation("brief_parallel", "%s has %d agents working, limit %d", b.ID, n, b.Budget.MaxParallelAgent)
		}
	}
	return nil
}

func (s *sim) apply(e model.AuditEvent) error {
	switch e.Tool {
	case "propose_plan", "approve_plan", "reject_plan":
		pid, _ := e.After["plan_id"].(string)
		brief := ""
		for _, t := range s.tasks {
			if t.PlanID == pid {
				brief = t.BriefID
			}
		}
		for _, t := range s.tasks {
			cur, ok := s.replay[t.ID]
			if t.PlanID != pid {
				if e.Tool == "propose_plan" && t.BriefID == brief && t.PlanID != "" && cur == model.StatusBacklog {
					s.replay[t.ID] = model.StatusCancelled
				}
				continue
			}
			switch {
			case e.Tool == "propose_plan" && !ok:
				s.replay[t.ID] = model.StatusBacklog
			case e.Tool == "approve_plan" && cur == model.StatusBacklog:
				s.replay[t.ID] = model.StatusReady
			case e.Tool == "reject_plan" && cur == model.StatusBacklog:
				s.replay[t.ID] = model.StatusCancelled
			}
		}
		return nil
	case "create_task":
		if e.TaskID != nil {
			s.replay[*e.TaskID], _ = e.After["status"].(string)
		}
		return nil
	}
	if e.TaskID == nil {
		return nil
	}
	from, ok1 := e.Before["status"].(string)
	to, ok2 := e.After["status"].(string)
	if !ok1 || !ok2 {
		return nil
	}
	id := *e.TaskID
	if cur := s.replay[id]; cur != from {
		return s.violation("audit_chain", "audit #%d moves %s %s → %s but it was %s", e.ID, id, from, to, cur)
	}
	if to == model.StatusDone && from != model.StatusApproved {
		return s.violation("done_without_approval", "audit #%d moves %s %s → done", e.ID, id, from)
	}
	if !allowed(from, to) {
		return s.violation("transition", "audit #%d moves %s %s → %s via %s", e.ID, id, from, to, e.Tool)
	}
	switch {
	case to == model.StatusSubmitted && e.Tool != "submit",
		to == model.StatusApproved && e.Tool != "review",
		to == model.StatusDone && e.Role != model.RoleOwner:
		return s.violation("transition", "audit #%d: %s (%s) moved %s to %s via %s", e.ID, e.AgentID, e.Role, id, to, e.Tool)
	}
	s.replay[id] = to
	t := s.taskBy[id]
	if t == nil {
		return s.violation("audit_chain", "audit #%d refers to unknown task %s", e.ID, id)
	}
	reviewer := ""
	switch {
	case e.Tool == "review":
		reviewer = e.AgentID
	case e.Tool == "assign" && to == model.StatusInReview:
		reviewer = deref(t.Reviewer)
	}
	if reviewer != "" && s.providerOf(reviewer) == s.providerOf(deref(t.Assignee)) {
		return s.violation("reviewer_provider", "audit #%d: reviewer %s and worker %s of %s share provider %s", e.ID, reviewer, deref(t.Assignee), id, s.providerOf(reviewer))
	}
	return nil
}

func (s *sim) verifyAudit() error {
	_, raws, err := s.fetchAudit(0)
	if err != nil {
		return err
	}
	if len(raws) != len(s.auditRaw) {
		return s.violation("audit_rewritten", "audit log has %d events, %d were seen", len(raws), len(s.auditRaw))
	}
	for i := range raws {
		if !bytes.Equal(raws[i], s.auditRaw[i]) {
			return s.violation("audit_rewritten", "audit event %d changed:\n    was %s\n    now %s", s.auditIDs[i], s.auditRaw[i], raws[i])
		}
	}
	return nil
}
