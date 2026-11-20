package dst

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/audit"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/server"
)

const (
	lease       = 15 * time.Minute
	maxTasks    = 36
	loopKeysCap = 200
)

var epoch = time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)

type agent struct {
	id, role, kind, provider string
	token                    string
	status                   string
	downUntil                int
	keys                     int
	last                     *call
}

type call struct {
	tool string
	args json.RawMessage
}

type sim struct {
	ctx   context.Context
	cfg   Config
	dir   string
	srv   *server.Server
	ts    *httptest.Server
	c     *client
	clock *Clock
	reg   *mcp.Registry

	step int
	log  []string

	agents []*agent
	by     map[string]*agent
	leader *agent
	paused bool
	loops  map[string]int

	tasks    []*model.Task
	taskBy   map[string]*model.Task
	briefs   []*model.Brief
	briefBy  map[string]*model.Brief
	replay   map[string]string
	auditRaw []json.RawMessage
	auditIDs []int64
}

func start(ctx context.Context, cfg Config) (*sim, error) {
	if cfg.Agents < 6 {
		cfg.Agents = 6
	}
	dir, err := os.MkdirTemp("", "subspace-dst-*")
	if err != nil {
		return nil, err
	}
	clock := NewClock(epoch)
	owner := crand.Text()
	srv, err := server.New(ctx, server.Config{
		DBPath: filepath.Join(dir, "dst.db"), OwnerToken: owner, PublicURL: "http://dst.test",
		Lease: lease, Now: clock.Now, Log: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	ts := httptest.NewServer(srv.Handler())
	s := &sim{
		ctx: ctx, cfg: cfg, dir: dir, srv: srv, ts: ts, clock: clock, reg: mcp.NewRegistry(srv.Board),
		c:  &client{base: ts.URL, owner: owner, hc: ts.Client()},
		by: map[string]*agent{}, loops: map[string]int{}, replay: map[string]string{},
		taskBy: map[string]*model.Task{}, briefBy: map[string]*model.Brief{}, step: -1,
	}
	return s, s.setup()
}

func (s *sim) close() {
	s.ts.Close()
	_ = s.srv.Close()
	_ = os.RemoveAll(s.dir)
}

func (s *sim) logf(format string, args ...any) {
	s.log = append(s.log, fmt.Sprintf(format, args...))
}

func (s *sim) violation(kind, format string, args ...any) error {
	v := &Violation{Step: s.step, Kind: kind, Detail: fmt.Sprintf(format, args...)}
	s.logf("  VIOLATION %s: %s", kind, v.Detail)
	return v
}

func (s *sim) setup() error {
	rng := rand.New(rand.NewPCG(uint64(s.cfg.Seed), setupSalt)) //nolint:gosec
	type spec struct{ id, role, kind, provider string }
	specs := []spec{
		{"lead", model.RoleLeader, model.KindOther, "anthropic"},
		{"tester-1", model.RoleTester, model.KindOpenCode, ""},
		{"rev-anthropic", model.RoleReviewer, model.KindClaudeCode, ""},
		{"rev-openai", model.RoleReviewer, model.KindCodex, ""},
	}
	kinds := []string{model.KindClaudeCode, model.KindCodex, model.KindOpenCode}
	for i := 0; len(specs) < s.cfg.Agents; i++ {
		k := kinds[i%len(kinds)]
		specs = append(specs, spec{fmt.Sprintf("w%d-%s", i+1, k), model.RoleWorker, k, ""})
	}
	for _, sp := range specs {
		var out struct {
			Agent   model.Agent `json:"agent"`
			Connect struct {
				Token string `json:"token"`
			} `json:"connect"`
		}
		code, ec, err := s.c.rest(s.ctx, "POST", "/api/v1/agents", map[string]any{"id": sp.id, "role": sp.role, "kind": sp.kind, "model": "m", "provider": sp.provider}, &out)
		if err != nil {
			return err
		}
		if code != 201 {
			return s.violation("setup", "register %s: %d %s", sp.id, code, ec)
		}
		a := &agent{id: sp.id, role: sp.role, kind: sp.kind, provider: out.Agent.Provider, token: out.Connect.Token, status: model.AgentActive}
		s.agents = append(s.agents, a)
		s.by[a.id] = a
	}
	s.leader = s.by["lead"]
	for _, b := range []map[string]any{
		{"title": "Payments", "body": "build payments", "repos": []string{"o/pay"}, "max_cost_eur": 30, "max_parallel_agents": 3},
		{"title": "Search", "body": "build search", "repos": []string{"o/search"}, "max_cost_eur": 9, "max_parallel_agents": 2},
	} {
		if code, ec, err := s.c.rest(s.ctx, "POST", "/api/v1/briefs", b, nil); err != nil || code != 201 {
			if err != nil {
				return err
			}
			return s.violation("setup", "create brief: %d %s", code, ec)
		}
	}
	budget := func() map[string]any {
		return map[string]any{"max_minutes": []int{0, 45, 120}[rng.IntN(3)], "max_cost_eur": []float64{0, 2.5, 6}[rng.IntN(3)]}
	}
	plans := []struct {
		brief, repo string
		tasks       [][3]string
	}{
		{"B-1", "o/pay", [][3]string{{"a", "", model.RoleWorker}, {"b", "a", model.RoleWorker}, {"c", "", model.RoleWorker}, {"d", "c", model.RoleTester}, {"e", "", model.RoleWorker}, {"f", "", model.RoleWorker}}},
		{"B-2", "o/search", [][3]string{{"g", "", model.RoleWorker}, {"h", "g", model.RoleWorker}, {"i", "", model.RoleWorker}, {"j", "", model.RoleTester}}},
	}
	var first string
	for _, p := range plans {
		var ts []any
		for _, t := range p.tasks {
			m := map[string]any{"ref": t[0], "title": "task " + t[0], "spec": "do " + t[0], "repo": p.repo,
				"acceptance_criteria": []string{"works"}, "role": t[2], "budget": budget()}
			if t[1] != "" {
				m["depends_on"] = []string{t[1]}
			}
			ts = append(ts, m)
		}
		r, err := s.call(s.leader, "propose_plan", map[string]any{"brief_id": p.brief, "tasks": ts})
		if err != nil {
			return err
		}
		if !r.ok() {
			return s.violation("setup", "propose_plan %s: %s", p.brief, r.outcome())
		}
		var out struct {
			Plan model.Plan `json:"plan"`
		}
		if err := json.Unmarshal(r.Body, &out); err != nil {
			return err
		}
		if p.brief == "B-1" {
			first = out.Plan.ID
		}
	}
	if err := s.approvePlan(first); err != nil {
		return err
	}
	return s.check()
}

func (s *sim) run(ops []Op) error {
	for i, op := range ops {
		s.step = i
		s.logf("#%d %s", i, op.Kind)
		if err := s.advance(time.Second); err != nil {
			return err
		}
		r := rand.New(rand.NewPCG(op.R, opStream)) //nolint:gosec
		if err := s.do(op.Kind, r); err != nil {
			return err
		}
		if err := s.check(); err != nil {
			return err
		}
		if i%20 == 19 {
			if err := s.verifyAudit(); err != nil {
				return err
			}
		}
	}
	return s.verifyAudit()
}

func (s *sim) do(k Kind, r *rand.Rand) error {
	switch k {
	case opLeader:
		return s.leaderAct(r)
	case opWorker:
		ws := s.agentsWith(func(a *agent) bool { return a.role == model.RoleWorker || a.role == model.RoleTester })
		return s.workerAct(ws[r.IntN(len(ws))], r)
	case opReviewer:
		rs := s.agentsWith(func(a *agent) bool { return a.role == model.RoleReviewer })
		return s.reviewerAct(rs[r.IntN(len(rs))], r)
	case opOwner:
		return s.ownerAct(r)
	case opTick:
		d := time.Duration(10+r.IntN(230)) * time.Second
		s.logf("  clock +%s", d)
		return s.advance(d)
	case opExpire:
		d := lease + time.Duration(1+r.IntN(10))*time.Minute
		s.logf("  clock +%s", d)
		return s.advance(d)
	case opCrash:
		return s.crash(r)
	case opReplay:
		return s.replayCall(r)
	case opPause:
		return s.togglePause()
	case opRevoke:
		return s.revoke(r)
	case opBurst:
		return s.burst(r)
	case opLoop:
		return s.loop(r)
	case opForbidden:
		return s.forbidden(r)
	}
	return fmt.Errorf("unknown op %d", k)
}

func (s *sim) advance(d time.Duration) error {
	s.clock.Advance(d)
	s.srv.Board.Flush(s.ctx)
	return s.srv.Board.Sweep(s.ctx)
}

func (s *sim) agentsWith(f func(*agent) bool) []*agent {
	var out []*agent
	for _, a := range s.agents {
		if f(a) {
			out = append(out, a)
		}
	}
	return out
}

func (s *sim) tasksWith(f func(*model.Task) bool) []*model.Task {
	var out []*model.Task
	for _, t := range s.tasks {
		if f(t) {
			out = append(out, t)
		}
	}
	return out
}

func pick[T any](r *rand.Rand, xs []T) T { return xs[r.IntN(len(xs))] }

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (s *sim) loopKey(a *agent, tool string, raw json.RawMessage) string {
	return a.id + "|" + tool + "|" + audit.ArgsHash(raw)
}

func (s *sim) readOnly(a *agent, tool string) bool {
	t, ok := s.reg.Get(tool, a.role)
	return ok && t.ReadOnly
}

func (s *sim) call(a *agent, tool string, args any) (reply, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return reply{}, err
	}
	ro := s.readOnly(a, tool)
	trip := false
	if !ro && a.status == model.AgentActive {
		k := s.loopKey(a, tool, raw)
		if _, seen := s.loops[k]; !seen {
			a.keys++
		}
		s.loops[k]++
		trip = s.loops[k] > audit.DefaultMaxRepeats
	}
	r, err := s.c.mcp(s.ctx, a.token, tool, raw)
	if err != nil {
		return r, err
	}
	s.logf("  %s %s %s -> %s", a.id, tool, raw, r.outcome())
	exact := a.keys < loopKeysCap
	switch {
	case a.status == model.AgentRevoked:
		if r.HTTP != 401 {
			return r, s.violation("revoked_token", "%s called %s with a revoked token: %s", a.id, tool, r.outcome())
		}
		return r, nil
	case a.status == model.AgentPaused && !ro:
		if r.Code != "agent_paused" {
			return r, s.violation("paused_agent", "paused agent %s called %s: %s", a.id, tool, r.outcome())
		}
		return r, nil
	case trip:
		if r.Code == "agent_paused" {
			a.status = model.AgentPaused
			s.logf("  loop protection paused %s", a.id)
		} else if exact {
			return r, s.violation("loop_protection", "call %d of identical %s by %s was not stopped: %s", s.loops[s.loopKey(a, tool, raw)], tool, a.id, r.outcome())
		}
		return r, nil
	case r.Code == "agent_paused":
		if exact {
			return r, s.violation("unexpected_pause", "%s %s: agent_paused without a loop", a.id, tool)
		}
		a.status = model.AgentPaused
		return r, nil
	}
	if r.HTTP != 200 || r.RPC != "" {
		return r, s.violation("transport", "%s %s: %s", a.id, tool, r.outcome())
	}
	if !ro {
		a.last = &call{tool: tool, args: raw}
	}
	return r, nil
}

func (s *sim) expect(a *agent, r reply, what string, codes ...string) error {
	if r.ok() || a.status != model.AgentActive || r.Code == "agent_paused" || slices.Contains(codes, r.Code) {
		return nil
	}
	return s.violation("unexpected_error", "%s: %s", what, r.outcome())
}

func (s *sim) approvePlan(id string) error {
	code, ec, err := s.c.rest(s.ctx, "POST", "/api/v1/plans/"+id+"/approve", map[string]any{"note": "ok"}, nil)
	if err != nil {
		return err
	}
	s.logf("  owner approve_plan %s -> %d %s", id, code, ec)
	if code != 200 {
		return s.violation("unexpected_error", "approve plan %s: %d %s", id, code, ec)
	}
	return nil
}

func (s *sim) leaderAct(r *rand.Rand) error {
	a := s.leader
	active := func(role string) []*agent {
		return s.agentsWith(func(x *agent) bool { return x.role == role && x.status == model.AgentActive })
	}
	x := r.IntN(100)
	if x < 45 {
		if ts := s.tasksWith(func(t *model.Task) bool { return t.Status == model.StatusReady }); len(ts) > 0 {
			t := pick(r, ts)
			if cands := active(t.Role); len(cands) > 0 {
				w := pick(r, cands)
				rep, err := s.call(a, "assign", map[string]any{"task_id": t.ID, "agent_id": w.id})
				if err != nil {
					return err
				}
				return s.expect(a, rep, "assign "+t.ID)
			}
		}
	}
	if x < 65 {
		if ts := s.tasksWith(func(t *model.Task) bool {
			return t.Status == model.StatusSubmitted || t.Status == model.StatusInReview
		}); len(ts) > 0 {
			t := pick(r, ts)
			if cands := active(model.RoleReviewer); len(cands) > 0 {
				rv := pick(r, cands)
				rep, err := s.call(a, "assign", map[string]any{"task_id": t.ID, "agent_id": rv.id, "as": "reviewer"})
				if err != nil {
					return err
				}
				if s.providerOf(deref(t.Assignee)) == rv.provider {
					if rep.ok() {
						return s.violation("reviewer_provider", "%s assigned as reviewer of %s with the worker's provider %s", rv.id, t.ID, rv.provider)
					}
					return s.expect(a, rep, "assign same-provider reviewer", "forbidden")
				}
				return s.expect(a, rep, "assign reviewer "+t.ID)
			}
		}
	}
	if x < 78 {
		if ts := s.tasksWith(func(t *model.Task) bool {
			switch t.Status {
			case model.StatusAssigned, model.StatusInProgress, model.StatusChangesRequested, model.StatusBlocked:
				return true
			}
			return false
		}); len(ts) > 0 {
			stuck := slices.DeleteFunc(slices.Clone(ts), func(t *model.Task) bool { return s.by[deref(t.Assignee)].status == model.AgentActive })
			if len(stuck) > 0 {
				ts = stuck
			}
			t := pick(r, ts)
			cands := slices.DeleteFunc(active(t.Role), func(w *agent) bool { return w.id == deref(t.Assignee) })
			if len(cands) > 0 {
				w := pick(r, cands)
				rep, err := s.call(a, "reassign", map[string]any{"task_id": t.ID, "agent_id": w.id, "reason": "rebalance"})
				if err != nil {
					return err
				}
				return s.expect(a, rep, "reassign "+t.ID)
			}
		}
	}
	if x < 88 && len(s.tasks) < maxTasks {
		br := pick(r, s.briefs)
		in := map[string]any{"brief_id": br.ID, "title": fmt.Sprintf("extra %d", len(s.tasks)+1), "spec": "more work",
			"repo": br.Repos[0], "acceptance_criteria": []string{"done"}, "role": model.RoleWorker,
			"budget": map[string]any{"max_minutes": 60 * r.IntN(3), "max_cost_eur": float64(r.IntN(4))}}
		if r.IntN(5) == 0 {
			in["role"] = model.RoleTester
		}
		if deps := s.tasksWith(func(t *model.Task) bool { return t.BriefID == br.ID }); len(deps) > 0 && r.IntN(3) == 0 {
			in["depends_on"] = []string{pick(r, deps).ID}
		}
		rep, err := s.call(a, "create_task", in)
		if err != nil {
			return err
		}
		return s.expect(a, rep, "create_task")
	}
	if x < 95 {
		if ts := s.tasksWith(func(t *model.Task) bool { return t.Status == model.StatusBlocked }); len(ts) > 0 {
			t := pick(r, ts)
			rep, err := s.call(a, "update_task", map[string]any{"task_id": t.ID, "status": "ready"})
			if err != nil {
				return err
			}
			return s.expect(a, rep, "unblock "+t.ID)
		}
	} else if ts := s.tasksWith(func(t *model.Task) bool { return t.Status == model.StatusInProgress }); len(ts) > 0 {
		t := pick(r, ts)
		rep, err := s.call(a, "update_task", map[string]any{"task_id": t.ID, "status": "blocked"})
		if err != nil {
			return err
		}
		return s.expect(a, rep, "block "+t.ID)
	}
	_, err := s.call(a, "board", map[string]any{})
	return err
}

func (s *sim) providerOf(id string) string {
	if a, ok := s.by[id]; ok {
		return a.provider
	}
	return ""
}

func (s *sim) workerAct(a *agent, r *rand.Rand) error {
	if a.downUntil > s.step {
		s.logf("  %s is down", a.id)
		return nil
	}
	rep, err := s.call(a, "list_my_tasks", map[string]any{})
	if err != nil || !rep.ok() {
		return err
	}
	var out struct {
		Tasks []*model.Task `json:"tasks"`
	}
	if err := json.Unmarshal(rep.Body, &out); err != nil {
		return err
	}
	var held, claimable []*model.Task
	for _, t := range out.Tasks {
		if deref(t.Assignee) != a.id {
			continue
		}
		switch t.Status {
		case model.StatusInProgress:
			held = append(held, t)
		case model.StatusAssigned, model.StatusChangesRequested:
			claimable = append(claimable, t)
		}
	}
	if len(held) > 0 {
		return s.work(a, pick(r, held), r)
	}
	if len(claimable) > 0 {
		return s.claim(a, pick(r, claimable))
	}
	return s.heartbeat(a, "")
}

func (s *sim) heartbeat(a *agent, taskID string) error {
	args := map[string]any{}
	if taskID != "" {
		args["task_id"] = taskID
	}
	rep, err := s.call(a, "heartbeat", args)
	if err != nil || !rep.ok() {
		if err == nil && taskID != "" {
			err = s.expect(a, rep, "heartbeat "+taskID, "not_assigned")
		}
		return err
	}
	var hb struct {
		Paused bool `json:"paused"`
	}
	if err := json.Unmarshal(rep.Body, &hb); err != nil {
		return err
	}
	if hb.Paused != s.paused {
		return s.violation("pause_flag", "heartbeat paused=%v while pause_all=%v", hb.Paused, s.paused)
	}
	return nil
}

func (s *sim) work(a *agent, t *model.Task, r *rand.Rand) error {
	x := r.IntN(100)
	switch {
	case x < 40:
		return s.heartbeat(a, t.ID)
	case x < 60:
		cost := float64(int((t.CostUsed+0.2+r.Float64()*1.3)*100)) / 100
		rep, err := s.call(a, "update_status", map[string]any{"task_id": t.ID, "status": "in_progress", "progress_pct": 10 + r.IntN(80), "note": "working", "cost_eur": cost})
		if err != nil {
			return err
		}
		return s.expect(a, rep, "update_status "+t.ID, "budget_exceeded")
	case x < 85:
		if a.role == model.RoleTester {
			rep, err := s.call(a, "report_tests", map[string]any{"task_id": t.ID, "passed": 10 + r.IntN(5), "failed": r.IntN(2)})
			if err != nil {
				return err
			}
			if err := s.expect(a, rep, "report_tests "+t.ID); err != nil || !rep.ok() {
				return err
			}
			rep, err = s.call(a, "submit", map[string]any{"task_id": t.ID, "summary": "tests added"})
			if err != nil {
				return err
			}
			return s.expect(a, rep, "submit "+t.ID)
		}
		rep, err := s.call(a, "submit", map[string]any{"task_id": t.ID, "pr_url": "https://git.example/" + t.Repo + "/pulls/" + strings.TrimPrefix(t.ID, "T-"), "summary": "implemented"})
		if err != nil {
			return err
		}
		return s.expect(a, rep, "submit "+t.ID)
	case x < 92:
		rep, err := s.call(a, "release_task", map[string]any{"task_id": t.ID, "reason": "switching"})
		if err != nil {
			return err
		}
		return s.expect(a, rep, "release "+t.ID)
	}
	rep, err := s.call(a, "update_status", map[string]any{"task_id": t.ID, "status": "blocked", "note": "need input"})
	if err != nil {
		return err
	}
	return s.expect(a, rep, "block "+t.ID)
}

func (s *sim) claim(a *agent, t *model.Task) error {
	rep, err := s.call(a, "claim_task", map[string]any{"task_id": t.ID})
	if err != nil || !rep.ok() {
		return err
	}
	return s.checkClaim(a, t.ID)
}

func (s *sim) checkClaim(a *agent, id string) error {
	t := s.taskBy[id]
	if t == nil {
		return s.violation("claim", "%s claimed unknown task %s", a.id, id)
	}
	if s.paused {
		return s.violation("claim_while_paused", "%s claimed %s during pause_all", a.id, id)
	}
	if t.Status == model.StatusInProgress {
		return s.violation("double_holder", "%s claimed %s already held by %s", a.id, id, deref(t.Assignee))
	}
	if t.Status != model.StatusAssigned && t.Status != model.StatusChangesRequested {
		return s.violation("transition", "%s claimed %s from %s", a.id, id, t.Status)
	}
	if deref(t.Assignee) != a.id {
		return s.violation("double_holder", "%s claimed %s assigned to %s", a.id, id, deref(t.Assignee))
	}
	for _, d := range t.DependsOn {
		if dt := s.taskBy[d]; dt == nil || dt.Status != model.StatusDone {
			return s.violation("dependency", "%s claimed %s before dependency %s was done", a.id, id, d)
		}
	}
	if t.Budget.MaxCostEUR > 0 && t.CostUsed >= t.Budget.MaxCostEUR || t.Budget.MaxMinutes > 0 && t.MinutesUsed >= t.Budget.MaxMinutes {
		return s.violation("task_budget", "%s claimed %s with exhausted budget", a.id, id)
	}
	br := s.briefBy[t.BriefID]
	if br.Budget.MaxCostEUR > 0 && br.CostUsed >= br.Budget.MaxCostEUR {
		return s.violation("brief_budget", "%s claimed %s although brief %s spent %.2f of %.2f", a.id, id, br.ID, br.CostUsed, br.Budget.MaxCostEUR)
	}
	return nil
}

func (s *sim) reviewerAct(a *agent, r *rand.Rand) error {
	rep, err := s.call(a, "list_reviews", map[string]any{})
	if err != nil || !rep.ok() {
		return err
	}
	var out struct {
		Tasks []*model.Task `json:"tasks"`
	}
	if err := json.Unmarshal(rep.Body, &out); err != nil {
		return err
	}
	for _, t := range out.Tasks {
		if s.providerOf(deref(t.Assignee)) == a.provider {
			return s.violation("reviewer_provider", "list_reviews offered %s to %s with the same provider", t.ID, a.id)
		}
	}
	if len(out.Tasks) > 0 && r.IntN(10) < 8 {
		t := pick(r, out.Tasks)
		args := map[string]any{"task_id": t.ID, "verdict": "approve", "comments": "lgtm"}
		if r.IntN(100) >= 55 {
			args["verdict"], args["comments"] = "changes_requested", "please fix "+t.ID
		}
		rep, err := s.call(a, "review", args)
		if err != nil {
			return err
		}
		return s.expect(a, rep, "review "+t.ID)
	}
	same := s.tasksWith(func(t *model.Task) bool {
		return (t.Status == model.StatusSubmitted || t.Status == model.StatusInReview) && s.providerOf(deref(t.Assignee)) == a.provider
	})
	if len(same) == 0 {
		return nil
	}
	t := pick(r, same)
	rep, err = s.call(a, "review", map[string]any{"task_id": t.ID, "verdict": "approve", "comments": "self review"})
	if err != nil {
		return err
	}
	if rep.ok() {
		return s.violation("reviewer_provider", "%s approved %s of a worker with the same provider", a.id, t.ID)
	}
	return s.expect(a, rep, "same-provider review", "forbidden", "not_assigned")
}

func (s *sim) ownerAct(r *rand.Rand) error {
	var acts []func() error
	var pl struct {
		Plans []model.Plan `json:"plans"`
	}
	if _, _, err := s.c.rest(s.ctx, "GET", "/api/v1/plans?status=proposed", nil, &pl); err != nil {
		return err
	}
	if len(pl.Plans) > 0 {
		id := pl.Plans[0].ID
		acts = append(acts, func() error { return s.approvePlan(id) })
	}
	for _, a := range s.agents {
		if a.status == model.AgentPaused {
			acts = append(acts, func() error {
				code, ec, err := s.c.rest(s.ctx, "POST", "/api/v1/agents/"+a.id+"/resume", map[string]any{}, nil)
				if err != nil {
					return err
				}
				s.logf("  owner resume %s -> %d %s", a.id, code, ec)
				if code != 200 {
					return s.violation("unexpected_error", "resume %s: %d %s", a.id, code, ec)
				}
				a.status = model.AgentActive
				return nil
			})
			break
		}
	}
	if ts := s.tasksWith(func(t *model.Task) bool { return t.Status == model.StatusApproved }); len(ts) > 0 {
		t := pick(r, ts)
		done := func() error { return s.ownerTask("POST", t.ID, "/done", map[string]any{"note": "merged"}, 200) }
		acts = append(acts, done, done)
	}
	if ts := s.tasksWith(func(t *model.Task) bool { return t.Status == model.StatusNeedsOwner }); len(ts) > 0 {
		t := pick(r, ts)
		acts = append(acts, func() error {
			return s.ownerTask("POST", t.ID, "/reopen", map[string]any{"status": "ready", "note": "try again"}, 200)
		})
	}
	if ts := s.tasksWith(func(t *model.Task) bool {
		return t.Status == model.StatusBlocked && strings.HasPrefix(deref(t.BlockedReason), "budget_exceeded")
	}); len(ts) > 0 {
		t := pick(r, ts)
		acts = append(acts, func() error {
			b := map[string]any{"max_minutes": t.MinutesUsed + 120, "max_cost_eur": t.CostUsed + 5}
			return s.ownerTask("PATCH", t.ID, "", map[string]any{"budget": b}, 200)
		})
	}
	if ts := s.tasksWith(func(t *model.Task) bool {
		return t.Status != model.StatusApproved && t.Status != model.StatusNeedsOwner
	}); len(ts) > 0 {
		t := pick(r, ts)
		acts = append(acts, func() error { return s.ownerTask("POST", t.ID, "/done", map[string]any{"note": "too early"}, 409) })
	}
	if ts := s.tasksWith(func(t *model.Task) bool {
		return t.Status != model.StatusDone && t.Status != model.StatusCancelled
	}); len(ts) > 0 && r.IntN(4) == 0 {
		t := pick(r, ts)
		acts = append(acts, func() error { return s.ownerTask("POST", t.ID, "/cancel", map[string]any{"reason": "descoped"}, 200) })
	}
	if len(acts) == 0 {
		return nil
	}
	return pick(r, acts)()
}

func (s *sim) ownerTask(method, id, suffix string, body any, want int) error {
	code, ec, err := s.c.rest(s.ctx, method, "/api/v1/tasks/"+id+suffix, body, nil)
	if err != nil {
		return err
	}
	s.logf("  owner %s %s%s -> %d %s", method, id, suffix, code, ec)
	if code != want {
		kind := "unexpected_error"
		if suffix == "/done" && code == 200 {
			kind = "done_without_approval"
		}
		return s.violation(kind, "%s %s%s: got %d %s, want %d", method, id, suffix, code, ec, want)
	}
	return nil
}

func (s *sim) crash(r *rand.Rand) error {
	held := s.agentsWith(func(a *agent) bool {
		for _, t := range s.tasks {
			if t.Status == model.StatusInProgress && deref(t.Assignee) == a.id {
				return a.downUntil <= s.step
			}
		}
		return false
	})
	if len(held) == 0 {
		return nil
	}
	a := pick(r, held)
	a.downUntil = s.step + 3 + r.IntN(12)
	s.logf("  %s crashes until step %d", a.id, a.downUntil)
	return nil
}

func (s *sim) replayCall(r *rand.Rand) error {
	as := s.agentsWith(func(a *agent) bool { return a.last != nil && a.status != model.AgentRevoked && a.downUntil <= s.step })
	if len(as) == 0 {
		return nil
	}
	a := pick(r, as)
	s.logf("  replay last call of %s", a.id)
	rep, err := s.call(a, a.last.tool, a.last.args)
	if err != nil || !rep.ok() || a.last.tool != "claim_task" {
		return err
	}
	var in struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(a.last.args, &in); err != nil {
		return err
	}
	return s.checkClaim(a, in.TaskID)
}

func (s *sim) togglePause() error {
	path, want := "/api/v1/pause-all", true
	if s.paused {
		path, want = "/api/v1/resume-all", false
	}
	code, ec, err := s.c.rest(s.ctx, "POST", path, map[string]any{"reason": "dst"}, nil)
	if err != nil {
		return err
	}
	s.logf("  owner %s -> %d %s", path, code, ec)
	if code != 200 {
		return s.violation("unexpected_error", "%s: %d %s", path, code, ec)
	}
	s.paused = want
	var st struct {
		Paused bool `json:"paused"`
	}
	if _, _, err := s.c.rest(s.ctx, "GET", "/api/v1/state", nil, &st); err != nil {
		return err
	}
	if st.Paused != want {
		return s.violation("pause_flag", "state paused=%v after %s", st.Paused, path)
	}
	return nil
}

func (s *sim) revoke(r *rand.Rand) error {
	ws := s.agentsWith(func(a *agent) bool { return a.role == model.RoleWorker && a.status != model.AgentRevoked })
	if len(ws) <= 2 {
		return nil
	}
	a := pick(r, ws)
	code, ec, err := s.c.rest(s.ctx, "POST", "/api/v1/agents/"+a.id+"/revoke", map[string]any{"reason": "dst"}, nil)
	if err != nil {
		return err
	}
	s.logf("  owner revoke %s -> %d %s", a.id, code, ec)
	if code != 200 {
		return s.violation("unexpected_error", "revoke %s: %d %s", a.id, code, ec)
	}
	a.status = model.AgentRevoked
	_, err = s.call(a, "whoami", map[string]any{})
	return err
}

type burstResult struct {
	who string
	rep reply
	err error
}

func (s *sim) burst(r *rand.Rand) error {
	ts := s.tasksWith(func(t *model.Task) bool {
		if t.Status != model.StatusAssigned && t.Status != model.StatusChangesRequested {
			return false
		}
		a := s.by[deref(t.Assignee)]
		return a != nil && a.status == model.AgentActive && a.downUntil <= s.step
	})
	if len(ts) == 0 {
		return nil
	}
	t := pick(r, ts)
	a := s.by[deref(t.Assignee)]
	raw, err := json.Marshal(map[string]any{"task_id": t.ID})
	if err != nil {
		return err
	}
	ka := s.loopKey(a, "claim_task", raw)
	n := min(2+r.IntN(3), audit.DefaultMaxRepeats-s.loops[ka])
	if n < 2 {
		return s.claim(a, t)
	}
	callers := make([]*agent, 0, n+1)
	for range n {
		callers = append(callers, a)
	}
	others := s.agentsWith(func(o *agent) bool {
		return o != a && o.role == a.role && o.status == model.AgentActive && s.loops[s.loopKey(o, "claim_task", raw)] < audit.DefaultMaxRepeats
	})
	if len(others) > 0 {
		callers = append(callers, pick(r, others))
	}
	for _, c := range callers {
		k := s.loopKey(c, "claim_task", raw)
		if _, seen := s.loops[k]; !seen {
			c.keys++
		}
		s.loops[k]++
	}
	res := make(chan burstResult, len(callers))
	for _, c := range callers {
		go func() {
			rep, err := s.c.mcp(s.ctx, c.token, "claim_task", raw)
			res <- burstResult{c.id, rep, err}
		}()
	}
	var outs []string
	wins := map[string]int{}
	for range callers {
		br := <-res
		if br.err != nil {
			return br.err
		}
		outs = append(outs, br.who+":"+br.rep.outcome())
		if br.rep.ok() {
			wins[br.who]++
		}
	}
	slices.Sort(outs)
	s.logf("  burst claim_task %s %s", t.ID, strings.Join(outs, " "))
	a.last = &call{tool: "claim_task", args: raw}
	total := 0
	for who, n := range wins {
		total += n
		if who != a.id {
			return s.violation("double_holder", "%s claimed %s assigned to %s", who, t.ID, a.id)
		}
	}
	for _, o := range outs {
		if strings.HasSuffix(o, ":agent_paused") || strings.Contains(o, ":http") || strings.Contains(o, ":rpc") {
			return s.violation("unexpected_error", "burst on %s: %s", t.ID, o)
		}
	}
	if total > 1 {
		return s.violation("double_holder", "%d concurrent claims of %s succeeded", total, t.ID)
	}
	if total == 1 {
		return s.checkClaim(a, t.ID)
	}
	return nil
}

func (s *sim) loop(r *rand.Rand) error {
	as := s.agentsWith(func(a *agent) bool { return a.status == model.AgentActive && a.downUntil <= s.step })
	if len(as) == 0 {
		return nil
	}
	a := pick(r, as)
	args := map[string]any{"body": "still stuck on the same thing"}
	for i := 0; i <= audit.DefaultMaxRepeats && a.status == model.AgentActive; i++ {
		if _, err := s.call(a, "add_note", args); err != nil {
			return err
		}
	}
	if a.status != model.AgentPaused && a.keys < loopKeysCap {
		return s.violation("loop_protection", "%s repeated add_note without being paused", a.id)
	}
	return nil
}

func (s *sim) forbidden(r *rand.Rand) error {
	a := pick(r, s.agentsWith(func(a *agent) bool { return a.status != model.AgentRevoked }))
	tool := map[string]string{model.RoleLeader: "claim_task", model.RoleWorker: "assign", model.RoleTester: "review", model.RoleReviewer: "submit"}[a.role]
	raw, err := json.Marshal(map[string]any{"task_id": "T-1", "agent_id": a.id, "verdict": "approve", "comments": "x"})
	if err != nil {
		return err
	}
	rep, err := s.c.mcp(s.ctx, a.token, tool, raw)
	if err != nil {
		return err
	}
	s.logf("  %s %s (not in role) -> %s", a.id, tool, rep.outcome())
	if rep.RPC == "" {
		return s.violation("role_tools", "%s (%s) could call %s: %s", a.id, a.role, tool, rep.outcome())
	}
	return nil
}

func taskNum(id string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(id, "T-"))
	return n
}
