package insights

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

const (
	SeverityWarning = "warning"
	SeverityHigh    = "high"
)

const (
	RuleRepeatedErrors  = "repeated_errors"
	RuleUnassignedClaim = "unassigned_claims"
	RuleLeaseExpiries   = "lease_expiries"
	RuleSilence         = "silence"
	RuleChangesRatio    = "changes_requested_ratio"
	RuleLoopTrips       = "loop_trips"
	RuleHighCost        = "high_cost"
)

const (
	EvidenceError   = "error"
	EvidenceAudit   = "audit"
	EvidenceMessage = "message"
	EvidenceTask    = "task"
)

const (
	loopPausePrefix = "Agent paused: loop"
	expiryNote      = "lease expired"
	claimTool       = "claim_task"
	notAssigned     = "not_assigned"
)

type Tier struct {
	Warning int `json:"warning"`
	High    int `json:"high"`
}

type Thresholds struct {
	Window        time.Duration
	RepeatedError Tier
	Unassigned    Tier
	LeaseExpiries Tier
	LoopTrips     Tier
	Silence       time.Duration
	SilenceHigh   time.Duration
	MinReviews    int
	ChangesRatio  float64
	ChangesHigh   float64
	MinCostTasks  int
	MinCostPeers  int
	CostFactor    float64
	CostHigh      float64
}

func DefaultThresholds() Thresholds {
	return Thresholds{
		Window:        24 * time.Hour,
		RepeatedError: Tier{Warning: 3, High: 6},
		Unassigned:    Tier{Warning: 3, High: 6},
		LeaseExpiries: Tier{Warning: 2, High: 3},
		LoopTrips:     Tier{Warning: 1, High: 2},
		Silence:       20 * time.Minute,
		SilenceHigh:   time.Hour,
		MinReviews:    3,
		ChangesRatio:  0.5,
		ChangesHigh:   0.75,
		MinCostTasks:  2,
		MinCostPeers:  2,
		CostFactor:    2,
		CostHigh:      3,
	}
}

type ToolError struct {
	ID          int64     `json:"id"`
	TS          time.Time `json:"ts"`
	AgentID     string    `json:"agent_id"`
	Tool        string    `json:"tool"`
	Code        string    `json:"code"`
	TaskID      string    `json:"task_id"`
	Fingerprint string    `json:"fingerprint"`
}

type Input struct {
	Now      time.Time
	Agents   []*model.Agent
	Tasks    []*model.Task
	Audit    []*model.AuditEvent
	Errors   []ToolError
	Messages []*model.Message
	Limits   Thresholds
}

type Evidence struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type Finding struct {
	ID       string     `json:"id"`
	AgentID  string     `json:"agent_id"`
	Rule     string     `json:"rule"`
	Severity string     `json:"severity"`
	Summary  string     `json:"summary"`
	Count    int        `json:"count"`
	TaskIDs  []string   `json:"task_ids"`
	Evidence []Evidence `json:"evidence"`
	Since    time.Time  `json:"since"`
}

func Analyze(in Input) []Finding {
	if in.Limits == (Thresholds{}) {
		in.Limits = DefaultThresholds()
	}
	var out []Finding
	for _, a := range []func(Input) []Finding{
		repeatedErrors, unassignedClaims, leaseExpiries, silence, changesRatio, loopTrips, highCost,
	} {
		out = append(out, a(in)...)
	}
	for i := range out {
		out[i].ID = out[i].AgentID + ":" + out[i].Rule
		slices.Sort(out[i].TaskIDs)
		out[i].TaskIDs = slices.Compact(out[i].TaskIDs)
		if out[i].TaskIDs == nil {
			out[i].TaskIDs = []string{}
		}
	}
	slices.SortFunc(out, func(a, b Finding) int {
		return cmp.Or(
			cmp.Compare(severityRank(b.Severity), severityRank(a.Severity)),
			cmp.Compare(a.AgentID, b.AgentID),
			cmp.Compare(a.Rule, b.Rule),
		)
	})
	return out
}

func severityRank(s string) int {
	if s == SeverityHigh {
		return 2
	}
	return 1
}

func tiered(n int, t Tier) string {
	switch {
	case t.High > 0 && n >= t.High:
		return SeverityHigh
	case t.Warning > 0 && n >= t.Warning:
		return SeverityWarning
	}
	return ""
}

func recent(in Input, ts time.Time) bool {
	return in.Limits.Window <= 0 || !ts.Before(in.Now.Add(-in.Limits.Window))
}

func repeatedErrors(in Input) []Finding {
	type key struct{ agent, tool, code, fp string }
	groups := map[key][]ToolError{}
	for _, e := range in.Errors {
		if recent(in, e.TS) {
			k := key{e.AgentID, e.Tool, e.Code, e.Fingerprint}
			groups[k] = append(groups[k], e)
		}
	}
	keys := make([]key, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b key) int {
		return cmp.Or(cmp.Compare(a.agent, b.agent), cmp.Compare(len(groups[b]), len(groups[a])),
			cmp.Compare(a.tool, b.tool), cmp.Compare(a.code, b.code), cmp.Compare(a.fp, b.fp))
	})
	var out []Finding
	seen := map[string]bool{}
	for _, k := range keys {
		es := groups[k]
		sev := tiered(len(es), in.Limits.RepeatedError)
		if sev == "" || seen[k.agent] {
			continue
		}
		seen[k.agent] = true
		f := Finding{
			AgentID: k.agent, Rule: RuleRepeatedErrors, Severity: sev, Count: len(es), Since: es[0].TS,
			Summary: fmt.Sprintf("%s failed %d times with the identical error %s", k.tool, len(es), k.code),
		}
		for _, e := range es {
			f.Evidence = append(f.Evidence, Evidence{EvidenceError, strconv.FormatInt(e.ID, 10)})
			if e.TaskID != "" {
				f.TaskIDs = append(f.TaskIDs, e.TaskID)
			}
			f.Since = minTime(f.Since, e.TS)
		}
		out = append(out, f)
	}
	return out
}

func unassignedClaims(in Input) []Finding {
	byAgent := map[string][]ToolError{}
	for _, e := range in.Errors {
		if e.Tool == claimTool && e.Code == notAssigned && recent(in, e.TS) {
			byAgent[e.AgentID] = append(byAgent[e.AgentID], e)
		}
	}
	var out []Finding
	for agent, es := range byAgent {
		sev := tiered(len(es), in.Limits.Unassigned)
		if sev == "" {
			continue
		}
		f := Finding{
			AgentID: agent, Rule: RuleUnassignedClaim, Severity: sev, Count: len(es), Since: es[0].TS,
			Summary: fmt.Sprintf("%d claim attempts on tasks not assigned to the agent", len(es)),
		}
		for _, e := range es {
			f.Evidence = append(f.Evidence, Evidence{EvidenceError, strconv.FormatInt(e.ID, 10)})
			if e.TaskID != "" {
				f.TaskIDs = append(f.TaskIDs, e.TaskID)
			}
			f.Since = minTime(f.Since, e.TS)
		}
		out = append(out, f)
	}
	return out
}

func sortedAudit(in Input) []*model.AuditEvent {
	evs := slices.Clone(in.Audit)
	slices.SortStableFunc(evs, func(a, b *model.AuditEvent) int {
		return cmp.Or(a.TS.Compare(b.TS), cmp.Compare(a.ID, b.ID))
	})
	return evs
}

func leaseExpiries(in Input) []Finding {
	holder := map[string]string{}
	for _, t := range in.Tasks {
		if t.Assignee != nil {
			holder[t.ID] = *t.Assignee
		}
	}
	byAgent := map[string][]*model.AuditEvent{}
	for _, e := range sortedAudit(in) {
		if e.TaskID == nil {
			continue
		}
		if e.Tool == claimTool {
			holder[*e.TaskID] = e.AgentID
		}
		if e.Tool == "lease_release" && e.Note != nil && *e.Note == expiryNote && recent(in, e.TS) {
			if h := holder[*e.TaskID]; h != "" {
				byAgent[h] = append(byAgent[h], e)
			}
		}
	}
	var out []Finding
	for agent, es := range byAgent {
		sev := tiered(len(es), in.Limits.LeaseExpiries)
		if sev == "" {
			continue
		}
		f := Finding{
			AgentID: agent, Rule: RuleLeaseExpiries, Severity: sev, Count: len(es), Since: es[0].TS,
			Summary: fmt.Sprintf("%d leases expired while the agent held the task", len(es)),
		}
		for _, e := range es {
			f.Evidence = append(f.Evidence, Evidence{EvidenceAudit, strconv.FormatInt(e.ID, 10)})
			f.TaskIDs = append(f.TaskIDs, *e.TaskID)
		}
		out = append(out, f)
	}
	return out
}

func silence(in Input) []Finding {
	agents := map[string]*model.Agent{}
	for _, a := range in.Agents {
		agents[a.ID] = a
	}
	byAgent := map[string]*Finding{}
	for _, t := range in.Tasks {
		if t.Status != model.StatusInProgress || t.Assignee == nil {
			continue
		}
		a := agents[*t.Assignee]
		if a == nil || a.Status != model.AgentActive {
			continue
		}
		last := t.UpdatedAt
		if t.LastHeartbeatAt != nil && t.LastHeartbeatAt.After(last) {
			last = *t.LastHeartbeatAt
		}
		if a.LastSeenAt != nil && a.LastSeenAt.After(last) {
			last = *a.LastSeenAt
		}
		quiet := in.Now.Sub(last)
		if quiet < in.Limits.Silence {
			continue
		}
		f := byAgent[a.ID]
		if f == nil {
			f = &Finding{AgentID: a.ID, Rule: RuleSilence, Severity: SeverityWarning, Since: last}
			byAgent[a.ID] = f
		}
		if in.Limits.SilenceHigh > 0 && quiet >= in.Limits.SilenceHigh {
			f.Severity = SeverityHigh
		}
		f.Count++
		f.Since = minTime(f.Since, last)
		f.TaskIDs = append(f.TaskIDs, t.ID)
		f.Evidence = append(f.Evidence, Evidence{EvidenceTask, t.ID})
	}
	var out []Finding
	for _, f := range byAgent {
		f.Summary = fmt.Sprintf("silent for %s while in progress on %s", in.Now.Sub(f.Since).Truncate(time.Minute), strings.Join(sortedUnique(f.TaskIDs), ", "))
		out = append(out, *f)
	}
	return out
}

func changesRatio(in Input) []Finding {
	assignee := map[string]string{}
	for _, t := range in.Tasks {
		if t.Assignee != nil {
			assignee[t.ID] = *t.Assignee
		}
	}
	type tally struct {
		total, changes int
		evidence       []Evidence
		tasks          []string
		since          time.Time
	}
	by := map[string]*tally{}
	for _, e := range sortedAudit(in) {
		if e.Tool != "review" || e.TaskID == nil || !recent(in, e.TS) {
			continue
		}
		agent := assignee[*e.TaskID]
		if agent == "" {
			continue
		}
		to, _ := e.After["status"].(string)
		if to != model.StatusApproved && to != model.StatusChangesRequested {
			continue
		}
		t := by[agent]
		if t == nil {
			t = &tally{since: e.TS}
			by[agent] = t
		}
		t.total++
		if to == model.StatusChangesRequested {
			t.changes++
			t.evidence = append(t.evidence, Evidence{EvidenceAudit, strconv.FormatInt(e.ID, 10)})
			t.tasks = append(t.tasks, *e.TaskID)
		}
	}
	var out []Finding
	for agent, t := range by {
		if t.total < in.Limits.MinReviews {
			continue
		}
		ratio := float64(t.changes) / float64(t.total)
		sev := ""
		switch {
		case in.Limits.ChangesHigh > 0 && ratio >= in.Limits.ChangesHigh:
			sev = SeverityHigh
		case ratio >= in.Limits.ChangesRatio:
			sev = SeverityWarning
		default:
			continue
		}
		out = append(out, Finding{
			AgentID: agent, Rule: RuleChangesRatio, Severity: sev, Count: t.changes, Since: t.since,
			Summary:  fmt.Sprintf("changes requested in %d of %d reviews (%.0f%%)", t.changes, t.total, ratio*100),
			TaskIDs:  t.tasks,
			Evidence: t.evidence,
		})
	}
	return out
}

func loopTrips(in Input) []Finding {
	by := map[string][]*model.Message{}
	for _, m := range in.Messages {
		if m.ThreadKind == model.ThreadAgent && m.Kind == model.MsgSystem && strings.HasPrefix(m.Body, loopPausePrefix) && recent(in, m.CreatedAt) {
			by[m.ThreadID] = append(by[m.ThreadID], m)
		}
	}
	var out []Finding
	for agent, ms := range by {
		sev := tiered(len(ms), in.Limits.LoopTrips)
		if sev == "" {
			continue
		}
		f := Finding{
			AgentID: agent, Rule: RuleLoopTrips, Severity: sev, Count: len(ms), Since: ms[0].CreatedAt,
			Summary: fmt.Sprintf("loop protection paused the agent %d times", len(ms)),
		}
		for _, m := range ms {
			f.Evidence = append(f.Evidence, Evidence{EvidenceMessage, strconv.FormatInt(m.ID, 10)})
			f.Since = minTime(f.Since, m.CreatedAt)
		}
		out = append(out, f)
	}
	return out
}

func highCost(in Input) []Finding {
	type spend struct {
		cost  float64
		tasks []*model.Task
	}
	by := map[string]*spend{}
	roles := map[string]string{}
	for _, a := range in.Agents {
		roles[a.ID] = a.Role
	}
	for _, t := range in.Tasks {
		if t.Assignee == nil || (t.Status != model.StatusDone && t.Status != model.StatusApproved) {
			continue
		}
		s := by[*t.Assignee]
		if s == nil {
			s = &spend{}
			by[*t.Assignee] = s
		}
		s.cost += t.CostUsed
		s.tasks = append(s.tasks, t)
	}
	avg := map[string]float64{}
	for id, s := range by {
		if len(s.tasks) >= in.Limits.MinCostTasks {
			avg[id] = s.cost / float64(len(s.tasks))
		}
	}
	var out []Finding
	for id, mine := range avg {
		var peers []float64
		for other, v := range avg {
			if other != id && roles[other] == roles[id] {
				peers = append(peers, v)
			}
		}
		if len(peers) < in.Limits.MinCostPeers {
			continue
		}
		slices.Sort(peers)
		med := median(peers)
		if med <= 0 || mine < med*in.Limits.CostFactor {
			continue
		}
		sev := SeverityWarning
		if in.Limits.CostHigh > 0 && mine >= med*in.Limits.CostHigh {
			sev = SeverityHigh
		}
		f := Finding{
			AgentID: id, Rule: RuleHighCost, Severity: sev, Count: len(by[id].tasks),
			Summary: fmt.Sprintf("%.2f EUR per task, %.1fx the peer median of %.2f EUR", mine, mine/med, med),
		}
		for _, t := range by[id].tasks {
			f.TaskIDs = append(f.TaskIDs, t.ID)
			f.Evidence = append(f.Evidence, Evidence{EvidenceTask, t.ID})
			if f.Since.IsZero() || t.UpdatedAt.Before(f.Since) {
				f.Since = t.UpdatedAt
			}
		}
		out = append(out, f)
	}
	return out
}

func median(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func sortedUnique(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return slices.Compact(s)
}
