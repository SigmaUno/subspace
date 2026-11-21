package dst

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
)

func NewWorld(seed int64) World {
	r := rand.New(rand.NewSource(seed)) //nolint:gosec // deterministic simulation
	w := World{Lease: 15 * time.Minute, Leader: "lead", MaxParallel: 2 + r.Intn(6)}
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
	w       World
	setup   Setup
	serial  bool
	leader  *agentSim
	agents  []*agentSim
	queue   []Op
	last    Op
	hasLast bool
}

func NewGen(seed int64, w World, s Setup, serial bool) *Gen {
	g := &Gen{r: rand.New(rand.NewSource(seed ^ 0x5d5c)), w: w, setup: s, serial: serial} //nolint:gosec // deterministic simulation
	for _, a := range w.Agents {
		hb := time.Duration(1+g.r.Intn(5)) * time.Minute
		if g.r.Intn(4) == 0 {
			hb = w.Lease - 2*time.Minute + time.Duration(g.r.Intn(5))*time.Minute
		}
		g.agents = append(g.agents, &agentSim{spec: a, hbEvery: hb})
	}
	g.leader = &agentSim{spec: AgentSpec{ID: w.Leader, Role: model.RoleLeader}}
	return g
}

func (g *Gen) pick(n int) int { return g.r.Intn(n) }

func (g *Gen) chance(p int) bool { return g.r.Intn(100) < p }

func (g *Gen) Next(s *State) Op {
	op := g.next(s)
	g.last, g.hasLast = op, true
	return op
}

func (g *Gen) next(s *State) Op {
	if len(g.queue) > 0 {
		op := g.queue[0]
		g.queue = g.queue[1:]
		return op
	}
	if g.hasLast && g.last.Mutating() && g.chance(5) {
		return g.last
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
		return g.agent(s, g.agents[g.pick(len(g.agents))])
	}
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
	for _, a := range g.w.Agents {
		if a.Role == role {
			out = append(out, a)
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
		return Op{Kind: OpMarkDone, Task: g.choose(g.tasksIn(s, model.StatusApproved, model.StatusNeedsOwner), s)}
	case p < 65:
		return Op{Kind: OpCancel, Task: g.choose(g.tasksIn(s, model.StatusInProgress, model.StatusAssigned, model.StatusSubmitted), s)}
	case p < 76:
		return Op{Kind: OpReopen, Task: g.choose(g.tasksIn(s, model.StatusNeedsOwner, model.StatusBlocked, model.StatusCancelled), s)}
	case p < 82:
		ids := []string{g.leader.spec.ID}
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
	l := g.leader
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
		ts := g.tasksIn(s, model.StatusSubmitted, model.StatusInReview)
		var cand []*model.Task
		for _, t := range ts {
			if t.Reviewer == nil || *t.Reviewer == id {
				cand = append(cand, t)
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
