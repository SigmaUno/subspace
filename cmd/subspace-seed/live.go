package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
)

type Sim struct {
	s     *Seeder
	rng   *rand.Rand
	extra int
}

func NewSim(s *Seeder, seed int64) *Sim {
	return &Sim{s: s, rng: rand.New(rand.NewPCG(uint64(seed), 0x11fe))} //nolint:gosec
}

func (m *Sim) Run(ctx context.Context, every time.Duration) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := m.Tick(ctx); err != nil && ctx.Err() == nil {
				m.s.logf("simulator: %v", err)
			}
		}
	}
}

type world struct {
	tasks   map[string]*model.Task
	briefs  map[string]*model.Brief
	busy    map[string]bool
	load    map[string]int
	waiting int
}

func (m *Sim) look(ctx context.Context) (*world, []*model.Task, error) {
	tasks, err := m.s.owner.ListTasks(ctx, board.TaskQuery{Limit: 1000})
	if err != nil {
		return nil, nil, err
	}
	briefs, err := m.s.owner.ListBriefs(ctx, "open")
	if err != nil {
		return nil, nil, err
	}
	w := &world{tasks: map[string]*model.Task{}, briefs: map[string]*model.Brief{}, busy: map[string]bool{}, load: map[string]int{}}
	for _, b := range briefs {
		w.briefs[b.ID] = b
	}
	for _, t := range tasks {
		w.tasks[t.ID] = t
		who := deref(t.Assignee)
		switch t.Status {
		case model.StatusInProgress:
			w.busy[who] = true
			w.load[who]++
		case model.StatusAssigned, model.StatusChangesRequested:
			w.load[who]++
		}
		if t.Status == model.StatusReady || t.Status == model.StatusAssigned {
			w.waiting++
		}
	}
	return w, tasks, nil
}

func (m *Sim) Tick(ctx context.Context) error {
	w, tasks, err := m.look(ctx)
	if err != nil {
		return err
	}
	var acts []func(context.Context) error
	for _, t := range tasks {
		a := m.s.agents[deref(t.Assignee)]
		if _, open := w.briefs[t.BriefID]; !open {
			continue
		}
		switch t.Status {
		case model.StatusReady:
			acts = append(acts, func(ctx context.Context) error { return m.assign(ctx, w, t) })
		case model.StatusAssigned, model.StatusChangesRequested:
			if a != nil && !w.busy[a.spec.ID] && depsDone(t, w.tasks) {
				acts = append(acts, func(ctx context.Context) error { _, err := a.mcp.ClaimTask(ctx, t.ID); return err })
			}
		case model.StatusInProgress:
			if a != nil {
				if _, err := a.mcp.Heartbeat(ctx, t.ID); err != nil {
					m.s.logf("simulator: heartbeat %s: %v", t.ID, err)
				}
				acts = append(acts, func(ctx context.Context) error { return m.progress(ctx, a, t) })
			}
		case model.StatusSubmitted:
			acts = append(acts, func(ctx context.Context) error { return m.assignReviewer(ctx, w, t) })
		case model.StatusInReview:
			if r := m.s.agents[deref(t.Reviewer)]; r != nil {
				acts = append(acts, func(ctx context.Context) error { return m.review(ctx, r, t) })
			}
		}
	}
	if w.waiting < 3 {
		acts = append(acts, m.followUp)
	}
	m.rng.Shuffle(len(acts), func(i, j int) { acts[i], acts[j] = acts[j], acts[i] })
	for _, act := range acts[:min(1+m.rng.IntN(2), len(acts))] {
		if err := act(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			m.s.logf("simulator: %v", err)
		}
	}
	return nil
}

func depsDone(t *model.Task, all map[string]*model.Task) bool {
	for _, d := range t.DependsOn {
		if dt := all[d]; dt == nil || dt.Status != model.StatusDone {
			return false
		}
	}
	return true
}

func (m *Sim) assign(ctx context.Context, w *world, t *model.Task) error {
	var best *agent
	for _, sp := range agentSpecs {
		a := m.s.agents[sp.ID]
		if sp.Role != t.Role || !providerAllowed(w.briefs[t.BriefID], a.provider) {
			continue
		}
		if best == nil || w.load[sp.ID] < w.load[best.spec.ID] {
			best = a
		}
	}
	if best == nil {
		return nil
	}
	w.load[best.spec.ID]++
	_, err := m.s.leader.Assign(ctx, t.ID, best.spec.ID, "")
	return err
}

func (m *Sim) assignReviewer(ctx context.Context, w *world, t *model.Task) error {
	worker := m.s.agents[deref(t.Assignee)]
	if worker == nil {
		return nil
	}
	var eligible []*agent
	for _, sp := range agentSpecs {
		a := m.s.agents[sp.ID]
		if sp.Role == model.RoleReviewer && a.provider != worker.provider && providerAllowed(w.briefs[t.BriefID], a.provider) {
			eligible = append(eligible, a)
		}
	}
	if len(eligible) == 0 {
		return nil
	}
	_, err := m.s.leader.Assign(ctx, t.ID, pick(m.rng, eligible).spec.ID, "reviewer")
	return err
}

func (m *Sim) review(ctx context.Context, r *agent, t *model.Task) error {
	if t.ReviewRounds < 2 && m.rng.IntN(4) == 0 {
		_, err := r.mcp.Review(ctx, t.ID, changes, fmt.Sprintf("%s (round %d)", pick(m.rng, objections), t.ReviewRounds+1))
		return err
	}
	_, err := r.mcp.Review(ctx, t.ID, approve, pick(m.rng, approvals))
	return err
}

func (m *Sim) progress(ctx context.Context, a *agent, t *model.Task) error {
	pct := t.ProgressPct + 8 + m.rng.IntN(15)
	if pct >= 95 {
		return m.finish(ctx, a, t)
	}
	cost := t.CostUsed + 0.05 + m.rng.Float64()*0.25
	if t.Budget.MaxCostEUR > 0 {
		cost = min(cost, t.Budget.MaxCostEUR*0.9)
	}
	in := board.StatusInput{TaskID: t.ID, Status: model.StatusInProgress, ProgressPct: ptr(pct), CostEUR: ptr(round2(cost))}
	if m.rng.IntN(3) == 0 {
		in.Note = pick(m.rng, progressNotes)
	}
	_, err := a.mcp.UpdateStatus(ctx, in)
	return err
}

func (m *Sim) finish(ctx context.Context, a *agent, t *model.Task) error {
	in := board.SubmitInput{TaskID: t.ID, Summary: pick(m.rng, summaries)}
	if t.Role == model.RoleTester {
		if _, err := a.mcp.ReportTests(ctx, t.ID, 20+m.rng.IntN(40), 0, ""); err != nil {
			return err
		}
	} else {
		m.s.prs++
		in.PRURL = fmt.Sprintf("https://github.com/%s/pull/%d", t.Repo, 100+m.s.prs)
	}
	_, err := a.mcp.Submit(ctx, in)
	return err
}

func (m *Sim) followUp(ctx context.Context) error {
	f := followUps[m.extra%len(followUps)]
	if round := m.extra / len(followUps); round > 0 {
		f.Title = fmt.Sprintf("%s (follow-up %d)", f.Title, round+1)
	}
	m.extra++
	_, err := m.s.leader.CreateTask(ctx, m.s.taskInput(&f, func(string) string { return "" }))
	return err
}

func providerAllowed(b *model.Brief, provider string) bool {
	return b != nil && (len(b.AllowedProviders) == 0 || slices.Contains(b.AllowedProviders, provider))
}
