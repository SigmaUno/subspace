package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/model"
)

const (
	soakRepo  = "soak/sandbox"
	bulkBatch = 6
)

type agent struct {
	id, role, provider, token string
}

type roster struct {
	mu        sync.Mutex
	pool      map[string][]*agent
	live      map[string]*agent
	dead      map[string]time.Time
	reviewers []*agent
	batch     int
}

func newRoster() *roster {
	return &roster{pool: map[string][]*agent{}, live: map[string]*agent{}, dead: map[string]time.Time{}}
}

func kindFor(role string, batch int) string {
	switch role {
	case model.RoleReviewer:
		return model.KindCodex
	case model.RoleTester:
		return model.KindOpenCode
	case model.RoleLeader:
		return model.KindOther
	}
	if batch%2 == 0 {
		return model.KindClaudeCode
	}
	return model.KindOpenCode
}

func (h *harness) register(ctx context.Context, role string, n int) ([]*agent, error) {
	h.roster.mu.Lock()
	h.roster.batch++
	b := h.roster.batch
	h.roster.mu.Unlock()
	kind := kindFor(role, b)
	in := board.BulkInput{Prefix: fmt.Sprintf("%c%d", role[0], b), Count: n, Role: role, Kind: kind, Model: "soak"}
	if role == model.RoleLeader {
		in.Provider = "soak"
	}
	regs, err := h.owner.RegisterBulk(ctx, in)
	if err != nil {
		return nil, err
	}
	h.count("bulk_register")
	out := make([]*agent, len(regs))
	for i, r := range regs {
		out[i] = &agent{id: r.Agent.ID, role: role, provider: r.Agent.Provider, token: r.Connect.Token}
	}
	return out, nil
}

func (h *harness) take(ctx context.Context, role string) (*agent, error) {
	h.roster.mu.Lock()
	if p := h.roster.pool[role]; len(p) > 0 {
		a := p[0]
		h.roster.pool[role] = p[1:]
		h.roster.mu.Unlock()
		return a, nil
	}
	h.roster.mu.Unlock()
	as, err := h.register(ctx, role, bulkBatch)
	if err != nil {
		return nil, err
	}
	h.roster.mu.Lock()
	defer h.roster.mu.Unlock()
	h.roster.pool[role] = append(h.roster.pool[role], as[1:]...)
	return as[0], nil
}

func (r *roster) join(a *agent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.live[a.id] = a
}

func (r *roster) leave(id string, crashed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.live, id)
	if crashed {
		r.dead[id] = time.Now()
	}
}

func (r *roster) isLive(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.live[id]
	return ok
}

func (r *roster) pick(rnd *rand.Rand, role, notProvider string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []string
	cands := r.reviewers
	if role != model.RoleReviewer {
		cands = nil
		for _, a := range r.live {
			cands = append(cands, a)
		}
	}
	for _, a := range cands {
		if a.role == role && a.provider != notProvider {
			ids = append(ids, a.id)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	return ids[rnd.IntN(len(ids))]
}

func (r *roster) provider(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a, ok := r.live[id]; ok {
		return a.provider
	}
	return ""
}

func (r *roster) reap(older time.Duration) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for id, at := range r.dead {
		if time.Since(at) > older {
			out = append(out, id)
			delete(r.dead, id)
		}
	}
	return out
}

func (r *roster) idle(rnd *rand.Rand) *agent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var all []*agent
	for _, p := range r.pool {
		all = append(all, p...)
	}
	if len(all) == 0 {
		return nil
	}
	return all[rnd.IntN(len(all))]
}

func (r *roster) keep(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.live[id]; ok {
		return true
	}
	for _, a := range r.reviewers {
		if a.id == id {
			return true
		}
	}
	return false
}

func pause(ctx context.Context, r *rand.Rand, lo, hi time.Duration) {
	d := lo
	if hi > lo {
		d += time.Duration(r.Int64N(int64(hi - lo)))
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func every(ctx context.Context, r *rand.Rand, d time.Duration, fn func()) {
	if d <= 0 {
		return
	}
	for {
		pause(ctx, r, d*3/4, d*5/4)
		if ctx.Err() != nil {
			return
		}
		fn()
	}
}

func (h *harness) slot(ctx context.Context, role string, r *rand.Rand) {
	for ctx.Err() == nil {
		a, err := h.take(ctx, role)
		if err != nil {
			pause(ctx, r, time.Second, 2*time.Second)
			continue
		}
		h.runAgent(ctx, a, r)
	}
}

func (h *harness) runAgent(ctx context.Context, a *agent, r *rand.Rand) {
	life := h.cfg.AgentLife/4 + time.Duration(r.Int64N(int64(h.cfg.AgentLife*3/4)+1))
	actx, cancel := context.WithTimeout(ctx, life)
	defer cancel()
	m := h.mcp(a.token)
	h.roster.join(a)
	h.count("agent_join")
	if err := m.Initialize(actx); err == nil {
		_, _ = m.GetInstructions(actx)
	}
	failed := map[string]int{}
	for actx.Err() == nil {
		h.agentStep(actx, m, a, r, failed)
	}
	if ctx.Err() != nil {
		h.roster.leave(a.id, false)
		return
	}
	if r.IntN(10) < 4 {
		h.roster.leave(a.id, true)
		h.count("agent_crash")
		return
	}
	lctx, lcancel := context.WithTimeout(ctx, 10*time.Second)
	defer lcancel()
	if mine, err := m.ListMyTasks(lctx, model.StatusInProgress); err == nil {
		for _, t := range mine {
			_, _ = m.ReleaseTask(lctx, t.ID, "leaving")
		}
	}
	_ = m.Close(lctx)
	h.roster.leave(a.id, false)
	h.count("agent_leave")
	if r.IntN(2) == 0 {
		if _, err := h.owner.RevokeAgent(lctx, a.id, "left"); err == nil {
			h.count("revoke")
		}
	}
}

func (h *harness) agentStep(ctx context.Context, m *client.MCP, a *agent, r *rand.Rand, failed map[string]int) {
	mine, err := m.ListMyTasks(ctx, model.StatusAssigned, model.StatusChangesRequested, model.StatusInProgress)
	var cands []*model.Task
	for _, t := range mine {
		if failed[t.ID] < 3 {
			cands = append(cands, t)
		}
	}
	if err != nil || len(cands) == 0 {
		pause(ctx, r, 300*time.Millisecond, 1500*time.Millisecond)
		return
	}
	t := cands[r.IntN(len(cands))]
	if t.Status != model.StatusInProgress {
		ct, err := m.ClaimTask(ctx, t.ID)
		if err != nil {
			failed[t.ID]++
			pause(ctx, r, 300*time.Millisecond, time.Second)
			return
		}
		h.count("claim")
		t = ct
	}
	h.work(ctx, m, a, t, r)
}

func (h *harness) work(ctx context.Context, m *client.MCP, a *agent, t *model.Task, r *rand.Rand) {
	steps := 1 + r.IntN(5)
	cost := t.CostUsed
	for i := range steps {
		pause(ctx, r, 200*time.Millisecond, 1500*time.Millisecond)
		if ctx.Err() != nil {
			return
		}
		if _, err := m.Heartbeat(ctx, t.ID); err != nil {
			return
		}
		if r.IntN(2) == 0 {
			cost += 0.05 + r.Float64()/2
			pct := min(99, (i+1)*100/(steps+1))
			if _, err := m.UpdateStatus(ctx, board.StatusInput{TaskID: t.ID, Status: model.StatusInProgress, ProgressPct: &pct,
				CostEUR: &cost, Note: fmt.Sprintf("step %d of %d", i+1, steps)}); err != nil {
				return
			}
		}
		switch r.IntN(40) {
		case 0:
			_, _ = m.AddNote(ctx, t.ID, fmt.Sprintf("note %d from %s", h.seq.Add(1), a.id))
		case 1:
			if _, err := m.AskOwner(ctx, t.ID, fmt.Sprintf("question %d: which way?", h.seq.Add(1)), "a", "b"); err == nil {
				h.count("ask_owner")
			}
		case 2:
			if _, err := m.UpdateStatus(ctx, board.StatusInput{TaskID: t.ID, Status: model.StatusBlocked, Note: fmt.Sprintf("stuck %d", h.seq.Add(1))}); err == nil {
				h.count("block")
			}
			return
		}
	}
	if a.role == model.RoleTester {
		_, _ = m.ReportTests(ctx, t.ID, 5+r.IntN(50), r.IntN(3), "soak run")
	}
	if _, err := m.Submit(ctx, board.SubmitInput{TaskID: t.ID, PRURL: h.prURL(t),
		Summary: "soak change"}); err == nil {
		h.count("submit")
	}
}

func (h *harness) leaderLoop(ctx context.Context, m *client.MCP, r *rand.Rand) {
	if err := m.Initialize(ctx); err == nil {
		_, _ = m.GetInstructions(ctx)
	}
	for ctx.Err() == nil {
		pause(ctx, r, 300*time.Millisecond, time.Second)
		briefs, err := m.ListBriefs(ctx, "open")
		if err != nil {
			continue
		}
		for _, b := range briefs {
			switch b.PlanStatus {
			case "none", "rejected", "":
				h.propose(ctx, m, b.ID, r)
			case "approved":
				if cols, err := m.Board(ctx, b.ID); err == nil {
					h.noteTasks(cols)
					h.lead(ctx, m, b.ID, cols, r)
				}
			}
		}
	}
}

func (h *harness) propose(ctx context.Context, m *client.MCP, briefID string, r *rand.Rand) {
	n := 1 + r.IntN(h.cfg.MaxPlan)
	in := board.PlanInput{BriefID: briefID, Note: fmt.Sprintf("plan %d", h.seq.Add(1))}
	for i := range n {
		t := board.TaskInput{Ref: fmt.Sprintf("k%d", i), Title: fmt.Sprintf("%s task %d", briefID, i), Spec: "Soak task " + briefID,
			AcceptanceCriteria: []string{"tests pass"}, Repo: soakRepo, Role: model.RoleWorker, Priority: r.IntN(3)}
		if h.cfg.Testers > 0 && r.IntN(6) == 0 {
			t.Role = model.RoleTester
		}
		if i > 0 && r.IntN(4) == 0 {
			t.DependsOn = []string{fmt.Sprintf("k%d", r.IntN(i))}
		}
		if r.IntN(3) == 0 {
			t.Budget.MaxCostEUR = float64(1 + r.IntN(4))
		}
		in.Tasks = append(in.Tasks, t)
	}
	if _, err := m.ProposePlan(ctx, in); err == nil {
		h.count("propose_plan")
	}
}

func (h *harness) lead(ctx context.Context, m *client.MCP, briefID string, cols map[string][]*model.Task, r *rand.Rand) {
	done := map[string]bool{}
	for _, t := range cols[model.StatusDone] {
		done[t.ID] = true
	}
	for _, t := range cols[model.StatusReady] {
		ready := true
		for _, d := range t.DependsOn {
			ready = ready && done[d]
		}
		if !ready {
			continue
		}
		if id := h.roster.pick(r, t.Role, ""); id != "" {
			if _, err := m.Assign(ctx, t.ID, id, "assignee"); err == nil {
				h.count("assign")
			}
		}
	}
	for _, st := range []string{model.StatusAssigned, model.StatusChangesRequested} {
		for _, t := range cols[st] {
			if t.Assignee == nil || h.roster.isLive(*t.Assignee) {
				continue
			}
			if id := h.roster.pick(r, t.Role, ""); id != "" {
				if _, err := m.Reassign(ctx, t.ID, id, "previous agent left"); err == nil {
					h.count("reassign")
				}
			}
		}
	}
	ready := model.StatusReady
	for _, t := range cols[model.StatusBlocked] {
		why := ""
		if t.BlockedReason != nil {
			why = *t.BlockedReason
		}
		if strings.HasPrefix(why, "budget_exceeded") || strings.HasPrefix(why, "poison_task") || strings.HasPrefix(why, "brief_closed") ||
			time.Since(t.UpdatedAt) < 2*time.Second {
			continue
		}
		if _, err := m.UpdateTask(ctx, t.ID, board.TaskPatch{Status: &ready}); err == nil {
			h.count("unblock_leader")
		}
	}
	for _, t := range cols[model.StatusSubmitted] {
		if t.Reviewer != nil || t.Assignee == nil || r.IntN(3) != 0 {
			continue
		}
		if id := h.roster.pick(r, model.RoleReviewer, h.roster.provider(*t.Assignee)); id != "" {
			_, _ = m.Assign(ctx, t.ID, id, "reviewer")
		}
	}
	switch r.IntN(40) {
	case 0:
		if _, err := m.CreateTask(ctx, board.TaskInput{BriefID: briefID, Title: fmt.Sprintf("follow-up %d", h.seq.Add(1)), Spec: "Follow-up work",
			AcceptanceCriteria: []string{"done"}, Repo: soakRepo, Role: model.RoleWorker}); err == nil {
			h.count("create_task")
		}
	case 1:
		_, _ = m.PostMessage(ctx, "brief:"+briefID, fmt.Sprintf("status update %d", h.seq.Add(1)))
	}
}

func (h *harness) reviewerLoop(ctx context.Context, a *agent, r *rand.Rand) {
	m := h.mcp(a.token)
	if err := m.Initialize(ctx); err == nil {
		_, _ = m.GetInstructions(ctx)
	}
	for ctx.Err() == nil {
		pause(ctx, r, 400*time.Millisecond, 1500*time.Millisecond)
		ts, err := m.ListReviews(ctx)
		if err != nil {
			continue
		}
		r.Shuffle(len(ts), func(i, j int) { ts[i], ts[j] = ts[j], ts[i] })
		for _, t := range ts[:min(3, len(ts))] {
			verdict, note := "approve", "lgtm"
			if r.IntN(4) == 0 {
				verdict, note = "changes_requested", "please fix"
			}
			if _, err := m.Review(ctx, t.ID, verdict, fmt.Sprintf("%s (%d)", note, h.seq.Add(1))); err == nil {
				h.count("review_" + verdict)
			}
		}
	}
}

func (h *harness) ownerLoop(ctx context.Context, r *rand.Rand) {
	var lastBriefs, lastAgents time.Time
	for ctx.Err() == nil {
		pause(ctx, r, 300*time.Millisecond, time.Second)
		h.inbox(ctx, r)
		if time.Since(lastBriefs) > 2*time.Second {
			h.briefs(ctx, r)
			lastBriefs = time.Now()
		}
		if time.Since(lastAgents) > 5*time.Second {
			h.agents(ctx, r)
			lastAgents = time.Now()
		}
	}
}

func (h *harness) inbox(ctx context.Context, r *rand.Rand) {
	ds, err := h.owner.Decisions(ctx, 25)
	if err != nil {
		return
	}
	r.Shuffle(len(ds), func(i, j int) { ds[i], ds[j] = ds[j], ds[i] })
	for _, d := range ds[:min(4, len(ds))] {
		var a client.DecisionAction
		switch d.Kind {
		case decisions.KindQuestion:
			a = client.DecisionAction{Action: decisions.ActionAnswer, Answer: "go with a"}
		case decisions.KindPlan:
			a = client.DecisionAction{Action: decisions.ActionApprove}
			if r.IntN(10) == 0 {
				a = client.DecisionAction{Action: decisions.ActionReject, Note: "split it differently"}
			}
		case decisions.KindMerge:
			if r.IntN(2) == 0 && h.enqueueMerge(ctx, d.TaskID) {
				continue
			}
			a = client.DecisionAction{Action: decisions.ActionMerge, Note: "merged"}
		case decisions.KindUnblock:
			h.unblock(ctx, d)
			continue
		default:
			continue
		}
		if _, err := h.owner.ActOnDecision(ctx, d.ID, a); err == nil {
			h.count("decision_" + a.Action)
		}
	}
}

func (h *harness) unblock(ctx context.Context, d *decisions.Decision) {
	t, err := h.owner.GetTask(ctx, d.TaskID)
	if err != nil {
		return
	}
	if t.BlockedReason != nil && strings.HasPrefix(*t.BlockedReason, "budget_exceeded") {
		if br, err := h.owner.GetBrief(ctx, t.BriefID); err == nil && br.Budget.MaxCostEUR > 0 {
			limit := br.CostUsed + 100
			_, _ = h.owner.UpdateBrief(ctx, br.ID, board.BriefPatch{MaxCostEUR: &limit})
		}
		if t.Budget.MaxCostEUR > 0 {
			if nt, err := h.owner.SetTaskBudget(ctx, t.ID, model.Budget{MaxCostEUR: t.CostUsed + 5}); err == nil {
				h.count("raise_budget")
				if nt.Status != model.StatusBlocked {
					return
				}
			}
		}
	}
	if _, err := h.owner.ActOnDecision(ctx, d.ID, client.DecisionAction{Action: decisions.ActionUnblock, Note: "retry"}); err == nil {
		h.count("decision_unblock")
		return
	}
	if _, err := h.owner.CancelTask(ctx, t.ID, "cannot unblock"); err == nil {
		h.count("cancel")
	}
}

func (h *harness) briefs(ctx context.Context, r *rand.Rand) {
	bs, err := h.owner.ListBriefs(ctx, "open")
	if err != nil {
		return
	}
	open := len(bs)
	for _, b := range bs {
		finished := false
		if time.Since(b.CreatedAt) > h.cfg.BriefLife {
			finished = true
		} else if b.PlanStatus == "approved" {
			ts, err := h.owner.ListTasks(ctx, board.TaskQuery{BriefID: b.ID, Limit: 1000})
			finished = err == nil && len(ts) > 0
			for _, t := range ts {
				finished = finished && (t.Status == model.StatusDone || t.Status == model.StatusCancelled)
			}
		}
		if !finished {
			continue
		}
		closed := "closed"
		if _, err := h.owner.UpdateBrief(ctx, b.ID, board.BriefPatch{Status: &closed}); err == nil {
			h.count("close_brief")
			open--
		}
	}
	for ; open < h.cfg.Briefs; open++ {
		cost := 500.0
		if r.IntN(8) == 0 {
			cost = 3
		}
		if _, err := h.owner.CreateBrief(ctx, board.BriefInput{Title: fmt.Sprintf("Soak brief %d", h.seq.Add(1)), Body: "Synthetic soak brief.",
			Repos: []string{soakRepo}, MaxCostEUR: cost, MaxParallel: 3 + r.IntN(12)}); err == nil {
			h.count("create_brief")
		}
	}
}

func (h *harness) agents(ctx context.Context, r *rand.Rand) {
	if ps, err := h.owner.ListAgents(ctx, "", model.AgentPaused); err == nil {
		for _, a := range ps {
			if a.Role == model.RoleLeader || h.roster.keep(a.ID) {
				if _, err := h.owner.ResumeAgent(ctx, a.ID); err == nil {
					h.count("resume_agent")
				}
			}
		}
	}
	for _, id := range h.roster.reap(2 * h.cfg.Lease) {
		if _, err := h.owner.RevokeAgent(ctx, id, "crashed"); err == nil {
			h.count("revoke")
		}
	}
	if r.IntN(4) == 0 {
		if a := h.roster.idle(r); a != nil {
			if reg, err := h.owner.RotateToken(ctx, a.id); err == nil {
				h.roster.mu.Lock()
				a.token = reg.Connect.Token
				h.roster.mu.Unlock()
				h.count("rotate_token")
			}
		}
	}
	if r.IntN(6) == 0 {
		if _, err := h.owner.SetCharter(ctx, fmt.Sprintf("Soak charter revision %d.", h.seq.Add(1))); err == nil {
			h.count("set_charter")
		}
	}
}

func (h *harness) pauseCycle(ctx context.Context, r *rand.Rand) {
	if err := h.owner.PauseAll(ctx, "soak pause"); err != nil {
		return
	}
	h.count("pause_all")
	pause(ctx, r, time.Second, 4*time.Second)
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := h.owner.ResumeAll(rctx); err == nil {
		h.count("resume_all")
	}
}

func (h *harness) backup(ctx context.Context) {
	if _, err := h.direct.Backup(ctx); err != nil {
		return
	}
	h.count("backup")
	var out struct {
		Backups []struct {
			Name     string `json:"name"`
			Verified bool   `json:"verified"`
		} `json:"backups"`
	}
	if err := h.direct.Do(ctx, "list_backups", http.MethodGet, "/api/v1/backups", nil, &out); err != nil {
		return
	}
	for _, b := range out.Backups {
		if !b.Verified {
			h.violate(invBackup, "backup "+b.Name+" failed integrity_check")
		}
	}
}

type sseFleet struct {
	mu        sync.Mutex
	parent    context.Context
	ctx       context.Context
	cancel    context.CancelFunc
	open      atomic.Int64
	connects  atomic.Int64
	events    atomic.Int64
	lagged    atomic.Int64
	failures  atomic.Int64
	kicks     atomic.Int64
	topicSets [][]string
}

func newSSEFleet(ctx context.Context) *sseFleet {
	f := &sseFleet{parent: ctx, topicSets: [][]string{{"all"}, {"board"}, {"agents", "control"}, {"plans", "questions"}, {"board", "control"}, {"output"}}}
	f.ctx, f.cancel = context.WithCancel(ctx)
	return f
}

func (f *sseFleet) current() context.Context {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ctx
}

func (f *sseFleet) kick() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancel()
	f.ctx, f.cancel = context.WithCancel(f.parent)
	f.kicks.Add(1)
}

func (f *sseFleet) stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancel()
}

func (h *harness) subscriber(ctx context.Context, r *rand.Rand) {
	f := h.sse
	for ctx.Err() == nil {
		cctx, cancel := context.WithTimeout(f.current(), time.Second+time.Duration(r.Int64N(int64(20*time.Second))))
		s, err := h.owner.Events(cctx, f.topicSets[r.IntN(len(f.topicSets))]...)
		if err != nil {
			cancel()
			if ctx.Err() == nil && cctx.Err() == nil {
				f.failures.Add(1)
			}
			pause(ctx, r, 200*time.Millisecond, time.Second)
			continue
		}
		f.connects.Add(1)
		f.open.Add(1)
		for {
			ev, err := s.Next()
			if err != nil {
				break
			}
			f.events.Add(1)
			if ev.Name == "lagged" {
				f.lagged.Add(1)
			}
		}
		f.open.Add(-1)
		_ = s.Close()
		cancel()
		pause(ctx, r, 50*time.Millisecond, 500*time.Millisecond)
	}
}
