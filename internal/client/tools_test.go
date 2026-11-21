package client_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/client"
	"github.com/SigmaUno/subspace/internal/helpreq"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/prompts"
	"github.com/SigmaUno/subspace/internal/usage"
)

func TestToolMethodsAgainstServer(t *testing.T) {
	defer guard(t)
	e := newEnv(t)
	ctx := e.ctx
	tm := e.team()
	br := e.brief()

	got := must(tm.worker.GetInstructions(ctx))
	if got.AgentID != "wally" || got.Role != model.RoleWorker || !strings.Contains(got.Instructions, "Owner charter") {
		t.Fatalf("instructions %+v", got)
	}
	me := must(tm.worker.WhoAmI(ctx))
	if me.Agent.ID != "wally" || me.Paused || me.ServerTime.IsZero() {
		t.Fatalf("whoami %+v", me)
	}

	briefs := must(tm.leader.ListBriefs(ctx, "open"))
	if len(briefs) != 1 || briefs[0].ID != br.ID {
		t.Fatalf("briefs %+v", briefs)
	}
	if b := must(tm.leader.GetBrief(ctx, br.ID)); b.Title != "client brief" {
		t.Fatalf("brief %+v", b)
	}

	plan := must(tm.leader.ProposePlan(ctx, board.PlanInput{BriefID: br.ID, Note: "split", Tasks: []board.TaskInput{
		{Ref: "impl", Title: "impl", Spec: "implement", Repo: repo, AcceptanceCriteria: []string{"compiles"}},
		{Ref: "tests", Title: "tests", Spec: "test it", Repo: repo, Role: model.RoleTester},
	}}))
	if len(plan.Tasks) != 2 || plan.Status != model.PlanProposed {
		t.Fatalf("plan %+v", plan)
	}
	_ = must(e.owner.ApprovePlan(ctx, plan.ID, "go"))
	impl, tests := plan.Tasks[0].ID, plan.Tasks[1].ID

	extra := must(tm.leader.CreateTask(ctx, taskInput(br.ID, "extra")))
	if extra.Status != model.StatusReady {
		t.Fatalf("extra status %s", extra.Status)
	}
	upd := "extra, refined"
	patched := must(tm.leader.UpdateTask(ctx, extra.ID, board.TaskPatch{Spec: &upd, Budget: &model.Budget{MaxMinutes: 30, MaxCostEUR: 2}}))
	if patched.Spec != upd || patched.Budget.MaxMinutes != 30 {
		t.Fatalf("patched %+v", patched)
	}
	if a := must(tm.leader.Assign(ctx, impl, "wally", "")); a.Assignee == nil || *a.Assignee != "wally" {
		t.Fatalf("assign %+v", a)
	}
	_ = must(tm.leader.Assign(ctx, tests, "tess", ""))
	_ = must(tm.leader.Assign(ctx, extra.ID, "wally", ""))
	e.agent("walt", model.RoleWorker, model.KindClaudeCode)
	re := must(tm.leader.Reassign(ctx, extra.ID, "walt", "better fit"))
	if *re.Assignee != "walt" {
		t.Fatalf("reassign %+v", re)
	}

	agents := must(tm.leader.ListAgents(ctx, model.RoleWorker, model.AgentActive))
	if len(agents) != 2 {
		t.Fatalf("agents %+v", agents)
	}
	if _, err := tm.leader.Capacity(ctx); err != nil {
		t.Fatal(err)
	}
	cols := must(tm.leader.Board(ctx, br.ID))
	if len(cols[model.StatusAssigned]) != 3 {
		t.Fatalf("board %v", cols)
	}
	spawns := must(tm.leader.RequestSpawn(ctx, board.SpawnInput{BriefID: br.ID, Kind: model.KindClaudeCode, Model: "m", N: 1}))
	if len(spawns) != 1 || spawns[0].BriefID != br.ID {
		t.Fatalf("spawns %+v", spawns)
	}

	mine := must(tm.worker.ListMyTasks(ctx, model.StatusAssigned))
	if len(mine) != 1 || mine[0].ID != impl {
		t.Fatalf("mine %+v", mine)
	}
	if tk := must(tm.worker.GetTask(ctx, impl)); tk.Title != "impl" {
		t.Fatalf("task %+v", tk)
	}
	claimed := must(tm.worker.ClaimTask(ctx, impl))
	if claimed.Status != model.StatusInProgress || claimed.LeaseExpiresAt == nil {
		t.Fatalf("claimed %+v", claimed)
	}
	hb := must(tm.worker.Heartbeat(ctx, impl))
	if !hb.OK || hb.Paused {
		t.Fatalf("heartbeat %+v", hb)
	}
	if hb := must(tm.worker.Heartbeat(ctx, "")); !hb.OK {
		t.Fatalf("heartbeat all %+v", hb)
	}
	pct, cost := 40, 0.5
	st := must(tm.worker.UpdateStatus(ctx, board.StatusInput{TaskID: impl, Status: model.StatusInProgress, ProgressPct: &pct, Note: "half", CostEUR: &cost}))
	if st.ProgressPct != 40 {
		t.Fatalf("status %+v", st)
	}
	note := must(tm.worker.AddNote(ctx, impl, "decided on X"))
	if note.Body != "decided on X" || note.Kind != model.MsgNote {
		t.Fatalf("note %+v", note)
	}
	if n := must(tm.worker.AddNote(ctx, "", "own thread")); n.ThreadKind != model.ThreadAgent {
		t.Fatalf("own note %+v", n)
	}
	q := must(tm.worker.AskOwner(ctx, impl, "which db?", "sqlite", "postgres"))
	if q.Status != "open" || !slices.Equal(q.Options, []string{"sqlite", "postgres"}) {
		t.Fatalf("question %+v", q)
	}
	_ = must(e.owner.AnswerQuestion(ctx, q.ID, "sqlite"))
	posted := must(tm.worker.PostMessage(ctx, "task:"+impl, "hello thread"))
	if posted.ThreadID != impl {
		t.Fatalf("posted %+v", posted)
	}
	msgs := must(tm.worker.ReadThread(ctx, "task:"+impl, 0, 0))
	var sawNote, sawAnswer, sawPost bool
	for _, m := range msgs {
		sawNote = sawNote || m.Body == "decided on X"
		sawAnswer = sawAnswer || m.Kind == model.MsgAnswer
		sawPost = sawPost || m.Body == "hello thread"
	}
	if !sawNote || !sawPost || !sawAnswer {
		t.Fatalf("thread %+v", msgs)
	}
	later := must(tm.worker.ReadThread(ctx, "task:"+impl, posted.ID, 10))
	if len(later) != 0 {
		t.Fatalf("since %d returned %+v", posted.ID, later)
	}
	rel := must(tm.worker.ReleaseTask(ctx, impl, "swap"))
	if rel.Status != model.StatusAssigned {
		t.Fatalf("released %+v", rel)
	}
	_ = must(tm.worker.ClaimTask(ctx, impl))
	if h := must(tm.worker.RequestHelp(ctx, impl, "unclear_spec", "which db?")); h.Type != helpreq.TypeHelp || h.Status != helpreq.StatusOpen {
		t.Fatalf("help %+v", h)
	}
	if b := must(tm.worker.ReportBlocker(ctx, impl, "waiting for access", helpreq.NeedsLeader)); b.Needs != helpreq.NeedsLeader {
		t.Fatalf("blocker %+v", b)
	}
	if bt := must(tm.leader.GetTask(ctx, impl)); bt.Status != model.StatusBlocked {
		t.Fatalf("blocked task %+v", bt)
	}
	unblock := model.StatusReady
	_ = must(tm.leader.UpdateTask(ctx, impl, board.TaskPatch{Status: &unblock}))
	_ = must(tm.worker.ClaimTask(ctx, impl))
	sub := must(tm.worker.Submit(ctx, board.SubmitInput{TaskID: impl, PRURL: "https://example.invalid/pr/1", Summary: "done"}))
	if sub.Status != model.StatusSubmitted || sub.PRURL == nil {
		t.Fatalf("submitted %+v", sub)
	}

	_ = must(tm.tester.ClaimTask(ctx, tests))
	rt := must(tm.tester.ReportTests(ctx, tests, 10, 1, "1 flaky"))
	if rt.Tests == nil || rt.Tests.Passed != 10 || rt.Tests.Failed != 1 {
		t.Fatalf("tests %+v", rt)
	}
	_ = must(tm.tester.Submit(ctx, board.SubmitInput{TaskID: tests, Summary: "tested"}))

	big := must(tm.leader.CreateTask(ctx, taskInput(br.ID, "big")))
	_ = must(tm.leader.Assign(ctx, big.ID, "wally", ""))
	split := must(tm.worker.SuggestSplit(ctx, big.ID, []helpreq.Proposed{
		{Ref: "a", Title: "big part a", Spec: "first half"},
		{Ref: "b", Title: "big part b", Spec: "second half", DependsOn: []string{"a"}},
	}))
	if open := must(tm.leader.ListHelpRequests(ctx, helpreq.Filter{TaskID: big.ID})); len(open) != 1 || open[0].ID != split.ID {
		t.Fatalf("open requests %+v", open)
	}
	accepted := must(tm.leader.AcceptSplit(ctx, split.ID))
	if len(accepted.Tasks) != 2 || accepted.Original.Status != model.StatusCancelled || accepted.Request.Status != helpreq.StatusAccepted {
		t.Fatalf("accepted %+v", accepted)
	}
	_ = must(tm.tester.RequestHelp(ctx, tests, "dependency_issue", "needs fixtures"))

	reviews := must(tm.reviewer.ListReviews(ctx))
	if !slices.ContainsFunc(reviews, func(x *model.Task) bool { return x.ID == impl }) {
		t.Fatalf("reviews %+v", reviews)
	}
	rv := must(tm.reviewer.Review(ctx, impl, "approve", "looks good"))
	if rv.Status != model.StatusApproved {
		t.Fatalf("review %+v", rv)
	}
	_ = must(tm.reviewer.RequestHelp(ctx, impl, "conflicting_requirements", "spec and tests disagree"))
	_ = must(tm.reviewer.SuggestSplit(ctx, impl, []helpreq.Proposed{{Title: "x", Spec: "x"}, {Title: "y", Spec: "y"}}))
	if d := must(e.owner.MarkDone(ctx, impl, "merged")); d.Status != model.StatusDone {
		t.Fatalf("done %+v", d)
	}

	svc := usage.New(usage.NewMemStore(), usage.Config{})
	reg := mcp.DefaultRegistry()
	for _, tl := range svc.Tools() {
		reg.Register(tl)
	}
	ms := mcp.NewServer(staticLookup{"usage-token": {ID: "wally", Role: model.RoleWorker, Status: model.AgentActive}}, reg)
	ts := httptest.NewServer(ms)
	defer ts.Close()
	uc := client.NewMCP(ts.URL, "usage-token", client.WithObserver(e.seen.observe))
	if err := uc.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	rec := must(uc.ReportUsage(ctx, client.UsageReport{Model: "claude-sonnet-5-5", TokensIn: 1000, TokensOut: 200, TaskID: "", RequestID: "r1"}))
	if rec.AgentID != "wally" || rec.TokensIn != 1000 || rec.Provider != "anthropic" || rec.Source != usage.SourceMCP {
		t.Fatalf("usage record %+v", rec)
	}
	_, err := uc.ReportUsage(ctx, client.UsageReport{})
	if !client.IsCode(err, client.CodeBadRequest) {
		t.Fatalf("empty usage report: %v", err)
	}

	for _, call := range []func() (json.RawMessage, error){
		func() (json.RawMessage, error) { return tm.leader.GetForecast(ctx, br.ID) },
		func() (json.RawMessage, error) { return tm.leader.GetReport(ctx, br.ID) },
		func() (json.RawMessage, error) { return tm.leader.Search(ctx, "impl", "task", 5) },
		func() (json.RawMessage, error) { return tm.leader.AgentStats(ctx, "wally") },
		func() (json.RawMessage, error) { return tm.leader.ListDecisions(ctx) },
		func() (json.RawMessage, error) { return tm.leader.ListSpawns(ctx, br.ID) },
		func() (json.RawMessage, error) { return tm.leader.GetComponents(ctx, br.ID) },
	} {
		_ = must(call())
	}

	_, _ = uc.ListArtifacts(ctx, "", 1)
	_, _ = uc.GetArtifact(ctx, "A-1")
	_, _ = uc.PutArtifact(ctx, "agent:me", "a.txt", "text/plain", []byte("x"))

	for _, role := range []string{model.RoleLeader, model.RoleWorker, model.RoleTester, model.RoleReviewer} {
		for _, tl := range mcp.DefaultRegistry().List(role) {
			if !e.seen.has(tl.Name) {
				t.Errorf("tool %s (role %s) has no client method exercised", tl.Name, role)
			}
		}
	}
	if !e.seen.has("report_usage") {
		t.Error("report_usage not exercised")
	}
}

type staticLookup map[string]*model.Agent

func (s staticLookup) LookupToken(tok string) (*model.Agent, error) { return s[tok], nil }

func TestListToolsMatchesRoles(t *testing.T) {
	defer guard(t)
	e := newEnv(t)
	tm := e.team()
	for role, c := range map[string]*client.MCP{
		model.RoleLeader: tm.leader, model.RoleWorker: tm.worker, model.RoleTester: tm.tester, model.RoleReviewer: tm.reviewer,
	} {
		got := must(c.ListTools(context.Background()))
		var names []string
		for _, tl := range got {
			names = append(names, tl.Name)
			if tl.InputSchema["type"] != "object" {
				t.Errorf("%s/%s schema %v", role, tl.Name, tl.InputSchema)
			}
		}
		var want []string
		reg := mcp.DefaultRegistry()
		mcp.RegisterTaskTemplates(reg, nil, prompts.New(""))
		for _, tl := range reg.List(role) {
			want = append(want, tl.Name)
		}
		if !slices.Equal(names, want) {
			t.Errorf("%s tools = %v, want %v", role, names, want)
		}
	}
}
