package dst

import (
	"math"
	"slices"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/gateway"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/quota"
)

const (
	InvGateway    = "gateway_calls_governed"
	InvTranscript = "transcript_complete_and_ordered"
	InvOutput     = "agent_output_sequenced"
	InvLeak       = "leak_revokes_exactly_the_leaked_identity"
	InvSchema     = "schema_stable_across_restarts"
	InvDrill      = "restore_drill_harmless"
	InvPush       = "push_wakeups_survive_reconnects"
	InvQuotaUsage = "quota_usage_counted_once"
)

type v4Model struct {
	rate     map[string][]time.Time
	day      string
	calls    int
	upstream int
	claims   map[quotaDay]map[string]bool
	cost     map[quotaDay]float64
}

func newV4Model() *v4Model {
	return &v4Model{rate: map[string][]time.Time{}, claims: map[quotaDay]map[string]bool{}, cost: map[quotaDay]float64{}}
}

func (c *Checker) checkV4(st *step, pre, post *State) *Violation {
	if pre.Server == nil || post.Server == nil || post.Server.V4 == nil {
		return nil
	}
	m := c.v4
	if st.op.Kind == OpRestart && st.res.Err == nil {
		m.rate, m.day, m.calls = map[string][]time.Time{}, "", 0
	}
	for _, f := range []func(*step, *State, *State) *Violation{c.checkGateway, c.checkLeak, m.checkDrill, m.checkQuotaUsage} {
		if v := f(st, pre, post); v != nil {
			return v
		}
	}
	if n := len(post.Server.V4.Upstream); n != m.upstream {
		return violation(InvGateway, "upstream received %d calls, %d gateway calls were admitted", n, m.upstream)
	}
	return nil
}

func gatewayEvents(st *step) []*model.AuditEvent {
	var out []*model.AuditEvent
	for _, e := range st.added {
		if e.Tool == "gateway_call" {
			out = append(out, e)
		}
	}
	return out
}

func (c *Checker) checkGateway(st *step, pre, post *State) *Violation {
	op, r, w := st.op, st.res, post.Server.V4.World
	a := pre.Agent(op.Actor)
	switch op.Kind {
	case OpGatewayList:
		if r.Err != nil || a == nil {
			return nil
		}
		want := []string{}
		for _, t := range upstreamTools {
			if w.allowed(a.Role, t) {
				want = append(want, gateway.ToolName(gwName, t))
			}
		}
		slices.Sort(want)
		if !slices.Equal(r.Hits, want) {
			return violation(InvGateway, "tools/list of %s (%s) shows %v, want %v", op.Actor, a.Role, r.Hits, want)
		}
		return nil
	case OpGatewayCall:
	default:
		if evs := gatewayEvents(st); len(evs) > 0 {
			return violation(InvGateway, "%s appended %d gateway_call events", op.Kind, len(evs))
		}
		return nil
	}
	evs := gatewayEvents(st)
	refused := func(why string) *Violation {
		if r.Err == nil || len(evs) > 0 || r.Upstream > 0 {
			return violation(InvGateway, "%s by %s (%s) reached the gateway: err %v, %d audit events, %d upstream calls",
				op.Target, op.Actor, why, r.Err, len(evs), r.Upstream)
		}
		return nil
	}
	switch {
	case r.Code() == "unauthorized" || r.Code() == "agent_paused":
		return refused(r.Code())
	case a == nil || a.Status != model.AgentActive:
		return refused("inactive")
	case !w.allowed(a.Role, op.Target):
		return refused("not allowed for " + a.Role)
	}
	if len(evs) != 1 || str(evs[0].After["tool"]) != op.Target || evs[0].AgentID != op.Actor {
		return violation(InvGateway, "gateway call %s by %s appended %d gateway_call events", op.Target, op.Actor, len(evs))
	}
	status := str(evs[0].After["status"])
	m := c.v4
	expect := func(code string) *Violation {
		if r.Code() != code || status != code || r.Upstream != 0 {
			return violation(InvGateway, "%s by %s: want %s without upstream call, got err %v, audit status %s, %d upstream calls",
				op.Target, op.Actor, code, r.Err, status, r.Upstream)
		}
		return nil
	}
	now := pre.Now
	if w.rateLimited(op.Target) {
		window := m.rate[op.Target][:0]
		for _, t := range m.rate[op.Target] {
			if now.Sub(t) < time.Minute {
				window = append(window, t)
			}
		}
		m.rate[op.Target] = window
		if len(window) >= w.Rate {
			return expect("rate_limited")
		}
		m.rate[op.Target] = append(window, now)
	}
	if day := now.UTC().Format(time.DateOnly); day != m.day {
		m.day, m.calls = day, 0
	}
	if m.calls >= w.Calls {
		return expect("budget_exceeded")
	}
	m.calls++
	if r.Upstream != 1 || m.upstream >= len(post.Server.V4.Upstream) {
		return violation(InvGateway, "admitted call %s by %s reached the upstream %d times", op.Target, op.Actor, r.Upstream)
	}
	outcome := post.Server.V4.Upstream[m.upstream]
	m.upstream++
	switch outcome {
	case "upstream_error":
		if r.Code() != outcome || status != outcome {
			return violation(InvGateway, "upstream failure of %s surfaced as err %v, audit %s", op.Target, r.Err, status)
		}
		if strings.Contains(r.Err.Error(), gwCredential) {
			return violation(InvGateway, "upstream error of %s leaked the gateway credential", op.Target)
		}
	default:
		if r.Err != nil || r.Status != outcome || status != outcome {
			return violation(InvGateway, "upstream %s of %s surfaced as err %v, result %s, audit %s", outcome, op.Target, r.Err, r.Status, status)
		}
		for _, h := range r.Hits {
			if strings.Contains(h, gwCredential) {
				return violation(InvGateway, "result of %s carries the gateway credential", op.Target)
			}
		}
	}
	return nil
}

func (c *Checker) checkLeak(st *step, pre, post *State) *Violation {
	op, r := st.op, st.res
	var leaks []*model.AuditEvent
	for _, e := range st.added {
		if e.Tool == "token_leak_detected" {
			leaks = append(leaks, e)
		}
	}
	if op.Kind == OpReissue {
		if r.Err != nil {
			return nil
		}
		if p, a := pre.Agent(op.Target), post.Agent(op.Target); p == nil || p.Status != model.AgentRevoked || a == nil || a.Status != model.AgentActive {
			return violation(InvLeak, "reissue of %s went %s → %s", op.Target, agentStatus(p), agentStatus(a))
		}
		return nil
	}
	if op.Kind != OpLeak {
		if len(leaks) > 0 {
			return violation(InvLeak, "%s detected %d leaks without a planted token", op.Kind, len(leaks))
		}
		return nil
	}
	victim := op.Target
	for _, a := range post.Agents {
		if p := pre.Agent(a.ID); p != nil && p.Status != model.AgentRevoked && a.Status == model.AgentRevoked && a.ID != victim {
			return violation(InvLeak, "leak of %s's token by %s revoked %s", victim, op.Actor, a.ID)
		}
	}
	p, a := pre.Agent(victim), post.Agent(victim)
	live := p != nil && p.Status != model.AgentRevoked && op.N == c.tokens[victim]
	revoked := live && a != nil && a.Status == model.AgentRevoked
	switch {
	case !live && len(leaks) > 0:
		return violation(InvLeak, "stale or revoked token #%d of %s raised %d leaks", op.N, victim, len(leaks))
	case !live:
		return nil
	case revoked && (len(leaks) != 1 || str(leaks[0].Before["agent"]) != victim):
		return violation(InvLeak, "%s was revoked with %d token_leak_detected events", victim, len(leaks))
	case !revoked && len(leaks) > 0:
		return violation(InvLeak, "leak of %s recorded but the agent is %s", victim, agentStatus(a))
	case !revoked && r.Err == nil:
		return violation(InvLeak, "live token #%d of %s accepted in a %s by %s without revocation", op.N, victim, op.Text, op.Actor)
	}
	return nil
}

func agentStatus(a *model.Agent) string {
	if a == nil {
		return "(none)"
	}
	return a.Status
}

func (m *v4Model) checkDrill(st *step, pre, post *State) *Violation {
	op, r := st.op, st.res
	if op.Kind != OpDrill {
		for _, e := range st.added {
			if e.Tool == "restore_drill" {
				return violation(InvDrill, "%s appended a restore_drill event", op.Kind)
			}
		}
		return nil
	}
	if r.Err != nil {
		return violation(InvDrill, "drill failed to run: %v", r.Err)
	}
	if ch := changed(pre, post); len(ch) > 0 {
		return violation(InvDrill, "drill changed tasks %v", ch)
	}
	if len(st.added) != 1 || st.added[0].Tool != "restore_drill" {
		return violation(InvDrill, "drill appended %d audit events", len(st.added))
	}
	msg := strings.Join(r.Hits, "")
	switch {
	case r.Status == "unsupported":
	case pre.Server.Backups == 0:
		if r.Status != "failed" || !strings.HasPrefix(msg, "restore:") {
			return violation(InvDrill, "drill without backups: %s %q", r.Status, msg)
		}
	case r.Status != "ok" && !strings.HasPrefix(msg, "row_counts:"):
		return violation(InvDrill, "drill of a verified backup: %s %q", r.Status, msg)
	}
	return nil
}

func (m *v4Model) checkQuotaUsage(st *step, pre, post *State) *Violation {
	day := quota.DayStart(pre.Now).Unix()
	st.each(func(op Op, r Result) bool {
		if op.Kind == OpClaim && r.Err == nil {
			k := quotaDay{op.Actor, day}
			if m.claims[k] == nil {
				m.claims[k] = map[string]bool{}
			}
			m.claims[k][op.Task] = true
		}
		return false
	})
	for _, t := range post.Tasks {
		if p := pre.Task(t.ID); p != nil && t.CostUsed > p.CostUsed && p.Assignee != nil {
			m.cost[quotaDay{*p.Assignee, day}] += t.CostUsed - p.CostUsed
		}
	}
	q := post.Server.V4.Quota
	if q == nil {
		return nil
	}
	day = quota.DayStart(post.Now).Unix()
	for id, u := range q {
		k := quotaDay{id, day}
		if u.Tasks != len(m.claims[k]) || math.Abs(u.CostEUR-m.cost[k]) > 1e-6 {
			return violation(InvQuotaUsage, "%s used %d tasks and %.4f EUR today by its quota, %d tasks and %.4f EUR by its calls after %s",
				id, u.Tasks, u.CostEUR, len(m.claims[k]), m.cost[k], st.op.Kind)
		}
	}
	return nil
}
