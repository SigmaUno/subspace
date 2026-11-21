package dst

import (
	"slices"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/mergequeue"
	"github.com/SigmaUno/subspace/internal/model"
)

func (c *Checker) checkServer(st *step, pre, post *State) *Violation {
	if post.Server == nil || pre.Server == nil {
		return nil
	}
	for _, f := range []func(Op, Result, *State, *State) *Violation{
		c.checkBackups, c.checkMerges, c.checkAutoEnqueue, c.checkAutopilot, c.checkWebhook, c.checkInstructions, c.checkSearch,
	} {
		if v := f(st.op, st.res, pre, post); v != nil {
			return v
		}
	}
	return nil
}

func (c *Checker) Final(post *State) *Violation {
	if v := c.checkFaults(nil, nil, post); v != nil {
		return v
	}
	if post.Server == nil {
		return nil
	}
	for key, n := range post.Server.Sent {
		if e := post.Server.Episodes[key]; n > e {
			return violation(InvNotify, "notification %s sent %d times for %d decisions", key, n, e)
		}
	}
	return nil
}

func (c *Checker) checkBackups(_ Op, _ Result, _, post *State) *Violation {
	if s := post.Server; s.Backups > s.BackupKeep {
		return violation(InvBackup, "%d backups kept, keep is %d", s.Backups, s.BackupKeep)
	}
	return nil
}

func reviewedAcrossProviders(s *State, t *model.Task) bool {
	if t.Reviewer == nil || t.Assignee == nil {
		return false
	}
	r, w := s.Agent(*t.Reviewer), s.Agent(*t.Assignee)
	return r != nil && w != nil && r.Provider != w.Provider
}

func mergeable(s *State, t *model.Task) bool {
	return t.Status == model.StatusApproved && (t.Tests == nil || t.Tests.Failed == 0) && reviewedAcrossProviders(s, t)
}

func (c *Checker) checkMerges(op Op, _ Result, pre, post *State) *Violation {
	pushes := post.Server.Pushes
	for i := len(pre.Server.Pushes); i < len(pushes); i++ {
		p := pushes[i]
		switch {
		case p.Paused:
			return violation(InvMerge, "%s merged while paused", p.Task)
		case p.Status != model.StatusApproved:
			return violation(InvMerge, "%s merged while %s", p.Task, p.Status)
		case p.Failed > 0:
			return violation(InvMerge, "%s merged with %d failing tests", p.Task, p.Failed)
		case p.Reviewer == "" || p.ReviewerProvider == "" || p.ReviewerProvider == p.WorkerProvider:
			return violation(InvMerge, "%s merged with reviewer %q on %q, worker on %q", p.Task, p.Reviewer, p.ReviewerProvider, p.WorkerProvider)
		}
		for _, q := range pushes[:i] {
			if q.Task == p.Task {
				return violation(InvMerge, "%s merged twice", p.Task)
			}
		}
		if t := post.Task(p.Task); t == nil || t.Status != model.StatusDone {
			return violation(InvMerge, "%s merged but is %s after %s", p.Task, status(t), op.Kind)
		}
	}
	return nil
}

func (c *Checker) checkAutoEnqueue(op Op, _ Result, _, post *State) *Violation {
	if op.Kind != OpMergeTick || !post.Server.AutoMerge || post.Paused {
		return nil
	}
	for _, t := range post.Tasks {
		if !mergeable(post, t) {
			continue
		}
		if !slices.ContainsFunc(post.Server.Candidates, func(c2 *mergequeue.Candidate) bool { return c2.TaskID == t.ID }) {
			return violation(InvAutoEnqueue, "approved task %s not enqueued after merge tick with auto-merge on", t.ID)
		}
	}
	return nil
}

func (c *Checker) checkAutopilot(op Op, res Result, pre, post *State) *Violation {
	if op.Kind != OpAutopilotTick {
		return nil
	}
	if res.Actions > 0 && pre.Paused {
		return violation(InvAutopilot, "autopilot took %d actions while paused", res.Actions)
	}
	if res.Actions > 0 && !post.Server.Autopilot {
		return violation(InvAutopilot, "autopilot took %d actions while disabled", res.Actions)
	}
	if res.Actions > 0 && !slices.ContainsFunc(pre.Briefs, func(b *model.Brief) bool { return b.LeaderID == nil }) {
		return violation(InvLeaderScope, "autopilot took %d actions although every brief has a leader", res.Actions)
	}
	if res.Spawned == 0 {
		return nil
	}
	cv := post.Server.Capacity
	if cv.GlobalMaxAgents > 0 && cv.AgentsInUse > cv.GlobalMaxAgents {
		return violation(InvAutopilot, "%d agents in use after autopilot spawn, global max %d", cv.AgentsInUse, cv.GlobalMaxAgents)
	}
	for _, l := range cv.Limits {
		if l.MaxConcurrency > 0 && l.InUse > l.MaxConcurrency {
			return violation(InvAutopilot, "%s/%s has %d in use after autopilot spawn, max %d", l.Provider, l.Model, l.InUse, l.MaxConcurrency)
		}
	}
	for _, br := range post.Briefs {
		if br.Budget.MaxParallelAgent == 0 {
			continue
		}
		act, busy := map[string]bool{}, map[string]bool{}
		for _, t := range post.Tasks {
			switch {
			case t.Assignee == nil:
			case t.Status == model.StatusInProgress:
				busy[*t.Assignee] = true
				if t.BriefID == br.ID {
					act[*t.Assignee] = true
				}
			case t.Status == model.StatusAssigned && t.BriefID == br.ID:
				act[*t.Assignee] = true
			}
		}
		pending := 0
		for _, sp := range post.Server.Spawns {
			if sp.BriefID == br.ID && sp.Status != "failed" && post.Now.Sub(sp.CreatedAt) < 10*time.Minute && !act[sp.AgentID] && !busy[sp.AgentID] {
				pending++
			}
		}
		if len(act)+pending > br.Budget.MaxParallelAgent {
			return violation(InvAutopilot, "brief %s has %d active and %d pending agents after autopilot spawn, max %d",
				br.ID, len(act), pending, br.Budget.MaxParallelAgent)
		}
	}
	return nil
}

func (c *Checker) checkWebhook(op Op, res Result, pre, post *State) *Violation {
	switch op.Kind {
	case OpWebhook:
		if op.Forged && res.Err == nil {
			return violation(InvWebhook, "forged delivery accepted")
		}
		if !op.Forged && res.Err != nil {
			return violation(InvWebhook, "signed %s delivery for %s rejected: %v", op.Target, op.Task, res.Err)
		}
	case OpWebhookRace:
		if res.Err != nil {
			return violation(InvWebhook, "merged delivery racing mark_done failed: %v", res.Err)
		}
		if p := pre.Task(op.Task); p != nil && p.Status == model.StatusApproved {
			if t := post.Task(op.Task); t == nil || t.Status != model.StatusDone {
				return violation(InvWebhook, "%s is %s after merged delivery raced mark_done", op.Task, status(t))
			}
		}
	}
	return nil
}

func (c *Checker) checkInstructions(op Op, res Result, _, post *State) *Violation {
	key := op.Target
	hist := c.instr[key]
	switch op.Kind {
	case OpInstrSave:
		if res.Err != nil {
			return violation(InvInstr, "save %s failed: %v", key, res.Err)
		}
		c.instr[key] = append(hist, op.Text)
	case OpInstrRollback:
		valid := op.N >= 1 && op.N <= len(hist) && op.N != len(hist)
		if valid != (res.Err == nil) {
			return violation(InvInstr, "rollback %s to v%d of %d: err=%v", key, op.N, len(hist), res.Err)
		}
		if valid {
			c.instr[key] = append(hist, hist[op.N-1])
		}
	}
	for k, h := range post.Server.Instr {
		want := c.instr[k]
		if h.Version != len(want) || (len(want) > 0 && h.Text != want[len(want)-1]) {
			return violation(InvInstr, "%s at v%d %q, want v%d", k, h.Version, h.Text, len(want))
		}
	}
	for k, want := range c.instr {
		if _, ok := post.Server.Instr[k]; !ok && len(want) > 0 {
			return violation(InvInstr, "%s has no version, want v%d", k, len(want))
		}
	}
	return nil
}

func (c *Checker) checkSearch(op Op, res Result, pre, _ *State) *Violation {
	if op.Kind != OpSearch {
		return nil
	}
	if res.Err != nil {
		return violation(InvSearch, "search %q failed: %v", op.Text, res.Err)
	}
	q := strings.ToLower(op.Text)
	var want []string
	switch op.Target {
	case "task":
		for _, t := range pre.Tasks {
			if strings.Contains(strings.ToLower(t.Title), q) || strings.Contains(strings.ToLower(t.Spec), q) {
				want = append(want, t.ID)
			}
		}
	case "agent":
		for _, a := range pre.Agents {
			if strings.Contains(strings.ToLower(a.ID), q) || strings.Contains(strings.ToLower(a.Model), q) {
				want = append(want, a.ID)
			}
		}
	}
	got := append([]string(nil), res.Hits...)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		return violation(InvSearch, "search %s %q returned %v, want %v", op.Target, op.Text, got, want)
	}
	return nil
}
