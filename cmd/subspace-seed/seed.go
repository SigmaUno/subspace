package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/model"
)

type agent struct {
	spec     agentSpec
	provider string
	mcp      *client.MCP
}

type Seeder struct {
	url    string
	owner  *client.Owner
	rng    *rand.Rand
	out    io.Writer
	agents map[string]*agent
	briefs []*model.Brief
	ids    map[string]string
	cost   map[string]float64
	brief  map[string]string
	prs    int
	leader *client.MCP
}

func NewSeeder(url, ownerToken string, seed int64, out io.Writer) *Seeder {
	return &Seeder{
		url:    url,
		owner:  client.NewOwner(url, ownerToken),
		rng:    rand.New(rand.NewPCG(uint64(seed), 0x5eed)), //nolint:gosec
		out:    out,
		agents: map[string]*agent{},
		ids:    map[string]string{},
		cost:   map[string]float64{},
		brief:  map[string]string{},
	}
}

func (s *Seeder) logf(format string, a ...any) {
	_, _ = fmt.Fprintf(s.out, format+"\n", a...)
}

func (s *Seeder) Run(ctx context.Context) error {
	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"check server", s.checkEmpty},
		{"agents", s.registerAgents},
		{"briefs", s.createBriefs},
		{"plans", s.planBriefs},
		{"follow-up tasks", s.createLate},
		{"task lifecycles", s.driveAll},
		{"questions", s.askQuestions},
		{"threads", s.chatter},
		{"artifacts", s.artifacts},
		{"usage", s.usage},
	}
	for _, st := range steps {
		if err := st.fn(ctx); err != nil {
			return fmt.Errorf("%s: %w", st.name, err)
		}
	}
	return nil
}

func (s *Seeder) checkEmpty(ctx context.Context) error {
	bs, err := s.owner.ListBriefs(ctx, "")
	if err != nil {
		return err
	}
	as, err := s.owner.ListAgents(ctx, "", "")
	if err != nil {
		return err
	}
	if len(bs) > 0 || len(as) > 0 {
		return errors.New("server already has briefs or agents; seed an empty server")
	}
	return nil
}

func (s *Seeder) registerAgents(ctx context.Context) error {
	for _, sp := range agentSpecs {
		r, err := s.owner.RegisterAgent(ctx, board.AgentInput{ID: sp.ID, Role: sp.Role, Kind: sp.Kind, Model: sp.Model})
		if err != nil {
			return fmt.Errorf("register %s: %w", sp.ID, err)
		}
		s.agents[sp.ID] = &agent{spec: sp, provider: r.Agent.Provider, mcp: client.NewMCP(s.url+"/mcp", r.Connect.Token)}
	}
	s.leader = s.agents[leaderID].mcp
	s.logf("registered %d agents", len(agentSpecs))
	return nil
}

func (s *Seeder) createBriefs(ctx context.Context) error {
	for _, sp := range briefSpecs {
		b, err := s.owner.CreateBrief(ctx, board.BriefInput{
			Title: sp.Title, Body: sp.Body, Repos: []string{sp.Repo}, MaxCostEUR: sp.MaxCost,
			MaxParallel: sp.MaxParallel, AllowedProviders: sp.Providers, CrossReview: ptr(true),
		})
		if err != nil {
			return err
		}
		if _, err := s.owner.SetComponents(ctx, b.ID, sp.Components); err != nil {
			return fmt.Errorf("components of %s: %w", b.ID, err)
		}
		s.briefs = append(s.briefs, b)
	}
	s.logf("created %d briefs", len(s.briefs))
	return nil
}

func (s *Seeder) taskInput(t *taskSpec, resolve func(string) string) board.TaskInput {
	in := board.TaskInput{
		Ref: t.Ref, BriefID: s.briefs[t.Brief].ID, Title: t.Title, Spec: t.Spec, AcceptanceCriteria: t.Criteria,
		Repo: briefSpecs[t.Brief].Repo, Role: t.Role, Priority: t.Priority,
		Budget: model.Budget{MaxMinutes: 180, MaxCostEUR: 8},
	}
	if t.Comp > 0 {
		in.ComponentID = fmt.Sprintf("C-%d", t.Comp)
	}
	for _, d := range t.Deps {
		in.DependsOn = append(in.DependsOn, resolve(d))
	}
	return in
}

func (s *Seeder) planBriefs(ctx context.Context) error {
	for i, b := range s.briefs {
		var inputs []board.TaskInput
		var specs []*taskSpec
		for j := range taskSpecs {
			t := &taskSpecs[j]
			if t.Brief == i && !t.Late {
				inputs = append(inputs, s.taskInput(t, func(r string) string { return r }))
				specs = append(specs, t)
			}
		}
		plan, err := s.leader.ProposePlan(ctx, board.PlanInput{
			BriefID: b.ID, Note: fmt.Sprintf("Initial breakdown of %q into %d tasks.", b.Title, len(inputs)), Tasks: inputs,
		})
		if err != nil {
			return fmt.Errorf("plan for %s: %w", b.ID, err)
		}
		if len(plan.Tasks) != len(specs) {
			return fmt.Errorf("plan %s has %d tasks, want %d", plan.ID, len(plan.Tasks), len(specs))
		}
		for k, t := range plan.Tasks {
			s.ids[specs[k].Ref] = t.ID
		}
		if briefSpecs[i].Approve {
			if _, err := s.owner.ApprovePlan(ctx, plan.ID, "Looks right. Go."); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Seeder) createLate(ctx context.Context) error {
	for i := range taskSpecs {
		t := &taskSpecs[i]
		if !t.Late {
			continue
		}
		created, err := s.leader.CreateTask(ctx, s.taskInput(t, func(r string) string { return s.ids[r] }))
		if err != nil {
			return err
		}
		s.ids[t.Ref] = created.ID
	}
	return nil
}

func (s *Seeder) driveAll(ctx context.Context) error {
	for i := range taskSpecs {
		t := &taskSpecs[i]
		if !briefSpecs[t.Brief].Approve {
			continue
		}
		if err := s.drive(ctx, t); err != nil {
			return fmt.Errorf("%s %q: %w", s.ids[t.Ref], t.Title, err)
		}
	}
	s.logf("drove tasks through their lifecycle")
	return nil
}

func (s *Seeder) reviewerFor(t *taskSpec) *agent {
	provider := s.agents[t.Worker].provider
	allowed := briefSpecs[t.Brief].Providers
	var eligible []*agent
	for _, sp := range agentSpecs {
		a := s.agents[sp.ID]
		if sp.Role == model.RoleReviewer && a.provider != provider && (len(allowed) == 0 || contains(allowed, a.provider)) {
			eligible = append(eligible, a)
		}
	}
	return pick(s.rng, eligible)
}

func (s *Seeder) drive(ctx context.Context, t *taskSpec) error {
	id := s.ids[t.Ref]
	switch t.Target {
	case model.StatusReady:
		return nil
	case model.StatusCancelled:
		_, err := s.owner.CancelTask(ctx, id, "Out of scope for this brief. Revisit if a customer asks for it.")
		return err
	}
	w := s.agents[t.Worker]
	rev := s.reviewerFor(t)
	s.brief[w.spec.ID] = s.briefs[t.Brief].ID
	s.brief[rev.spec.ID] = s.briefs[t.Brief].ID
	if _, err := s.leader.Assign(ctx, id, w.spec.ID, ""); err != nil {
		return err
	}
	switch t.Target {
	case model.StatusAssigned:
		return nil
	case model.StatusInProgress:
		return s.work(ctx, t, id, w, 25+s.rng.IntN(55))
	case model.StatusBlocked:
		if err := s.work(ctx, t, id, w, 20+s.rng.IntN(30)); err != nil {
			return err
		}
		_, err := w.mcp.UpdateStatus(ctx, board.StatusInput{TaskID: id, Status: model.StatusBlocked, Note: t.Block})
		return err
	}
	pending := t.Target == model.StatusSubmitted || t.Target == model.StatusInReview
	cycles := len(t.Reviews)
	if pending {
		cycles++
	}
	for i := 0; i < cycles; i++ {
		if err := s.work(ctx, t, id, w, 40+s.rng.IntN(40)); err != nil {
			return err
		}
		if t.Role == model.RoleTester {
			failed := 0
			if i < len(t.Reviews) && t.Reviews[i] == changes {
				failed = 1 + s.rng.IntN(3)
			}
			if _, err := w.mcp.ReportTests(ctx, id, 24+s.rng.IntN(40), failed, ""); err != nil {
				return err
			}
		}
		if err := s.submit(ctx, t, id, w, i); err != nil {
			return err
		}
		if i < len(t.Reviews) {
			if _, err := rev.mcp.Review(ctx, id, t.Reviews[i], s.reviewText(t.Reviews[i], i)); err != nil {
				return err
			}
		}
	}
	switch t.Target {
	case model.StatusInReview:
		_, err := s.leader.Assign(ctx, id, rev.spec.ID, "reviewer")
		return err
	case model.StatusDone:
		_, err := s.owner.MarkDone(ctx, id, "Merged. Thanks.")
		return err
	}
	return nil
}

func (s *Seeder) work(ctx context.Context, t *taskSpec, id string, w *agent, pct int) error {
	if _, err := w.mcp.ClaimTask(ctx, id); err != nil {
		return err
	}
	steps := 2 + s.rng.IntN(2)
	for k := 1; k <= steps; k++ {
		p := pct * k / steps
		s.cost[id] += 0.2 + s.rng.Float64()*0.9
		note := pick(s.rng, progressNotes)
		if _, err := w.mcp.UpdateStatus(ctx, board.StatusInput{TaskID: id, Status: model.StatusInProgress, ProgressPct: ptr(p), CostEUR: ptr(round2(s.cost[id])), Note: note}); err != nil {
			return err
		}
		if _, err := w.mcp.Heartbeat(ctx, id); err != nil {
			return err
		}
	}
	if s.rng.IntN(3) > 0 {
		if _, err := w.mcp.AddNote(ctx, id, fmt.Sprintf(pick(s.rng, workNotes), t.Title)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Seeder) submit(ctx context.Context, t *taskSpec, id string, w *agent, cycle int) error {
	in := board.SubmitInput{TaskID: id, Summary: fmt.Sprintf("Round %d: %s", cycle+1, pick(s.rng, summaries))}
	if t.Role != model.RoleTester {
		s.prs++
		in.PRURL = fmt.Sprintf("https://github.com/%s/pull/%d", briefSpecs[t.Brief].Repo, 100+s.prs)
	}
	_, err := w.mcp.Submit(ctx, in)
	return err
}

func (s *Seeder) reviewText(verdict string, round int) string {
	if verdict == approve {
		return pick(s.rng, approvals)
	}
	return fmt.Sprintf("%s (round %d)", pick(s.rng, objections), round+1)
}

func (s *Seeder) askQuestions(ctx context.Context) error {
	for i := range taskSpecs {
		t := &taskSpecs[i]
		if t.Question == nil {
			continue
		}
		q, err := s.agents[t.Worker].mcp.AskOwner(ctx, s.ids[t.Ref], t.Question.Text, t.Question.Options...)
		if err != nil {
			return err
		}
		if t.Question.Answer != "" {
			if _, err := s.owner.AnswerQuestion(ctx, q.ID, t.Question.Answer); err != nil {
				return err
			}
		}
	}
	_, err := s.leader.AskOwner(ctx, "", "The inference gateway plan is waiting for approval. Any provider we should avoid for the router work?", "none", "avoid deepseek", "avoid openai")
	return err
}

func (s *Seeder) chatter(ctx context.Context) error {
	for i, b := range s.briefs {
		if _, err := s.leader.PostMessage(ctx, "brief:"+b.ID, fmt.Sprintf(pick(s.rng, briefUpdates), b.Title)); err != nil {
			return err
		}
		if _, err := s.owner.PostThreadMessage(ctx, model.ThreadBrief, b.ID, ownerBriefReplies[i]); err != nil {
			return err
		}
	}
	for _, r := range ownerTaskReplies {
		if _, err := s.owner.PostThreadMessage(ctx, model.ThreadTask, s.ids[r.Ref], r.Text); err != nil {
			return err
		}
	}
	for _, id := range []string{"claude-1", "codex-1", "oc-1"} {
		if _, err := s.leader.PostMessage(ctx, "agent:"+id, pick(s.rng, leaderNudges)); err != nil {
			return err
		}
	}
	_, err := s.owner.SendMessage(ctx, "leader", "Please finish the webhook work before starting new dashboard tasks.")
	return err
}

func (s *Seeder) artifacts(ctx context.Context) error {
	for _, a := range artifactSpecs {
		scope := ""
		if a.Scope == "project" {
			scope = "project:" + s.briefs[int(a.Ref[0]-'0')].ID
		} else {
			scope = "task:" + s.ids[a.Ref]
		}
		if _, err := s.agents[a.By].mcp.PutArtifact(ctx, scope, a.Name, a.Type, s.artifactBody(a)); err != nil {
			return fmt.Errorf("artifact %s: %w", a.Name, err)
		}
	}
	return nil
}

func (s *Seeder) artifactBody(a artifactSpec) []byte {
	var b strings.Builder
	switch a.Type {
	case "application/json":
		fmt.Fprintf(&b, `{"name":%q,"runs":%d,"p50_ms":%d,"p95_ms":%d}`+"\n", a.Name, 10+s.rng.IntN(20), 200+s.rng.IntN(400), 800+s.rng.IntN(900))
	case "text/markdown":
		fmt.Fprintf(&b, "# %s\n\nGenerated for the demo workspace.\n\n- %s\n- %s\n- %s\n", a.Name, pick(s.rng, progressNotes), pick(s.rng, progressNotes), pick(s.rng, progressNotes))
	default:
		for i := 0; i < 12; i++ {
			fmt.Fprintf(&b, "ok  \tpkg/part%02d\t0.%03ds\n", i, 20+s.rng.IntN(900))
		}
	}
	return []byte(b.String())
}

func (s *Seeder) usage(ctx context.Context) error {
	for _, sp := range agentSpecs {
		a := s.agents[sp.ID]
		for k := 0; k < 3+s.rng.IntN(4); k++ {
			rep := client.UsageReport{
				Provider: a.provider, Model: sp.Model, BriefID: s.brief[sp.ID],
				TokensIn: int64(2000 + s.rng.IntN(40000)), TokensOut: int64(500 + s.rng.IntN(9000)),
				CacheRead: int64(s.rng.IntN(60000)), RequestID: fmt.Sprintf("seed-%s-%d", sp.ID, k),
			}
			if _, err := a.mcp.ReportUsage(ctx, rep); err != nil {
				s.logf("usage reporting is not available on this server (%v); skipped", err)
				return nil
			}
		}
	}
	return nil
}
