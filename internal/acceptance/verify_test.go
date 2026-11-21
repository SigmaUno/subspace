package acceptance

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/activity"
	"github.com/SigmaUno/subspace/internal/mergequeue"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/replay"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/usage"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func ptr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (fl *fleet) auditEvents(ctx context.Context, t *testing.T) []*model.AuditEvent {
	t.Helper()
	var all []*model.AuditEvent
	cursor := ""
	for {
		p, err := fl.owner.AuditPage(ctx, store.AuditFilter{Limit: 1000}, cursor)
		if err != nil {
			t.Fatalf("audit: %v", err)
		}
		all = append(all, p.Items...)
		if p.NextCursor == "" {
			return all
		}
		cursor = p.NextCursor
	}
}

func (fl *fleet) verify(ctx context.Context, t *testing.T) {
	t.Helper()
	v := fl.world.view()
	events := fl.auditEvents(ctx, t)
	fl.checkTasks(ctx, t, v)
	fl.checkOrder(t, v, events)
	fl.checkReviews(t, v, events)
	fl.checkMerges(ctx, t, v, events)
	fl.checkNotifications(t)
	fl.checkAuditChain(ctx, t, events)
	fl.checkBoardViews(ctx, t, v)
	fl.checkCosts(ctx, t, v)
	fl.checkForge(t, v)
	fl.checkQuestions(t, v)
	fl.checkLeader(t)
}

func (fl *fleet) checkTasks(ctx context.Context, t *testing.T, v *view) {
	t.Helper()
	if len(v.tasks) != len(samplePlan) {
		t.Fatalf("%d tasks, planned %d", len(v.tasks), len(samplePlan))
	}
	agents, err := fl.owner.ListAgents(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	provider := map[string]string{}
	for _, a := range agents {
		provider[a.ID] = a.Provider
	}
	want := plannedDeps()
	workers := map[string]bool{}
	for _, task := range v.tasks {
		if task.Status != model.StatusDone {
			t.Errorf("%s is %s", task.ID, task.Status)
		}
		var deps []string
		for _, d := range task.DependsOn {
			deps = append(deps, v.tasks[d].Title)
		}
		slices.Sort(deps)
		exp := append([]string(nil), want[task.Title]...)
		slices.Sort(exp)
		if !slices.Equal(deps, exp) {
			t.Errorf("%s depends on %v, planned %v", task.ID, deps, exp)
		}
		switch task.Role {
		case model.RoleWorker:
			workers[ptr(task.Assignee)] = true
		case model.RoleTester:
			if ptr(task.Assignee) != "tester-1" {
				t.Errorf("tester task %s assigned to %q", task.ID, ptr(task.Assignee))
			}
		}
		if rv := ptr(task.Reviewer); !strings.HasPrefix(rv, "reviewer-") || provider[rv] == provider[ptr(task.Assignee)] {
			t.Errorf("%s reviewed by %q (%s) after work by %q (%s)", task.ID, rv, provider[rv], ptr(task.Assignee), provider[ptr(task.Assignee)])
		}
	}
	if len(workers) != workerCount {
		t.Errorf("code tasks were spread over %d workers, want %d", len(workers), workerCount)
	}
}

func (fl *fleet) checkOrder(t *testing.T, v *view, events []*model.AuditEvent) {
	t.Helper()
	claimed, done := map[string]int64{}, map[string]int64{}
	for _, e := range events {
		if e.TaskID == nil {
			continue
		}
		switch e.Tool {
		case "claim_task":
			if _, ok := claimed[*e.TaskID]; !ok {
				claimed[*e.TaskID] = e.ID
			}
		case "mark_done":
			done[*e.TaskID] = e.ID
		}
	}
	for id, task := range v.tasks {
		if claimed[id] == 0 || done[id] == 0 {
			t.Errorf("%s: claim %d done %d", id, claimed[id], done[id])
		}
		for _, d := range task.DependsOn {
			if claimed[id] < done[d] {
				t.Errorf("%s was claimed (audit %d) before its dependency %s was done (audit %d)", id, claimed[id], d, done[d])
			}
		}
	}
}

func (fl *fleet) reviewCounts(events []*model.AuditEvent) (changes, approvals map[string]int) {
	changes, approvals = map[string]int{}, map[string]int{}
	for _, e := range events {
		if e.Tool != "review" || e.TaskID == nil {
			continue
		}
		switch e.After["status"] {
		case model.StatusChangesRequested:
			changes[*e.TaskID]++
		case model.StatusApproved:
			approvals[*e.TaskID]++
		}
	}
	return changes, approvals
}

func (fl *fleet) checkReviews(t *testing.T, v *view, events []*model.AuditEvent) {
	t.Helper()
	changes, approvals := fl.reviewCounts(events)
	for id, task := range v.tasks {
		wantRounds := 0
		if task.Title == defectTitle {
			wantRounds = 1
		}
		if task.ReviewRounds != wantRounds || changes[id] != wantRounds || approvals[id] != 1 {
			t.Errorf("%s (%s): review_rounds %d, %d changes_requested, %d approvals; want %d, %d, 1",
				id, task.Title, task.ReviewRounds, changes[id], approvals[id], wantRounds, wantRounds)
		}
	}
}

func (fl *fleet) checkMerges(ctx context.Context, t *testing.T, v *view, events []*model.AuditEvent) {
	t.Helper()
	code := map[string]bool{}
	for id, task := range v.tasks {
		if task.Role == model.RoleWorker {
			code[id] = true
		}
	}
	approvedAt := map[string]time.Time{}
	merged := map[string]string{}
	for _, e := range events {
		if e.TaskID == nil {
			continue
		}
		if e.Tool == "review" && e.After["status"] == model.StatusApproved {
			approvedAt[*e.TaskID] = e.TS
		}
		if e.Tool == "mark_done" {
			merged[*e.TaskID] = ptr(e.Note)
		}
	}
	got := map[string]bool{}
	for _, c := range v.candidates {
		got[c.TaskID] = true
		if c.State != mergequeue.StateMerged || c.MergedSHA == "" {
			t.Errorf("candidate %s for %s is %s", c.ID, c.TaskID, c.State)
		}
		if at, ok := approvedAt[c.TaskID]; !ok || c.EnqueuedAt.Before(at) {
			t.Errorf("candidate %s for %s was queued at %s, before the approval at %s", c.ID, c.TaskID, c.EnqueuedAt, at)
		}
		if !strings.Contains(merged[c.TaskID], "merged by merge queue") {
			t.Errorf("%s was marked done with %q", c.TaskID, merged[c.TaskID])
		}
	}
	if len(got) != len(code) || len(v.candidates) != len(code) {
		t.Errorf("merged %d candidates for %d tasks, want the %d code tasks", len(v.candidates), len(got), len(code))
	}
	for id := range code {
		if !got[id] || !fl.script.merged[id] {
			t.Errorf("code task %s was not merged by the train", id)
		}
	}
	for id, task := range v.tasks {
		if task.Role == model.RoleTester && !strings.Contains(merged[id], "merged by the owner") {
			t.Errorf("tester task %s was marked done with %q", id, merged[id])
		}
	}
	if fl.script.probe != "conflict" {
		t.Errorf("enqueueing a task that needs changes answered %q, want conflict", fl.script.probe)
	}
	var trains struct {
		Trains []struct {
			State   string `json:"state"`
			Members []struct {
				TaskID string `json:"task_id"`
				Result string `json:"result"`
			} `json:"members"`
		} `json:"trains"`
	}
	if err := fl.owner.Do(ctx, "trains", http.MethodGet, "/api/v1/mergequeue/trains", nil, &trains); err != nil {
		t.Fatal(err)
	}
	inTrain := map[string]bool{}
	for _, tr := range trains.Trains {
		if tr.State != "merged" {
			t.Errorf("train in state %s", tr.State)
		}
		for _, m := range tr.Members {
			if m.Result != "merged" || !code[m.TaskID] {
				t.Errorf("train member %s result %s", m.TaskID, m.Result)
			}
			inTrain[m.TaskID] = true
		}
	}
	if len(inTrain) != len(code) {
		t.Errorf("trains merged %d tasks, want %d", len(inTrain), len(code))
	}
	files, err := fl.git.files("main")
	if err != nil {
		t.Fatal(err)
	}
	var wantFiles []string
	wantFiles = append(wantFiles, "README")
	for id := range code {
		wantFiles = append(wantFiles, strings.ToLower(id)+".txt")
	}
	slices.Sort(wantFiles)
	slices.Sort(files)
	if !slices.Equal(files, wantFiles) {
		t.Errorf("main holds %v, want %v", files, wantFiles)
	}
}

func (fl *fleet) checkNotifications(t *testing.T) {
	t.Helper()
	counts := map[string]int{}
	for _, n := range fl.sink.all() {
		counts[n.Kind+" "+n.ID]++
	}
	want := map[string]int{"plan P-1": 1, "question Q-1": 1}
	for i := 1; i <= len(samplePlan); i++ {
		want[fmt.Sprintf("approved T-%d", i)] = 1
	}
	if fmt.Sprint(counts) != fmt.Sprint(want) {
		t.Errorf("notifications %v, want %v", counts, want)
	}
}

func (fl *fleet) checkAuditChain(ctx context.Context, t *testing.T, events []*model.AuditEvent) {
	t.Helper()
	var res struct {
		OK     bool `json:"ok"`
		Events int  `json:"events"`
		Head   struct {
			ID int64 `json:"id"`
		} `json:"head"`
	}
	if err := fl.owner.Do(ctx, "audit-verify", http.MethodGet, "/api/v1/audit/verify", nil, &res); err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Events != len(events) || res.Head.ID != events[len(events)-1].ID {
		t.Errorf("audit chain: %+v over %d events ending at %d", res, len(events), events[len(events)-1].ID)
	}
}

func (fl *fleet) checkBoardViews(ctx context.Context, t *testing.T, v *view) {
	t.Helper()
	live, err := fl.owner.Board(ctx, fl.brief.ID)
	if err != nil {
		t.Fatal(err)
	}
	var snap replay.Snapshot
	if err := fl.owner.Do(ctx, "replay", http.MethodGet, "/api/v1/briefs/"+fl.brief.ID+"/replay", nil, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Total != len(v.tasks) || snap.Counts[model.StatusDone] != len(v.tasks) {
		t.Errorf("replay total %d counts %v", snap.Total, snap.Counts)
	}
	for status, tasks := range live.Columns {
		var liveIDs, replayIDs []string
		for _, task := range tasks {
			liveIDs = append(liveIDs, task.ID)
		}
		for _, rt := range snap.Columns[status] {
			replayIDs = append(replayIDs, rt.ID)
			task := v.tasks[rt.ID]
			if rt.Status != task.Status || ptr(rt.Assignee) != ptr(task.Assignee) || ptr(rt.Reviewer) != ptr(task.Reviewer) ||
				rt.ProgressPct != task.ProgressPct || !near(rt.CostUsed, task.CostUsed) {
				t.Errorf("replay of %s: %+v, live %s %s %s %d %v", rt.ID, rt, task.Status, ptr(task.Assignee), ptr(task.Reviewer), task.ProgressPct, task.CostUsed)
			}
		}
		sort.Strings(liveIDs)
		sort.Strings(replayIDs)
		if !slices.Equal(liveIDs, replayIDs) {
			t.Errorf("column %s: live %v, replay %v", status, liveIDs, replayIDs)
		}
	}

	var feed activity.Page
	if err := fl.owner.Do(ctx, "activity", http.MethodGet, "/api/v1/activity?limit=1000&brief_id="+fl.brief.ID, nil, &feed); err != nil {
		t.Fatal(err)
	}
	if feed.NextCursor != nil {
		t.Fatalf("activity feed has more than one page")
	}
	perTool := map[string]map[string]int{}
	for _, it := range feed.Items {
		for _, ref := range it.Targets {
			if ref.Kind != "task" || it.Tool == "" {
				continue
			}
			if perTool[it.Tool] == nil {
				perTool[it.Tool] = map[string]int{}
			}
			perTool[it.Tool][ref.ID]++
		}
	}
	changes, approvals := fl.reviewCounts(fl.auditEvents(ctx, t))
	for id, task := range v.tasks {
		if perTool["mark_done"][id] != 1 || perTool["review"][id] != changes[id]+approvals[id] || perTool["claim_task"][id] != task.ReviewRounds+1 {
			t.Errorf("activity of %s: done %d, reviews %d, claims %d; board: %d review rounds",
				id, perTool["mark_done"][id], perTool["review"][id], perTool["claim_task"][id], task.ReviewRounds)
		}
	}
}

func (fl *fleet) checkCosts(ctx context.Context, t *testing.T, v *view) {
	t.Helper()
	led := fl.agents.ledger
	inAll, outAll, usdAll := led.sum(func(usageLine) bool { return true })
	var byAgent, byTask usage.CostBreakdown
	if err := fl.owner.Do(ctx, "costs", http.MethodGet, "/api/v1/costs?group_by=agent", nil, &byAgent); err != nil {
		t.Fatal(err)
	}
	if err := fl.owner.Do(ctx, "costs", http.MethodGet, "/api/v1/costs?group_by=task", nil, &byTask); err != nil {
		t.Fatal(err)
	}
	agents := map[string]usage.CostGroup{}
	var sumIn, sumOut int64
	var sumEUR float64
	for _, g := range byAgent.Groups {
		agents[g.Key] = g
		sumIn += g.TokensIn
		sumOut += g.TokensOut
		sumEUR += g.CostEUR
	}
	for _, w := range append(append([]*scripted{}, fl.workers...), fl.tester) {
		in, out, usd := led.sum(func(u usageLine) bool { return u.agent == w.id })
		g := agents[w.id]
		if g.TokensIn != in || g.TokensOut != out || !near(g.CostEUR, usd) {
			t.Errorf("costs of %s: %+v, reported %d in %d out %v usd", w.id, g.CostTotals, in, out, usd)
		}
	}
	if l := agents["leader-1"]; l.TokensIn != leaderTokensI || l.TokensOut != leaderTokensO {
		t.Errorf("costs of the leader: %+v", l.CostTotals)
	}
	tot := byAgent.Total
	if tot.TokensIn != inAll+leaderTokensI || tot.TokensOut != outAll+leaderTokensO || sumIn != tot.TokensIn || sumOut != tot.TokensOut || !near(sumEUR, tot.CostEUR) {
		t.Errorf("cost totals %+v, groups %d in %d out %v eur, reported %d in %d out", tot, sumIn, sumOut, sumEUR, inAll, outAll)
	}
	tasks := map[string]usage.CostGroup{}
	for _, g := range byTask.Groups {
		tasks[g.Key] = g
	}
	var used float64
	for id, task := range v.tasks {
		_, _, usd := led.sum(func(u usageLine) bool { return u.task == id })
		used += task.CostUsed
		if !near(task.CostUsed, usd) || !near(tasks[id].CostEUR, usd) {
			t.Errorf("%s: cost_used %v, costs %v, reported %v", id, task.CostUsed, tasks[id].CostEUR, usd)
		}
	}
	b, err := fl.owner.GetBrief(ctx, fl.brief.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !near(b.CostUsed, used) || !near(used, usdAll) {
		t.Errorf("brief cost_used %v, tasks %v, reported %v", b.CostUsed, used, usdAll)
	}
}

func (fl *fleet) checkForge(t *testing.T, v *view) {
	t.Helper()
	for id, task := range v.tasks {
		pr := fl.forge.pr(fl.forge.prNumber(*task.PRURL))
		if !pr.Merged || pr.Branch != task.Branch || fl.forge.state(pr.SHA) != "success" || v.prs[id].Number != int64(pr.Number) {
			t.Errorf("%s: forge pr %+v, state %q, snapshot %+v", id, pr, fl.forge.state(pr.SHA), v.prs[id])
		}
	}
	counts := map[string]int{}
	for _, s := range fl.forge.posted() {
		if s.Context != "subspace/review" {
			t.Errorf("status context %q", s.Context)
		}
		counts[s.State]++
	}
	if counts["failure"] != 1 || counts["error"] != 0 || counts["success"] != len(v.tasks) {
		t.Errorf("forge statuses %v", counts)
	}
}

func (fl *fleet) checkQuestions(t *testing.T, v *view) {
	t.Helper()
	if len(v.questions) != 1 {
		t.Fatalf("%d questions", len(v.questions))
	}
	for _, q := range v.questions {
		if q.Status != "answered" || ptr(q.Answer) != ownerAnswer || v.tasks[ptr(q.TaskID)].Title != askTitle {
			t.Errorf("question %+v", q)
		}
	}
	got := 0
	for _, w := range fl.workers {
		got += len(w.answers)
	}
	if got != 1 {
		t.Errorf("%d workers saw the answer, want 1", got)
	}
}

func (fl *fleet) checkLeader(t *testing.T) {
	t.Helper()
	proposed := 0
	for _, m := range scanLines(fl.leaderOut) {
		switch m["event"] {
		case "plan_proposed":
			proposed++
			if m["tasks"] != float64(len(samplePlan)) || m["warnings"] != float64(0) {
				t.Errorf("plan event %v", m)
			}
		case "plan_failed", "plan_invalid":
			t.Errorf("leader event %v", m)
		}
	}
	if proposed != 1 {
		t.Errorf("leader proposed %d plans:\n%s", proposed, fl.leaderOut)
	}
}

func (fl *fleet) dump(procs []*proc) string {
	var sb strings.Builder
	v := fl.world.view()
	ids := make([]string, 0, len(v.tasks))
	for id := range v.tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		task := v.tasks[id]
		fmt.Fprintf(&sb, "%s %-18s %-18s assignee=%s reviewer=%s rounds=%d\n", id, task.Title, task.Status, ptr(task.Assignee), ptr(task.Reviewer), task.ReviewRounds)
		if p, ok := v.prs[id]; ok {
			fmt.Fprintf(&sb, "  pr %s %s checks=%s head=%s changed=%s err=%v\n", p.URL, p.State, p.Checks, p.HeadSHA, p.ChangedAt.Format(time.RFC3339Nano), p.Error)
		}
	}
	fmt.Fprintf(&sb, "notifications: %d, forge statuses: %v\n", len(fl.sink.all()), fl.forge.posted())
	fmt.Fprintf(&sb, "leader:\n%s", fl.leaderOut)
	for i, p := range procs {
		fmt.Fprintf(&sb, "reviewer %d stderr:\n%s", i+1, p.stderr.String())
	}
	return sb.String()
}
