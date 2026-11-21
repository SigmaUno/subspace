package dst

import (
	"fmt"
	"maps"
	"math/rand"
	"slices"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/instructions"
	"github.com/SigmaUno/subspace/internal/model"
)

func NewWorld(seed int64) World {
	r := rand.New(rand.NewSource(seed)) //nolint:gosec // deterministic simulation
	w := World{Seed: seed, Lease: 15 * time.Minute, Leader: "lead", MaxParallel: 2 + r.Intn(6)}
	if r.Intn(3) > 0 {
		w.BriefCost = float64(5 + r.Intn(40))
	}
	kinds := []string{model.KindClaudeCode, model.KindCodex, model.KindOpenCode}
	for i := range 2 + r.Intn(5) {
		k := kinds[r.Intn(len(kinds))]
		w.Agents = append(w.Agents, AgentSpec{ID: fmt.Sprintf("w%d", i), Role: model.RoleWorker, Kind: k, Provider: model.ProviderForKind(k)})
	}
	for i := range 1 + r.Intn(2) {
		k := kinds[r.Intn(len(kinds))]
		w.Agents = append(w.Agents, AgentSpec{ID: fmt.Sprintf("t%d", i), Role: model.RoleTester, Kind: k, Provider: model.ProviderForKind(k)})
	}
	providers := []string{"anthropic", "openai", "deepseek"}
	for i := range 1 + r.Intn(2) {
		w.Agents = append(w.Agents, AgentSpec{ID: fmt.Sprintf("r%d", i), Role: model.RoleReviewer, Kind: model.KindOther, Provider: providers[r.Intn(len(providers))]})
	}
	n := 3 + r.Intn(10)
	for i := range n {
		t := TaskSpec{Ref: fmt.Sprintf("k%d", i), Role: model.RoleWorker}
		if r.Intn(5) == 0 {
			t.Role = model.RoleTester
		}
		if i > 0 && r.Intn(4) == 0 {
			t.DependsOn = []string{fmt.Sprintf("k%d", r.Intn(i))}
		}
		if r.Intn(2) == 0 {
			t.Budget.MaxCostEUR = float64(1 + r.Intn(8))
		}
		if r.Intn(2) == 0 {
			t.Budget.MaxMinutes = 20 + r.Intn(200)
		}
		w.Tasks = append(w.Tasks, t)
	}
	return w
}

type agentSim struct {
	spec         AgentSpec
	hbEvery      time.Duration
	lastHB       time.Time
	crashedUntil time.Time
	token        int
}

type Gen struct {
	r       *rand.Rand
	scope   *rand.Rand
	w       World
	setup   Setup
	serial  bool
	leader  *agentSim
	rival   *agentSim
	agents  []*agentSim
	known   map[string]bool
	queue   []Op
	last    Op
	hasLast bool
}

func NewGen(seed int64, w World, s Setup, serial bool) *Gen {
	g := &Gen{r: rand.New(rand.NewSource(seed ^ 0x5d5c)), w: w, setup: s, serial: serial, known: map[string]bool{}} //nolint:gosec // deterministic simulation
	for _, a := range w.Agents {
		g.add(a)
	}
	if w.Server != nil {
		g.queue = append(g.queue, Op{Kind: OpApprove, Target: s.PlanID})
	}
	g.leader = &agentSim{spec: AgentSpec{ID: w.Leader, Role: model.RoleLeader}}
	if w.Rival != "" {
		g.rival = &agentSim{spec: AgentSpec{ID: w.Rival, Role: model.RoleLeader}}
		g.scope = rand.New(rand.NewSource(seed ^ 0x1ead)) //nolint:gosec // deterministic simulation
	}
	return g
}

func (g *Gen) add(a AgentSpec) {
	hb := time.Duration(1+g.r.Intn(5)) * time.Minute
	if g.r.Intn(4) == 0 {
		hb = g.w.Lease - 2*time.Minute + time.Duration(g.r.Intn(5))*time.Minute
	}
	g.agents = append(g.agents, &agentSim{spec: a, hbEvery: hb})
	g.known[a.ID] = true
}

func (g *Gen) sync(s *State) {
	for _, a := range s.Agents {
		if !g.known[a.ID] && a.ID != g.w.Leader && a.Role != model.RoleLeader {
			g.add(AgentSpec{ID: a.ID, Role: a.Role, Kind: a.Kind, Provider: a.Provider})
		}
	}
}

func (g *Gen) leaderSim() *agentSim {
	if g.rival != nil && g.scope.Intn(2) == 0 {
		return g.rival
	}
	return g.leader
}

func (g *Gen) handoff(s *State) (Op, bool) {
	if g.rival == nil || len(s.Briefs) == 0 || g.scope.Intn(100) >= 4 {
		return Op{}, false
	}
	cur := deref(s.Briefs[0].LeaderID)
	var cand []string
	for _, id := range []string{g.w.Leader, g.w.Rival, ""} {
		if id != cur {
			cand = append(cand, id)
		}
	}
	return Op{Kind: OpHandoff, Target: cand[g.scope.Intn(len(cand))]}, true
}

func (g *Gen) pick(n int) int { return g.r.Intn(n) }

func (g *Gen) chance(p int) bool { return g.r.Intn(100) < p }

func (g *Gen) Next(s *State) Op {
	g.sync(s)
	op := g.next(s)
	if op.Kind != OpHandoff {
		g.last, g.hasLast = op, true
	}
	return op
}

func (g *Gen) next(s *State) Op {
	if len(g.queue) > 0 {
		op := g.queue[0]
		g.queue = g.queue[1:]
		return op
	}
	if op, ok := g.handoff(s); ok {
		return op
	}
	if g.hasLast && g.last.Mutating() && g.chance(5) {
		return g.last
	}
	if g.w.Server != nil && g.chance(18) {
		return g.server(s)
	}
	if g.w.Server != nil && g.chance(14) {
		return g.feature(s)
	}
	switch p := g.pick(100); {
	case p < 11:
		d := time.Duration(10+g.pick(170)) * time.Second
		if g.chance(10) {
			d = time.Duration(5+g.pick(20)) * time.Minute
		}
		return Op{Kind: OpAdvance, Dur: d}
	case p < 12:
		if g.chance(60) {
			return Op{Kind: OpAdvance, Dur: -time.Duration(1+g.pick(30)) * time.Minute}
		}
		return Op{Kind: OpAdvance, Dur: time.Duration(1+g.pick(6)) * time.Hour}
	case p < 17:
		return Op{Kind: OpSweep}
	case p < 20:
		return Op{Kind: OpFlush}
	case p < 23:
		if op, ok := g.race(s); ok {
			return op
		}
		return Op{Kind: OpSweep}
	case p < 26:
		if g.serial {
			return Op{Kind: OpSweep}
		}
		return g.batch(s)
	case p < 27:
		return Op{Kind: OpRestart, Crash: g.chance(50)}
	case p < 30:
		return g.sse(s)
	case p < 37:
		return g.owner(s)
	case p < 49:
		return g.leaderOp(s)
	default:
		if g.w.Server != nil && g.chance(80) {
			if as := g.working(s); len(as) > 0 {
				return g.agent(s, as[g.pick(len(as))])
			}
		}
		return g.agent(s, g.agents[g.pick(len(g.agents))])
	}
}

func (g *Gen) working(s *State) []*agentSim {
	var out []*agentSim
	for _, a := range g.agents {
		if s.Now.Before(a.crashedUntil) {
			continue
		}
		if a.spec.Role == model.RoleReviewer {
			if len(g.reviewable(s, a)) > 0 {
				out = append(out, a)
			}
		} else if len(g.mine(s, a.spec.ID, model.StatusAssigned, model.StatusInProgress, model.StatusChangesRequested)) > 0 {
			out = append(out, a)
		}
	}
	return out
}

func (g *Gen) reviewable(s *State, a *agentSim) []*model.Task {
	var out []*model.Task
	for _, t := range g.tasksIn(s, model.StatusSubmitted, model.StatusInReview) {
		if t.Reviewer != nil && *t.Reviewer != a.spec.ID {
			continue
		}
		if w := s.Agent(deref(t.Assignee)); w != nil && w.Provider == a.spec.Provider {
			continue
		}
		out = append(out, t)
	}
	return out
}

func (g *Gen) race(s *State) (Op, bool) {
	var ts []*model.Task
	for _, t := range s.Tasks {
		if t.Status == model.StatusInProgress && t.LeaseExpiresAt != nil && !t.LeaseExpiresAt.Before(s.Now) {
			ts = append(ts, t)
		}
	}
	if len(ts) == 0 {
		return Op{}, false
	}
	t := ts[g.pick(len(ts))]
	d := t.LeaseExpiresAt.Sub(s.Now) + time.Duration(g.pick(3)-1)*time.Millisecond
	if d < 0 {
		d = 0
	}
	hb := Op{Kind: OpHeartbeat, Actor: *t.Assignee, Task: t.ID, Token: g.knownToken(*t.Assignee)}
	if g.chance(50) {
		g.queue = append(g.queue, hb, Op{Kind: OpSweep})
	} else {
		g.queue = append(g.queue, Op{Kind: OpSweep}, hb)
	}
	return Op{Kind: OpAdvance, Dur: d}, true
}

func (g *Gen) knownToken(id string) int {
	if id == g.leader.spec.ID {
		return g.leader.token
	}
	if g.rival != nil && id == g.rival.spec.ID {
		return g.rival.token
	}
	for _, a := range g.agents {
		if a.spec.ID == id {
			return a.token
		}
	}
	return 0
}

func (g *Gen) batch(s *State) Op {
	n := 2 + g.pick(4)
	used := map[string]bool{}
	capacitySet := false
	var sub []Op
	for tries := 0; len(sub) < n && tries < 4*n; tries++ {
		var op Op
		switch p := g.pick(100); {
		case p < 30:
			op = g.owner(s)
		case p < 45:
			op = g.leaderOp(s)
		default:
			op = g.agent(s, g.agents[g.pick(len(g.agents))])
		}
		switch op.Kind {
		case OpAdvance:
			continue
		case OpSetCapacity:
			if capacitySet {
				continue
			}
			capacitySet = true
		}
		if op.Actor != "" {
			if used[op.Actor] {
				continue
			}
			used[op.Actor] = true
		}
		sub = append(sub, op)
	}
	return Op{Kind: OpBatch, Sub: sub}
}

func (g *Gen) sse(s *State) Op {
	switch p := g.pick(100); {
	case p < 30 || len(s.Subs) == 0:
		topics := []string{"all", "board", "agents", "control", "questions", "plans", g.anyBrief(s)}
		topic := topics[g.pick(len(topics))]
		if g.chance(30) {
			topic = "task:" + g.anyTask(s)
		}
		return Op{Kind: OpSubscribe, Target: topic}
	case p < 50:
		return Op{Kind: OpUnsubscribe, N: g.pick(len(s.Subs))}
	default:
		return Op{Kind: OpDrain, N: g.pick(len(s.Subs)), Limit: g.pick(64)}
	}
}

func (g *Gen) anyBrief(s *State) string {
	if len(s.Briefs) == 0 {
		return "brief:B-1"
	}
	return "brief:" + s.Briefs[g.pick(len(s.Briefs))].ID
}

func (g *Gen) tasksIn(s *State, statuses ...string) []*model.Task {
	var out []*model.Task
	for _, t := range s.Tasks {
		for _, st := range statuses {
			if t.Status == st {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

func (g *Gen) anyTask(s *State) string {
	if len(s.Tasks) == 0 {
		return "T-0"
	}
	return s.Tasks[g.pick(len(s.Tasks))].ID
}

func (g *Gen) choose(ts []*model.Task, s *State) string {
	if len(ts) == 0 || g.chance(5) {
		return g.anyTask(s)
	}
	return ts[g.pick(len(ts))].ID
}

func (g *Gen) agentsWithRole(role string) []AgentSpec {
	var out []AgentSpec
	for _, a := range g.agents {
		if a.spec.Role == role {
			out = append(out, a.spec)
		}
	}
	return out
}

func (g *Gen) plan(s *State) string {
	id := g.setup.PlanID
	for _, p := range s.Plans {
		if p.Status == model.PlanProposed {
			return p.ID
		}
		id = p.ID
	}
	return id
}

func (g *Gen) owner(s *State) Op {
	switch p := g.pick(100); {
	case p < 13:
		return Op{Kind: OpApprove, Target: g.plan(s)}
	case p < 15:
		return Op{Kind: OpReject, Target: g.plan(s)}
	case p < 18:
		return Op{Kind: OpPauseAll}
	case p < 36:
		return Op{Kind: OpResumeAll}
	case p < 62:
		if g.w.Server != nil && g.chance(85) {
			return Op{Kind: OpMarkDone, Task: g.choose(g.tasksIn(s, model.StatusNeedsOwner), s)}
		}
		return Op{Kind: OpMarkDone, Task: g.choose(g.tasksIn(s, model.StatusApproved, model.StatusNeedsOwner), s)}
	case p < 65:
		return Op{Kind: OpCancel, Task: g.choose(g.tasksIn(s, model.StatusInProgress, model.StatusAssigned, model.StatusSubmitted), s)}
	case p < 76:
		return Op{Kind: OpReopen, Task: g.choose(g.tasksIn(s, model.StatusNeedsOwner, model.StatusBlocked, model.StatusCancelled), s)}
	case p < 82:
		ids := []string{g.leader.spec.ID}
		if g.rival != nil {
			ids = append(ids, g.rival.spec.ID)
		}
		for _, a := range g.agents {
			ids = append(ids, a.spec.ID)
		}
		return Op{Kind: OpRotate, Target: ids[g.pick(len(ids))]}
	case p < 87:
		providers := []string{"*", "anthropic", "openai", "deepseek"}
		op := Op{Kind: OpSetCapacity, Target: providers[g.pick(len(providers))], Limit: g.pick(5)}
		if g.chance(40) {
			op.N = len(g.w.Agents) + g.pick(6)
		}
		return op
	case p < 91:
		prefixes := []string{"bk", "bx"}
		if g.chance(30) {
			return Op{Kind: OpRegister, Target: fmt.Sprintf("%s-%03d", prefixes[g.pick(2)], 1+g.pick(6))}
		}
		return Op{Kind: OpBulk, Target: prefixes[g.pick(2)], N: 1 + g.pick(6)}
	default:
		return Op{Kind: OpAnswer, Target: fmt.Sprintf("Q-%d", 1+g.pick(5))}
	}
}

func (g *Gen) leaderOp(s *State) Op {
	l := g.leaderSim()
	g.refresh(s, l)
	op := Op{Actor: l.spec.ID}
	if br := s.Brief(g.firstBrief(s)); br != nil && br.PlanStatus == model.PlanRejected && g.chance(60) {
		op.Kind = OpPropose
	} else {
		switch p := g.pick(100); {
		case p < 50:
			ts := g.tasksIn(s, model.StatusReady)
			role := model.RoleWorker
			id := g.choose(ts, s)
			if t := s.Task(id); t != nil {
				role = t.Role
			}
			op.Kind, op.Task, op.Target = OpAssign, id, g.agentID(role)
		case p < 78:
			op.Kind, op.Task, op.Target = OpAssignReviewer, g.choose(g.tasksIn(s, model.StatusSubmitted), s), g.agentID(model.RoleReviewer)
		case p < 92:
			ts := g.tasksIn(s, model.StatusAssigned, model.StatusInProgress, model.StatusChangesRequested, model.StatusBlocked)
			role := model.RoleWorker
			id := g.choose(ts, s)
			if t := s.Task(id); t != nil {
				role = t.Role
			}
			op.Kind, op.Task, op.Target = OpReassign, id, g.agentID(role)
		default:
			kinds := []string{model.KindClaudeCode, model.KindCodex, model.KindOpenCode}
			op.Kind, op.Target, op.N = OpSpawn, kinds[g.pick(len(kinds))], 1+g.pick(3)
		}
	}
	op.Token = l.token
	return op
}

func (g *Gen) firstBrief(s *State) string {
	if len(s.Briefs) == 0 {
		return ""
	}
	return s.Briefs[0].ID
}

func (g *Gen) refresh(s *State, a *agentSim) {
	if cur := s.Tokens[a.spec.ID]; a.token < cur && g.chance(30) {
		a.token = cur
	}
}

func (g *Gen) agentID(role string) string {
	as := g.agentsWithRole(role)
	if len(as) == 0 {
		return "nobody"
	}
	return as[g.pick(len(as))].ID
}

func (g *Gen) mine(s *State, id string, statuses ...string) []*model.Task {
	var out []*model.Task
	for _, t := range g.tasksIn(s, statuses...) {
		if deref(t.Assignee) == id {
			out = append(out, t)
		}
	}
	return out
}

func (g *Gen) agent(s *State, a *agentSim) Op {
	g.refresh(s, a)
	op := g.agentOp(s, a)
	if op.Kind != OpAdvance {
		op.Token = a.token
	}
	return op
}

func (g *Gen) agentOp(s *State, a *agentSim) Op {
	id := a.spec.ID
	if s.Now.Before(a.crashedUntil) {
		return Op{Kind: OpAdvance, Dur: time.Duration(1+g.pick(30)) * time.Second}
	}
	if a.spec.Role == model.RoleReviewer {
		var cand []*model.Task
		if g.w.Server != nil {
			cand = g.reviewable(s, a)
		} else {
			for _, t := range g.tasksIn(s, model.StatusSubmitted, model.StatusInReview) {
				if t.Reviewer == nil || *t.Reviewer == id {
					cand = append(cand, t)
				}
			}
		}
		if g.chance(3) {
			return Op{Kind: OpAskOwner, Actor: id, Task: g.choose(cand, s)}
		}
		return Op{Kind: OpReview, Actor: id, Task: g.choose(cand, s), Approve: g.chance(45)}
	}
	held := g.mine(s, id, model.StatusInProgress)
	if g.chance(2) {
		a.crashedUntil = s.Now.Add(time.Duration(1+g.pick(int(2*g.w.Lease/time.Minute))) * time.Minute)
		a.lastHB = time.Time{}
		return Op{Kind: OpAdvance, Dur: time.Second}
	}
	if len(held) == 0 {
		return Op{Kind: OpClaim, Actor: id, Task: g.choose(g.mine(s, id, model.StatusAssigned, model.StatusChangesRequested), s)}
	}
	t := held[g.pick(len(held))]
	if s.Now.Sub(a.lastHB) >= a.hbEvery || s.Now.Before(a.lastHB) {
		a.lastHB = s.Now
		task := t.ID
		if g.chance(20) {
			task = ""
		}
		return Op{Kind: OpHeartbeat, Actor: id, Task: task}
	}
	switch p := g.pick(100); {
	case p < 45:
		cost := t.CostUsed + float64(g.pick(150))/100
		return Op{Kind: OpUpdate, Actor: id, Task: t.ID, Pct: g.pick(101), Cost: cost}
	case p < 70:
		return Op{Kind: OpSubmit, Actor: id, Task: t.ID}
	case p < 75:
		return Op{Kind: OpRelease, Actor: id, Task: t.ID}
	case p < 79:
		return Op{Kind: OpBlock, Actor: id, Task: t.ID}
	case p < 85:
		return Op{Kind: OpAskOwner, Actor: id, Task: t.ID}
	case p < 90:
		return Op{Kind: OpClaim, Actor: id, Task: g.anyTask(s)}
	default:
		return Op{Kind: OpHeartbeat, Actor: id, Task: g.anyTask(s)}
	}
}

var searchTerms = []string{"task k", "TASK K1", "spec", "k1", "%", "_", "k1_", "sim", "spawn", "w", "r0"}

func (g *Gen) server(s *State) Op {
	switch p := g.pick(100); {
	case p < 16:
		return Op{Kind: OpAutopilotTick}
	case p < 34:
		return Op{Kind: OpMergeTick}
	case p < 42:
		return Op{Kind: OpEnqueue, Task: g.choose(g.tasksIn(s, model.StatusApproved), s)}
	case p < 54:
		return g.webhook(s)
	case p < 57:
		return Op{Kind: OpWebhookRace, Task: g.choose(g.tasksIn(s, model.StatusApproved), s), Target: "github_pr_merged"}
	case p < 61:
		return Op{Kind: OpCancel, Task: g.choose(g.tasksIn(s, model.StatusApproved, model.StatusSubmitted, model.StatusInReview, model.StatusAssigned), s)}
	case p < 73:
		var ts []*model.Task
		for _, t := range s.Tasks {
			if t.Role == model.RoleTester && t.Assignee != nil {
				ts = append(ts, t)
			}
		}
		if len(ts) == 0 {
			return Op{Kind: OpSearch, Target: "task", Text: "k1"}
		}
		t := ts[g.pick(len(ts))]
		failed := 0
		if g.chance(35) {
			failed = 1 + g.pick(3)
		}
		return Op{Kind: OpReportTests, Actor: *t.Assignee, Task: t.ID, N: failed, Token: g.knownToken(*t.Assignee)}
	case p < 84:
		return Op{Kind: OpInstrSave, Target: g.instrKey(), Text: fmt.Sprintf("rule %d", g.pick(4))}
	case p < 88:
		return Op{Kind: OpInstrRollback, Target: g.instrKey(), N: g.pick(6)}
	default:
		kind := "task"
		if g.chance(30) {
			kind = "agent"
		}
		return Op{Kind: OpSearch, Target: kind, Text: searchTerms[g.pick(len(searchTerms))]}
	}
}

func (g *Gen) instrKey() string {
	return instrScopes[g.pick(len(instrScopes))] + "/" + instructions.Kinds[g.pick(len(instructions.Kinds))]
}

func (g *Gen) webhook(s *State) Op {
	t := g.choose(g.tasksIn(s, model.StatusApproved, model.StatusDone, model.StatusSubmitted, model.StatusInReview), s)
	op := Op{Kind: OpWebhook, Task: t, Target: fixtureNames[g.pick(len(fixtureNames))], Forged: g.chance(10)}
	owner := Op{Kind: OpMarkDone, Task: t}
	switch p := g.pick(100); {
	case p < 30:
		g.queue = append(g.queue, owner)
	case p < 55:
		g.queue = append(g.queue, op)
		return owner
	case p < 70:
		g.queue = append(g.queue, op)
	}
	return op
}

func (g *Gen) actor(s *State, a *agentSim) Op {
	g.refresh(s, a)
	return Op{Actor: a.spec.ID, Token: a.token}
}

func (g *Gen) anyAgent() *agentSim {
	if n := g.pick(len(g.agents) + 2); n < len(g.agents) {
		return g.agents[n]
	}
	return g.leaderSim()
}

func (g *Gen) workerSim() *agentSim {
	var ws []*agentSim
	for _, a := range g.agents {
		if a.spec.Role == model.RoleWorker || a.spec.Role == model.RoleTester {
			ws = append(ws, a)
		}
	}
	if len(ws) == 0 {
		return g.agents[g.pick(len(g.agents))]
	}
	return ws[g.pick(len(ws))]
}

func (g *Gen) pickID(ids []string, prefix string) string {
	if len(ids) == 0 || g.chance(10) {
		return fmt.Sprintf("%s-%d", prefix, 1+g.pick(4))
	}
	return ids[g.pick(len(ids))]
}

var (
	leaderReads = []string{"get_brief", "board", "read_thread", "get_forecast", "get_task", "list_briefs", "list_help_requests"}
	crons       = []string{"*/15 * * * *", "0 * * * *", "*/5 * * * *", "30 */2 * * *"}
	incidentSet = []string{"pause_all", "revoke_agents", "rotate_leader_tokens", "snapshot", "notify"}
)

func (g *Gen) feature(s *State) Op {
	sv := s.Server
	brief := g.firstBrief(s)
	switch p := g.pick(100); {
	case p < 5:
		return Op{Kind: OpAdvance, Dur: time.Duration(18+g.pick(14)) * time.Hour}
	case p < 13:
		op := Op{Kind: OpSetQuota, Target: g.workerSim().spec.ID}
		if g.chance(60) {
			op.Cost = float64(1 + g.pick(6))
		}
		if g.chance(60) {
			op.N = 1 + g.pick(4)
		}
		if g.chance(60) {
			op.Limit = 1 + g.pick(2)
		}
		return op
	case p < 20:
		op := Op{Kind: OpAnnounce, Text: fmt.Sprintf("notice %d\nsecond line", g.pick(100))}
		if g.chance(50) {
			op.Target = brief
		}
		if g.chance(40) {
			op.Dur = time.Duration(10+g.pick(110)) * time.Minute
		}
		return op
	case p < 22:
		return Op{Kind: OpUnannounce, Target: g.pickID(sv.Announcements, "AN")}
	case p < 30:
		op := g.actor(s, g.anyAgent())
		op.Kind = OpInstructions
		return op
	case p < 38:
		a := g.workerSim()
		op := g.actor(s, a)
		op.Kind, op.Task = OpGetTask, g.choose(g.mine(s, a.spec.ID, model.StatusAssigned, model.StatusInProgress, model.StatusChangesRequested), s)
		return op
	case p < 46:
		op := g.actor(s, g.leaderSim())
		op.Kind, op.Text = OpLeaderRead, leaderReads[g.pick(len(leaderReads))]
		switch op.Text {
		case "get_task":
			op.Task = g.anyTask(s)
		case "list_briefs", "list_help_requests":
		default:
			op.Target = brief
		}
		return op
	case p < 49:
		a := g.workerSim()
		op := g.actor(s, a)
		op.Kind, op.Task = OpRequestHelp, g.choose(g.mine(s, a.spec.ID, model.StatusAssigned, model.StatusInProgress), s)
		return op
	case p < 53:
		a := g.workerSim()
		op := g.actor(s, a)
		op.Kind, op.Task, op.N = OpSuggestSplit, g.choose(g.mine(s, a.spec.ID, model.StatusAssigned, model.StatusInProgress, model.StatusBlocked), s), 2+g.pick(2)
		if g.chance(50) {
			op.Text = "chain"
		}
		return op
	case p < 58:
		op := g.actor(s, g.leaderSim())
		op.Kind = OpAcceptSplit
		ids := slices.Sorted(maps.Keys(sv.Splits))
		op.Target = g.pickID(ids, "HR")
		op.Task = sv.Splits[op.Target]
		return op
	case p < 63:
		a := g.anyAgent()
		op := g.actor(s, a)
		op.Kind, op.Text = OpAddKnowledge, fmt.Sprintf("pitfall %d", g.pick(50))
		if a.spec.Role == model.RoleLeader || g.chance(20) {
			op.Target = brief
		}
		return op
	case p < 66:
		return Op{Kind: OpModerate, Target: g.pickID(sv.Knowledge, "K"), Approve: g.chance(70)}
	case p < 71:
		op := g.actor(s, g.anyAgent())
		op.Kind = OpReadKnowledge
		if g.chance(50) {
			op.Target = g.pickID(sv.Knowledge, "K")
		}
		return op
	case p < 75:
		return Op{Kind: OpEstimate, Target: brief}
	case p < 77:
		var acts []string
		for _, a := range incidentSet {
			if g.chance(40) {
				acts = append(acts, a)
			}
		}
		if len(acts) == 0 {
			acts = []string{incidentSet[g.pick(len(incidentSet))]}
		}
		op := Op{Kind: OpIncidentStart, Text: strings.Join(acts, ",")}
		if slices.Contains(acts, "revoke_agents") {
			providers := []string{"anthropic", "openai", "deepseek"}
			op.Target = []string{model.RoleWorker, model.RoleTester}[g.pick(2)] + "/" + providers[g.pick(len(providers))]
		}
		return op
	case p < 80:
		return Op{Kind: OpIncidentEnd, Approve: g.chance(50)}
	case p < 82:
		return Op{Kind: OpResumeAgent, Target: g.anyAgent().spec.ID}
	case p < 84:
		return Op{Kind: OpSchedule, Target: crons[g.pick(len(crons))], Text: []string{"skip", "run-once"}[g.pick(2)], N: g.pick(3)}
	case p < 90:
		return Op{Kind: OpScheduleTick, N: 1 + g.pick(2)}
	case p < 93:
		return Op{Kind: OpRetention}
	case p < 95:
		return Op{Kind: OpRestore, N: g.pick(8)}
	case p < 97:
		gs := []string{"questions", "review", "budget", "tests"}
		g.r.Shuffle(len(gs), func(i, j int) { gs[i], gs[j] = gs[j], gs[i] })
		return Op{Kind: OpSetGates, Text: strings.Join(gs[:g.pick(len(gs)+1)], ","), N: g.pick(12)}
	default:
		op := Op{Kind: OpOverride, Task: g.choose(g.tasksIn(s, model.StatusSubmitted, model.StatusInReview), s)}
		if g.chance(50) {
			op.Text = []string{"tests", "questions", "budget"}[g.pick(3)]
		}
		return op
	}
}
