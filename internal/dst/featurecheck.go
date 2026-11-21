package dst

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/estimate"
	"github.com/SigmaUno/subspace/internal/gates"
	"github.com/SigmaUno/subspace/internal/incident"
	"github.com/SigmaUno/subspace/internal/knowledge"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/quota"
	"github.com/SigmaUno/subspace/internal/schedule"
)

const costSlack = 1e-6

type quotaDay struct {
	agent string
	day   int64
}

type annModel struct {
	id      string
	brief   string
	created time.Time
	expires time.Time
	deleted bool
}

type entryModel struct {
	brief  string
	status string
}

type schedModel struct {
	catchUp   string
	maxActive int
}

type featureModel struct {
	limits     map[string]quota.Quota
	cost       map[quotaDay]float64
	claimed    map[quotaDay]map[string]bool
	anns       []*annModel
	entries    map[string]*entryModel
	schedules  map[string]schedModel
	ran        map[string]bool
	runTasks   map[string][]string
	overrides  map[string][]string
	incident   string
	incDone    map[string]bool
	claims     map[string]int
	reassigned map[string]bool
	assignees  map[string][]string
}

func newFeatureModel() *featureModel {
	return &featureModel{limits: map[string]quota.Quota{}, cost: map[quotaDay]float64{}, claimed: map[quotaDay]map[string]bool{},
		entries: map[string]*entryModel{}, schedules: map[string]schedModel{}, ran: map[string]bool{}, runTasks: map[string][]string{},
		overrides: map[string][]string{}, incDone: map[string]bool{}, claims: map[string]int{}, reassigned: map[string]bool{},
		assignees: map[string][]string{}}
}

func (c *Checker) checkFeatures(st *step, pre, post *State) *Violation {
	if post.Server == nil || pre.Server == nil {
		return nil
	}
	m := c.fm
	for _, f := range []func(*step, *State, *State) *Violation{
		m.checkQuota, m.checkAnnouncements, m.checkContext, m.checkLeaderReads, m.checkSplit, m.checkKnowledge,
		m.checkEstimate, m.checkIncident, m.checkSchedules, m.checkGates,
	} {
		if v := f(st, pre, post); v != nil {
			return v
		}
	}
	m.learn(st, pre, post)
	return nil
}

func (m *featureModel) learn(st *step, pre, post *State) {
	for _, e := range st.added {
		if e.TaskID == nil {
			continue
		}
		id := *e.TaskID
		switch e.Tool {
		case "claim_task":
			m.claims[id]++
			m.assign(id, e.AgentID)
		case "reassign":
			m.reassigned[id] = true
		}
		if a, ok := e.After["assignee"].(string); ok {
			m.assign(id, a)
		}
	}
	day := quota.DayStart(pre.Now).Unix()
	for _, t := range post.Tasks {
		if p := pre.Task(t.ID); p != nil && t.CostUsed > p.CostUsed && p.Assignee != nil {
			m.cost[quotaDay{*p.Assignee, day}] += t.CostUsed - p.CostUsed
		}
	}
}

func (m *featureModel) assign(task, agent string) {
	if agent != "" && !slices.Contains(m.assignees[task], agent) {
		m.assignees[task] = append(m.assignees[task], agent)
	}
}

func heldBy(s *State, agent string, statuses ...string) []*model.Task {
	var out []*model.Task
	for _, t := range s.Tasks {
		if deref(t.Assignee) == agent && slices.Contains(statuses, t.Status) {
			out = append(out, t)
		}
	}
	return out
}

func (m *featureModel) usage(pre *State, agent string, assign bool) (quota.Usage, quotaDay) {
	key := quotaDay{agent, quota.DayStart(pre.Now).Unix()}
	u := quota.Usage{CostEUR: m.cost[key], Tasks: len(m.claimed[key])}
	if assign {
		u.Tasks += len(heldBy(pre, agent, model.StatusAssigned))
		u.ConcurrentTasks = len(heldBy(pre, agent, model.StatusAssigned, model.StatusInProgress, model.StatusChangesRequested))
	} else {
		u.ConcurrentTasks = len(heldBy(pre, agent, model.StatusInProgress))
	}
	return u, key
}

func exceeded(lim quota.Quota, u quota.Usage, newTask bool, slack float64) (string, bool) {
	u.CostEUR += slack
	return quota.Exceeded(lim, u, newTask)
}

func (m *featureModel) checkQuota(st *step, pre, _ *State) *Violation {
	batch := st.op.Kind == OpBatch
	var v *Violation
	st.each(func(op Op, r Result) bool {
		switch op.Kind {
		case OpSetQuota:
			if r.Err == nil {
				m.limits[op.Target] = quota.Quota{MaxCostEURPerDay: op.Cost, MaxTasksPerDay: op.N, MaxConcurrentTasks: op.Limit}
			}
		case OpClaim:
			lim := m.limits[op.Actor]
			u, key := m.usage(pre, op.Actor, false)
			newTask := !m.claimed[key][op.Task]
			if r.Err == nil {
				if limit, hit := exceeded(lim, u, newTask, -costSlack); hit {
					v = violation(InvQuota, "%s claimed %s beyond its daily quota (%s): %+v of %+v", op.Actor, op.Task, limit, u, lim)
					return true
				}
				if m.claimed[key] == nil {
					m.claimed[key] = map[string]bool{}
				}
				m.claimed[key][op.Task] = true
			} else if r.Code() == "quota_exceeded" && !batch {
				if _, hit := exceeded(lim, u, newTask, costSlack); !hit {
					v = violation(InvQuota, "claim of %s by %s refused for quota at %+v of %+v", op.Task, op.Actor, u, lim)
					return true
				}
			}
		case OpAssign, OpReassign:
			t := pre.Task(op.Task)
			if batch || t == nil || deref(t.Assignee) == op.Target {
				return false
			}
			lim := m.limits[op.Target]
			u, _ := m.usage(pre, op.Target, true)
			if r.Err == nil {
				if limit, hit := exceeded(lim, u, true, -costSlack); hit {
					v = violation(InvQuota, "%s of %s to %s beyond its daily quota (%s): %+v of %+v", op.Kind, op.Task, op.Target, limit, u, lim)
					return true
				}
			} else if r.Code() == "quota_exceeded" {
				if _, hit := exceeded(lim, u, true, costSlack); !hit {
					v = violation(InvQuota, "%s of %s to %s refused for quota at %+v of %+v", op.Kind, op.Task, op.Target, u, lim)
					return true
				}
			}
		}
		return false
	})
	return v
}

func briefAudience(s *State, briefID string) map[string]bool {
	out := map[string]bool{}
	for _, t := range s.Tasks {
		if t.BriefID != briefID || t.Assignee == nil {
			continue
		}
		switch t.Status {
		case model.StatusBacklog, model.StatusDone, model.StatusCancelled:
			continue
		}
		if a := s.Agent(*t.Assignee); a != nil && a.Status != model.AgentRevoked {
			out[a.ID] = true
		}
	}
	return out
}

func idNum(id string) int {
	n, _ := strconv.Atoi(id[strings.LastIndex(id, "-")+1:])
	return n
}

func (m *featureModel) checkAnnouncements(st *step, pre, _ *State) *Violation {
	op, r := st.op, st.res
	switch op.Kind {
	case OpAnnounce:
		if r.Err != nil {
			return nil
		}
		want := map[string]bool{}
		if op.Target == "" {
			for _, a := range pre.Agents {
				if a.Status != model.AgentRevoked {
					want[a.ID] = true
				}
			}
		} else {
			want = briefAudience(pre, op.Target)
		}
		if n := r.Delivered[""]; n != len(want) {
			return violation(InvAnnounce, "%s reported %d deliveries for an audience of %d", r.ID, n, len(want))
		}
		for _, a := range pre.Agents {
			if got, exp := r.Delivered[a.ID], want[a.ID]; (exp && got != 1) || (!exp && got != 0) {
				return violation(InvAnnounce, "%s reached %s %d times, in audience: %t", r.ID, a.ID, got, exp)
			}
		}
		a := &annModel{id: r.ID, brief: op.Target, created: pre.Now}
		if op.Dur != 0 {
			a.expires = pre.Now.Add(op.Dur)
		}
		m.anns = append(m.anns, a)
	case OpUnannounce:
		i := slices.IndexFunc(m.anns, func(a *annModel) bool { return a.id == op.Target && !a.deleted })
		if (i >= 0) != (r.Err == nil) {
			return violation(InvAnnounce, "delete of %s: known %t, err %v", op.Target, i >= 0, r.Err)
		}
		if i >= 0 {
			m.anns[i].deleted = true
		}
	case OpInstructions:
		if r.Err != nil {
			return nil
		}
		if r.Status == "repeated" {
			return violation(InvAnnounce, "instructions of %s repeat the announcement header", op.Actor)
		}
		var want []*annModel
		for _, a := range m.anns {
			if a.deleted || (!a.expires.IsZero() && !a.expires.After(pre.Now)) {
				continue
			}
			if a.brief != "" && !briefAudience(pre, a.brief)[op.Actor] {
				continue
			}
			want = append(want, a)
		}
		slices.SortStableFunc(want, func(a, b *annModel) int {
			if c := b.created.Compare(a.created); c != 0 {
				return c
			}
			return idNum(b.id) - idNum(a.id)
		})
		ids := []string{}
		for _, a := range want[:min(len(want), 10)] {
			ids = append(ids, a.id)
		}
		if got := append([]string{}, r.Hits...); !slices.Equal(got, ids) {
			return violation(InvAnnounce, "instructions of %s list announcements %v, want %v", op.Actor, got, ids)
		}
	}
	return nil
}

func (m *featureModel) checkContext(st *step, pre, post *State) *Violation {
	op, r := st.op, st.res
	if op.Kind != OpGetTask || r.Err != nil {
		return nil
	}
	t := pre.Task(op.Task)
	eligible := t != nil && deref(t.Assignee) == op.Actor &&
		slices.Contains([]string{model.StatusAssigned, model.StatusChangesRequested, model.StatusInProgress}, t.Status)
	tc := r.Context
	if tc == nil {
		if !eligible || post.Server.Archived {
			return nil
		}
		claims := m.claims[t.ID]
		if t.Status == model.StatusInProgress {
			claims--
		}
		prev := slices.DeleteFunc(slices.Clone(m.assignees[t.ID]), func(a string) bool { return a == op.Actor })
		if claims > 0 || len(prev) > 0 || m.reassigned[t.ID] || t.ReviewRounds > 0 {
			return violation(InvContext, "get_task %s by its assignee %s has no handoff context despite history (claims %d, previous %v, rounds %d)",
				t.ID, op.Actor, claims, prev, t.ReviewRounds)
		}
		return nil
	}
	raw, _ := json.Marshal(tc)
	switch {
	case !eligible:
		return violation(InvContext, "%s got handoff context for %s it is not working on", op.Actor, op.Task)
	case slices.Contains(tc.PreviousAssignees, op.Actor):
		return violation(InvContext, "handoff context of %s lists the caller %s as previous assignee", op.Task, op.Actor)
	case len(tc.PreviousAssignees) > 10:
		return violation(InvContext, "handoff context of %s lists %d previous assignees", op.Task, len(tc.PreviousAssignees))
	case len(raw) > board.MaxContextBytes:
		return violation(InvContext, "handoff context of %s is %d bytes", op.Task, len(raw))
	case tc.Branch != t.Branch || tc.ReviewRounds != t.ReviewRounds:
		return violation(InvContext, "handoff context of %s disagrees with the task", op.Task)
	}
	return nil
}

func (m *featureModel) checkLeaderReads(st *step, pre, _ *State) *Violation {
	op, r := st.op, st.res
	if op.Kind != OpLeaderRead || r.Err != nil {
		return nil
	}
	for _, id := range r.Hits {
		if br := pre.Brief(id); br != nil && !br.LedBy(op.Actor) {
			return violation(InvLeaderScope, "%s by %s returned brief %s led by %s", op.Text, op.Actor, id, deref(br.LeaderID))
		}
	}
	if op.Text == "list_briefs" {
		for _, br := range pre.Briefs {
			if br.LedBy(op.Actor) && !slices.Contains(r.Hits, br.ID) {
				return violation(InvLeaderScope, "list_briefs by %s misses its brief %s", op.Actor, br.ID)
			}
		}
	}
	return nil
}

func terminal(t *model.Task) bool {
	return t.Status == model.StatusDone || t.Status == model.StatusCancelled
}

func (m *featureModel) checkSplit(st *step, pre, post *State) *Violation {
	op, r := st.op, st.res
	if op.Kind != OpAcceptSplit {
		return nil
	}
	for _, p := range pre.Tasks {
		t := post.Task(p.ID)
		if r.Err != nil && (t == nil || t.Status != p.Status || !slices.Equal(t.DependsOn, p.DependsOn)) {
			return violation(InvSplit, "failed accept_split %s (%v) changed %s", op.Target, r.Err, p.ID)
		}
	}
	for _, t := range post.Tasks {
		if pre.Task(t.ID) == nil && r.Err != nil && t.Status != model.StatusCancelled {
			return violation(InvSplit, "failed accept_split %s (%v) left task %s %s", op.Target, r.Err, t.ID, t.Status)
		}
	}
	if r.Err != nil {
		return nil
	}
	orig, p := post.Task(op.Task), pre.Task(op.Task)
	if p == nil || terminal(p) {
		return violation(InvSplit, "accept_split %s replaced %s which was %s", op.Target, op.Task, status(p))
	}
	if orig.Status != model.StatusCancelled {
		return violation(InvSplit, "accept_split %s left original %s %s", op.Target, op.Task, orig.Status)
	}
	if len(r.Hits) < 2 {
		return violation(InvSplit, "accept_split %s created %d tasks", op.Target, len(r.Hits))
	}
	for _, id := range r.Hits {
		if t := post.Task(id); t == nil || t.BriefID != orig.BriefID || t.Status == model.StatusCancelled {
			return violation(InvSplit, "accept_split %s created %s as %s", op.Target, id, status(t))
		}
	}
	for _, t := range post.Tasks {
		if !terminal(t) && slices.Contains(t.DependsOn, orig.ID) {
			return violation(InvSplit, "%s still depends on split task %s", t.ID, orig.ID)
		}
	}
	return nil
}

func privileged(s *State, agent string) bool {
	a := s.Agent(agent)
	return a != nil && a.Role == model.RoleLeader
}

func worksOn(s *State, agent, brief string) bool {
	return slices.ContainsFunc(s.Tasks, func(t *model.Task) bool {
		return t.BriefID == brief && (deref(t.Assignee) == agent || deref(t.Reviewer) == agent)
	})
}

func (m *featureModel) visible(s *State, agent string, e *entryModel) bool {
	if privileged(s, agent) {
		return e.status != knowledge.StatusSuperseded
	}
	return e.status == knowledge.StatusActive && worksOn(s, agent, e.brief)
}

func (m *featureModel) checkKnowledge(st *step, pre, _ *State) *Violation {
	op, r := st.op, st.res
	switch op.Kind {
	case OpAddKnowledge:
		if r.Err != nil {
			return nil
		}
		want := knowledge.StatusActive
		if pre.Server.Moderation {
			want = knowledge.StatusPending
		}
		if r.Status != want {
			return violation(InvKnowledge, "entry %s by %s is %s, want %s", r.ID, op.Actor, r.Status, want)
		}
		m.entries[r.ID] = &entryModel{brief: r.Hits[0], status: r.Status}
	case OpModerate:
		e := m.entries[op.Target]
		ok := e != nil && (!op.Approve || e.status == knowledge.StatusPending)
		if ok != (r.Err == nil) {
			return violation(InvKnowledge, "moderate %s (approve %t): known %v, err %v", op.Target, op.Approve, e, r.Err)
		}
		switch {
		case r.Err != nil:
		case op.Approve:
			e.status = knowledge.StatusActive
		default:
			delete(m.entries, op.Target)
		}
	case OpReadKnowledge:
		if r.Code() == "rate_limited" || r.Code() == "agent_paused" || r.Code() == "unauthorized" {
			return nil
		}
		if op.Target != "" {
			e := m.entries[op.Target]
			vis := e != nil && m.visible(pre, op.Actor, e)
			if vis != (r.Err == nil) {
				return violation(InvKnowledge, "get_knowledge %s by %s: visible %t, err %v", op.Target, op.Actor, vis, r.Err)
			}
			return nil
		}
		if r.Err != nil {
			return violation(InvKnowledge, "search_knowledge by %s failed: %v", op.Actor, r.Err)
		}
		var want []string
		for id, e := range m.entries {
			if m.visible(pre, op.Actor, e) {
				want = append(want, id)
			}
		}
		got := slices.Clone(r.Hits)
		slices.Sort(got)
		slices.Sort(want)
		if len(want) <= knowledge.MaxLimit && !slices.Equal(got, want) {
			return violation(InvKnowledge, "search_knowledge by %s returned %v, want %v", op.Actor, got, want)
		}
	}
	return nil
}

func ordered(r estimate.Range) bool { return r.Low <= r.Expected && r.Expected <= r.High }

func (m *featureModel) checkEstimate(st *step, pre, _ *State) *Violation {
	op, r := st.op, st.res
	if op.Kind != OpEstimate || r.Err != nil {
		return nil
	}
	open, live, done := 0, 0, 0
	for _, t := range pre.Tasks {
		if t.BriefID != op.Target || t.Status == model.StatusCancelled {
			continue
		}
		live++
		if !estimate.Completed(t) {
			open++
		}
		switch t.Status {
		case model.StatusDone, model.StatusApproved, model.StatusSubmitted, model.StatusInReview:
			done++
		}
	}
	e, f := r.Estimate, r.Forecast
	tot := e.Totals
	switch {
	case !r.Stable:
		return violation(InvEstimate, "estimates or forecast of %s changed between two reads", op.Target)
	case tot.Tasks != open || len(e.Tasks) != open:
		return violation(InvEstimate, "estimates of %s cover %d tasks (%d listed), %d open", op.Target, tot.Tasks, len(e.Tasks), open)
	case tot.Estimated > tot.Tasks || e.Sufficient != (tot.Estimated > 0):
		return violation(InvEstimate, "estimates of %s: %d of %d estimated, sufficient %t", op.Target, tot.Estimated, tot.Tasks, e.Sufficient)
	case !ordered(tot.Minutes) || !ordered(tot.CostEUR) || !ordered(tot.CriticalPathMinutes):
		return violation(InvEstimate, "estimate ranges of %s out of order: %+v", op.Target, tot)
	case f.Tasks.Total != live || f.Tasks.Complete != done || f.Tasks.Complete+f.Tasks.Remaining != f.Tasks.Total:
		return violation(InvEstimate, "forecast of %s counts %+v, want %d live, %d complete", op.Target, f.Tasks, live, done)
	}
	return nil
}

func (m *featureModel) checkIncident(st *step, pre, post *State) *Violation {
	op, r := st.op, st.res
	if r.Err != nil {
		return nil
	}
	switch op.Kind {
	case OpIncidentStart:
		if r.Status == "created" {
			if m.incident != "" {
				return violation(InvIncident, "%s created while %s is active", r.ID, m.incident)
			}
			m.incident, m.incDone = r.ID, map[string]bool{}
			if n := countTool(st, "incident_start"); n != 1 {
				return violation(InvIncident, "%s started with %d incident_start events", r.ID, n)
			}
		} else if r.ID != m.incident {
			return violation(InvIncident, "start joined %s, active is %q", r.ID, m.incident)
		}
		acts := strings.Split(op.Text, ",")
		failed := func(a string) bool { return slices.Contains(r.Hits, a) }
		if slices.Contains(acts, incident.PauseAll) && !failed(incident.PauseAll) && !m.incDone[incident.PauseAll] {
			if !post.Paused {
				return violation(InvIncident, "%s paused nothing", r.ID)
			}
			m.incDone[incident.PauseAll] = true
		}
		if slices.Contains(acts, incident.RotateLeaderTokens) {
			var want []string
			if !m.incDone[incident.RotateLeaderTokens] {
				for _, a := range pre.Agents {
					if a.Role == model.RoleLeader && a.Status != model.AgentRevoked {
						want = append(want, a.ID)
					}
				}
			}
			got := slices.Clone(r.Rotated)
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				return violation(InvIncident, "%s rotated %v, want %v", r.ID, got, want)
			}
			if !failed(incident.RotateLeaderTokens) {
				m.incDone[incident.RotateLeaderTokens] = true
			}
		}
		if slices.Contains(acts, incident.RevokeAgents) {
			role, provider, _ := strings.Cut(op.Target, "/")
			for _, a := range pre.Agents {
				if (role == "" || a.Role == role) && (provider == "" || a.Provider == provider) {
					if p := post.Agent(a.ID); p == nil || p.Status != model.AgentRevoked {
						return violation(InvIncident, "%s left %s (%s/%s) unrevoked", r.ID, a.ID, a.Role, a.Provider)
					}
				}
			}
		}
	case OpIncidentEnd:
		if r.ID != m.incident || m.incident == "" {
			return violation(InvIncident, "ended %s, active is %q", r.ID, m.incident)
		}
		if m.incDone[incident.PauseAll] && pre.Paused && (post.Paused || r.Status != "resumed") {
			return violation(InvIncident, "%s ended without resuming its pause", r.ID)
		}
		if op.Approve {
			for _, a := range pre.Agents {
				if p := post.Agent(a.ID); a.Status == model.AgentPaused && p != nil && p.Status != model.AgentActive {
					return violation(InvIncident, "%s ended with resume_agents but %s is %s", r.ID, a.ID, p.Status)
				}
			}
		}
		m.incident = ""
	}
	return nil
}

func countTool(st *step, tool string) int {
	n := 0
	for _, e := range st.added {
		if e.Tool == tool {
			n++
		}
	}
	return n
}

func (m *featureModel) checkSchedules(st *step, pre, _ *State) *Violation {
	op, r := st.op, st.res
	if op.Kind == OpSchedule && r.Err == nil {
		m.schedules[r.ID] = schedModel{catchUp: op.Text, maxActive: op.N}
	}
	created, made := 0, 0
	for _, e := range st.added {
		switch {
		case e.Tool == "create_task" && e.AgentID == "schedule":
			made++
		case e.Tool == "run_schedule":
			id, at := str(e.After["schedule_id"]), str(e.After["scheduled_for"])
			key := id + "|" + at
			if m.ran[key] {
				return violation(InvSchedule, "schedule %s ran twice for %s", id, at)
			}
			m.ran[key] = true
			if str(e.After["status"]) != schedule.RunCreated {
				continue
			}
			created += int(num(e.After["tasks"]))
			sc := m.schedules[id]
			when, err := time.Parse(time.RFC3339, at)
			if err != nil {
				return violation(InvSchedule, "schedule %s run instant %q: %v", id, at, err)
			}
			if when.After(pre.Now) {
				return violation(InvSchedule, "schedule %s ran %s before it was due at %s", id, at, pre.Now.Format(time.RFC3339))
			}
			if sc.catchUp == schedule.CatchUpSkip && pre.Now.Sub(when) > schedule.DefaultGrace {
				return violation(InvSchedule, "schedule %s with catch_up skip ran %s late at %s", id, at, pre.Now.Format(time.RFC3339))
			}
			if sc.maxActive > 0 {
				open := 0
				for k, ids := range m.runTasks {
					if !strings.HasPrefix(k, id+"|") {
						continue
					}
					if slices.ContainsFunc(ids, func(tid string) bool { t := pre.Task(tid); return t != nil && !terminal(t) }) {
						open++
					}
				}
				if open >= sc.maxActive {
					return violation(InvSchedule, "schedule %s ran %s with %d open runs, max_active %d", id, at, open, sc.maxActive)
				}
			}
		}
	}
	if created != made {
		return violation(InvSchedule, "runs report %d tasks, schedule created %d", created, made)
	}
	if op.Kind == OpScheduleTick {
		for _, h := range r.Hits {
			parts := strings.SplitN(h, "|", 3)
			if len(parts) == 3 && parts[2] != "" {
				m.runTasks[parts[0]+"|"+parts[1]] = strings.Split(parts[2], ",")
			}
		}
	}
	return nil
}

func (m *featureModel) gateFailures(s *State, t *model.Task) []string {
	br := s.Brief(t.BriefID)
	if br == nil {
		return nil
	}
	g := br.QualityGates
	ov, overridden := m.overrides[t.ID]
	covers := func(gate string) bool { return overridden && (len(ov) == 0 || slices.Contains(ov, gate)) }
	var out []string
	fail := func(gate string, bad bool) {
		if bad && !covers(gate) {
			out = append(out, gate)
		}
	}
	if g.Tests {
		fail(gates.Tests, t.Tests == nil || t.Tests.Failed > 0 || t.Tests.Passed < g.MinTestsPassed)
	}
	if g.Questions {
		fail(gates.Questions, s.Server.OpenQuestions[t.ID])
	}
	if g.Budget {
		fail(gates.Budget, (t.Budget.MaxCostEUR > 0 && t.CostUsed > t.Budget.MaxCostEUR) ||
			(t.Budget.MaxMinutes > 0 && t.MinutesUsed > t.Budget.MaxMinutes) ||
			(br.Budget.MaxCostEUR > 0 && br.CostUsed > br.Budget.MaxCostEUR))
	}
	return out
}

func (m *featureModel) checkGates(st *step, pre, post *State) *Violation {
	op, r := st.op, st.res
	if op.Kind == OpOverride && r.Err == nil {
		m.overrides[op.Task] = nil
		if op.Text != "" {
			m.overrides[op.Task] = strings.Split(op.Text, ",")
		}
	}
	if op.Kind == OpBatch {
		return nil
	}
	for _, t := range post.Tasks {
		p := pre.Task(t.ID)
		if p == nil || p.Status == model.StatusApproved || t.Status != model.StatusApproved {
			continue
		}
		if f := m.gateFailures(post, t); len(f) > 0 {
			return violation(InvGates, "%s approved via %s with failing gates %v", t.ID, op.Kind, f)
		}
	}
	if op.Kind == OpReview && r.Code() == "gates_failed" {
		if t := pre.Task(op.Task); t != nil && len(m.gateFailures(pre, t)) == 0 {
			return violation(InvGates, "approval of %s held although every gate passes", op.Task)
		}
	}
	return nil
}
