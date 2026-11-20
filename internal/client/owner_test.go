package client_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/components"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

func TestOwnerControlAndAgents(t *testing.T) {
	defer guard(t)
	e := newEnv(t)
	ctx, o := e.ctx, e.owner

	if h := must(o.Health(ctx)); h.Status != "ok" || h.Paused {
		t.Fatalf("health %+v", h)
	}
	if s := must(o.State(ctx)); s.Paused {
		t.Fatalf("state %+v", s)
	}
	if err := o.PauseAll(ctx, "drill"); err != nil {
		t.Fatal(err)
	}
	if s := must(o.State(ctx)); !s.Paused || s.PauseReason == nil || *s.PauseReason != "drill" {
		t.Fatalf("paused state %+v", s)
	}
	if h := must(o.Health(ctx)); !h.Paused {
		t.Fatalf("health while paused %+v", h)
	}
	if err := o.ResumeAll(ctx); err != nil {
		t.Fatal(err)
	}

	if c := must(o.SetCharter(ctx, "be kind")); c.Text != "be kind" {
		t.Fatalf("charter %+v", c)
	}
	if c := must(o.Charter(ctx)); c.Text != "be kind" {
		t.Fatalf("charter %+v", c)
	}
	bk := must(o.Backup(ctx))
	if st, err := os.Stat(bk.Path); err != nil || st.Size() != bk.Bytes {
		t.Fatalf("backup %+v: %v", bk, err)
	}

	reg := must(o.RegisterAgent(ctx, board.AgentInput{ID: "wally", Role: model.RoleWorker, Kind: model.KindClaudeCode, Model: "m"}))
	if reg.Agent.Provider != "anthropic" || !strings.HasPrefix(reg.Connect.Token, "ssp_") {
		t.Fatalf("registered %+v", reg)
	}
	bulk := must(o.RegisterBulk(ctx, board.BulkInput{Prefix: "bee", Count: 3, Role: model.RoleWorker, Kind: model.KindCodex, Model: "m"}))
	if len(bulk) != 3 || bulk[0].Agent.ID != "bee-001" || bulk[2].Connect.Token == "" {
		t.Fatalf("bulk %+v", bulk)
	}
	if as := must(o.ListAgents(ctx, model.RoleWorker, "")); len(as) != 4 {
		t.Fatalf("agents %d", len(as))
	}
	if a := must(o.GetAgent(ctx, "wally")); a.Kind != model.KindClaudeCode {
		t.Fatalf("agent %+v", a)
	}
	conn := must(o.Connect(ctx, "wally"))
	if conn.Token != "" || !strings.Contains(conn.JoinLine, "as wally") || conn.Snippets["claude_code"] == "" {
		t.Fatalf("connect %+v", conn)
	}

	old := e.mcp(reg.Connect.Token)
	rot := must(o.RotateToken(ctx, "wally"))
	if rot.Connect.Token == "" || rot.Connect.Token == reg.Connect.Token {
		t.Fatalf("rotate %+v", rot)
	}
	e.mcp(rot.Connect.Token)
	for range 100 {
		if _, err := old.WhoAmI(ctx); client.IsCode(err, client.CodeUnauthorized) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := old.WhoAmI(ctx); !client.IsCode(err, client.CodeUnauthorized) {
		t.Fatalf("old token still works: %v", err)
	}
	if a := must(o.RevokeAgent(ctx, "bee-001", "retired")); a.Status != model.AgentRevoked {
		t.Fatalf("revoke %+v", a)
	}
	if as := must(o.ListAgents(ctx, "", model.AgentRevoked)); len(as) != 1 || as[0].ID != "bee-001" {
		t.Fatalf("revoked %+v", as)
	}
	if a := must(o.ResumeAgent(ctx, "bee-002")); a.Status != model.AgentActive {
		t.Fatalf("resume %+v", a)
	}

	cap := must(o.SetCapacity(ctx, capacity.Config{GlobalMaxAgents: 7, Limits: []capacity.Limit{{Provider: "anthropic", Model: "*", MaxConcurrency: 3}}}))
	if cap.GlobalMaxAgents != 7 {
		t.Fatalf("capacity %+v", cap)
	}
	if got := must(o.Capacity(ctx)); got.GlobalMaxAgents != 7 || len(got.Limits) == 0 {
		t.Fatalf("capacity %+v", got)
	}
}

func TestOwnerBoardFlow(t *testing.T) {
	defer guard(t)
	e := newEnv(t)
	ctx, o := e.ctx, e.owner
	tm := e.team()
	br := e.brief()

	if bs := must(o.ListBriefs(ctx, "open")); len(bs) != 1 {
		t.Fatalf("briefs %+v", bs)
	}
	if b := must(o.GetBrief(ctx, br.ID)); b.ID != br.ID || !b.CrossReview {
		t.Fatalf("brief %+v", b)
	}
	maxPar := 9
	if b := must(o.UpdateBrief(ctx, br.ID, board.BriefPatch{MaxParallel: &maxPar})); b.Budget.MaxParallelAgent != 9 {
		t.Fatalf("patched brief %+v", b)
	}

	p1 := must(tm.leader.ProposePlan(ctx, board.PlanInput{BriefID: br.ID, Tasks: []board.TaskInput{taskInput(br.ID, "a"), taskInput(br.ID, "b")}}))
	if ps := must(o.ListPlans(ctx, br.ID, model.PlanProposed)); len(ps) != 1 || ps[0].ID != p1.ID {
		t.Fatalf("plans %+v", ps)
	}
	if p := must(o.GetPlan(ctx, p1.ID)); len(p.Tasks) != 2 {
		t.Fatalf("plan %+v", p)
	}
	if rj := must(o.RejectPlan(ctx, p1.ID, "too vague")); rj.Status != model.PlanRejected {
		t.Fatalf("reject %+v", rj)
	}
	p2 := must(tm.leader.ProposePlan(ctx, board.PlanInput{BriefID: br.ID, Tasks: []board.TaskInput{taskInput(br.ID, "c"), taskInput(br.ID, "d")}}))
	if ap := must(o.ApprovePlan(ctx, p2.ID, "ok")); ap.Status != model.PlanApproved {
		t.Fatalf("approve %+v", ap)
	}

	tasks := must(o.ListTasks(ctx, board.TaskQuery{BriefID: br.ID, Status: model.StatusReady}))
	if len(tasks) != 2 {
		t.Fatalf("ready tasks %+v", tasks)
	}
	c, d := tasks[0].ID, tasks[1].ID
	if tk := must(o.GetTask(ctx, c)); tk.ID != c {
		t.Fatalf("task %+v", tk)
	}
	if tk := must(o.SetTaskBudget(ctx, c, model.Budget{MaxMinutes: 45, MaxCostEUR: 3})); tk.Budget.MaxMinutes != 45 {
		t.Fatalf("budget %+v", tk)
	}
	_ = must(tm.leader.Assign(ctx, c, "wally", ""))
	if as := must(o.ListTasks(ctx, board.TaskQuery{Assignee: "wally"})); len(as) != 1 || as[0].ID != c {
		t.Fatalf("assignee filter %+v", as)
	}
	if tk := must(o.CancelTask(ctx, d, "dropped")); tk.Status != model.StatusCancelled {
		t.Fatalf("cancel %+v", tk)
	}
	if tk := must(o.ReopenTask(ctx, d, model.StatusReady, "needed after all")); tk.Status != model.StatusReady {
		t.Fatalf("reopen %+v", tk)
	}
	bv := must(o.Board(ctx, br.ID))
	if bv.Brief == nil || len(bv.Columns[model.StatusAssigned]) != 1 || len(bv.Columns[model.StatusReady]) != 1 {
		t.Fatalf("board %+v", bv)
	}

	_ = must(tm.worker.ClaimTask(ctx, c))
	q := must(tm.worker.AskOwner(ctx, c, "tabs or spaces?", "tabs", "spaces"))
	if qs := must(o.ListQuestions(ctx, "open")); len(qs) != 1 || qs[0].ID != q.ID {
		t.Fatalf("questions %+v", qs)
	}
	if a := must(o.AnswerQuestion(ctx, q.ID, "tabs")); a.Status != "answered" || a.Answer == nil || *a.Answer != "tabs" {
		t.Fatalf("answer %+v", a)
	}

	m := must(o.PostThreadMessage(ctx, model.ThreadTask, c, "owner says hi"))
	if m.AuthorRole != model.RoleOwner || m.ThreadID != c {
		t.Fatalf("thread post %+v", m)
	}
	msgs := must(o.ThreadMessages(ctx, model.ThreadTask, c, 0, 0))
	if !slices.ContainsFunc(msgs, func(x *model.Message) bool { return x.ID == m.ID }) {
		t.Fatalf("thread %+v", msgs)
	}
	if after := must(o.ThreadMessages(ctx, model.ThreadTask, c, m.ID, 5)); len(after) != 0 {
		t.Fatalf("since %+v", after)
	}
	if mine := must(tm.worker.ReadThread(ctx, "task:"+c, m.ID-1, 5)); len(mine) != 1 || mine[0].Body != "owner says hi" {
		t.Fatalf("agent view %+v", mine)
	}

	_ = must(tm.worker.Submit(ctx, board.SubmitInput{TaskID: c, PRURL: "https://example.invalid/pr/2", Summary: "s"}))
	_ = must(tm.reviewer.Review(ctx, c, "approve", "ok"))
	if tk := must(o.MarkDone(ctx, c, "merged")); tk.Status != model.StatusDone {
		t.Fatalf("done %+v", tk)
	}

	if sp := must(tm.leader.RequestSpawn(ctx, board.SpawnInput{BriefID: br.ID, Kind: model.KindOpenCode, Model: "m", N: 2})); len(sp) != 2 {
		t.Fatalf("spawn %+v", sp)
	}
	if sps := must(o.ListSpawns(ctx, br.ID)); len(sps) != 2 || sps[0].Provider != "deepseek" {
		t.Fatalf("spawns %+v", sps)
	}

	evs := must(o.Audit(ctx, store.AuditFilter{TaskID: c}))
	var tools []string
	for _, ev := range evs {
		tools = append(tools, ev.Tool)
	}
	for _, want := range []string{"claim_task", "submit", "review"} {
		if !slices.Contains(tools, want) {
			t.Errorf("audit for %s lacks %s: %v", c, want, tools)
		}
	}
	if one := must(o.Audit(ctx, store.AuditFilter{Tool: "claim_task", Limit: 1})); len(one) != 1 || one[0].Tool != "claim_task" {
		t.Fatalf("audit filter %+v", one)
	}
	if after := must(o.Audit(ctx, store.AuditFilter{SinceID: evs[len(evs)-1].ID, TaskID: c})); len(after) != 0 {
		t.Fatalf("audit since %+v", after)
	}
}

func TestOwnerSwarmExtras(t *testing.T) {
	defer guard(t)
	e := newEnv(t)
	ctx, o := e.ctx, e.owner
	tm := e.team()
	br := e.brief()

	p := must(tm.leader.ProposePlan(ctx, board.PlanInput{BriefID: br.ID, Tasks: []board.TaskInput{taskInput(br.ID, "a")}}))
	ds := must(o.Decisions(ctx, 0))
	if len(ds) == 0 {
		t.Fatal("no decisions for a proposed plan")
	}
	next := must(o.NextDecision(ctx))
	if next == nil || next.ID != ds[0].ID {
		t.Fatalf("next %+v vs %+v", next, ds[0])
	}
	if limited := must(o.Decisions(ctx, 1)); len(limited) != 1 {
		t.Fatalf("limit %+v", limited)
	}
	out := must(o.ActOnDecision(ctx, next.ID, client.DecisionAction{Action: "approve", Note: "go"}))
	if out.Action != "approve" {
		t.Fatalf("outcome %+v", out)
	}
	if pl := must(o.GetPlan(ctx, p.ID)); pl.Status != model.PlanApproved {
		t.Fatalf("plan after decision %+v", pl)
	}
	if n := must(o.NextDecision(ctx)); n != nil {
		t.Fatalf("decision left: %+v", n)
	}

	specs := []components.Spec{
		{Name: "core", Description: "the core", Paths: []string{"core/"}},
		{Name: "api", Paths: []string{"api/"}, DependsOn: []string{"core"}},
	}
	comps := must(o.SetComponents(ctx, br.ID, specs))
	if len(comps) != 2 || comps[0].ID != "C-1" {
		t.Fatalf("components %+v", comps)
	}
	g := must(o.Architecture(ctx, br.ID))
	if len(g.Nodes) != 2 || len(g.Edges) != 1 || g.Edges[0].From != "C-2" {
		t.Fatalf("graph %+v", g)
	}

	conv := must(o.SendMessage(ctx, "agent:wally", "please hurry"))
	if conv.ID == "" || !slices.Equal(conv.Recipients, []string{"wally"}) {
		t.Fatalf("conversation %+v", conv)
	}
	got := must(o.GetConversation(ctx, conv.ID))
	if len(got.Messages) != 1 || got.Messages[0].Body != "please hurry" {
		t.Fatalf("conversation %+v", got)
	}
	if cs := must(o.Conversations(ctx)); len(cs) != 1 || cs[0].ID != conv.ID {
		t.Fatalf("conversations %+v", cs)
	}

	ps := must(o.Prompts(ctx, ""))
	if len(ps) == 0 {
		t.Fatal("no prompts")
	}
	one := must(o.GetPrompt(ctx, ps[0].ID))
	if one.ID != ps[0].ID || one.Body == "" {
		t.Fatalf("prompt %+v", one)
	}
	vars := map[string]string{}
	for _, v := range one.Variables {
		vars[v] = "X"
	}
	if text := must(o.RenderPrompt(ctx, one.ID, vars)); text == "" || strings.Contains(text, "{{") {
		t.Fatalf("render %q", text)
	}

	target := 0.5
	job := must(o.CreateHillclimbJob(ctx, client.HillclimbJobInput{BriefID: br.ID, Repo: repo, MetricCmd: "make bench", Direction: "min", Target: &target, Budget: 5}))
	if job.ID == "" || job.Direction != "min" {
		t.Fatalf("job %+v", job)
	}
	if jobs := must(o.HillclimbJobs(ctx)); len(jobs) != 1 || jobs[0].ID != job.ID {
		t.Fatalf("jobs %+v", jobs)
	}
	if s := must(o.HillclimbSeries(ctx, job.ID)); s.Job.ID != job.ID || len(s.Points) != 0 {
		t.Fatalf("series %+v", s)
	}
}

func TestOwnerEvents(t *testing.T) {
	defer guard(t)
	e := newEnv(t)
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()

	s := must(e.owner.Events(ctx, "board", "control"))
	defer func() { _ = s.Close() }()
	hello := must(s.Next())
	if hello.Name != "hello" {
		t.Fatalf("first event %+v", hello)
	}

	if err := e.owner.PauseAll(ctx, "events"); err != nil {
		t.Fatal(err)
	}
	for {
		ev := must(s.Next())
		if ev.Name != "control.paused" {
			continue
		}
		env := must(ev.Envelope())
		if env.Type != "control.paused" || env.TS.IsZero() || !strings.Contains(string(env.Data), "events") {
			t.Fatalf("envelope %+v", env)
		}
		break
	}

	_, err := client.NewOwner(e.url, "wrong-owner-token-wrong-owner-token").Events(ctx)
	if !client.IsCode(err, client.CodeUnauthorized) {
		t.Fatalf("events with bad token: %v", err)
	}
}
