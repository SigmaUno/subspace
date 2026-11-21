package storetest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

func Run(t *testing.T, open func(t *testing.T) store.Store) {
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			s := open(t)
			sc.run(t, s)
		})
	}
}

var scenarios = []struct {
	name string
	run  func(t *testing.T, s store.Store)
}{
	{"briefs", scenarioBriefs},
	{"plans", scenarioPlans},
	{"task round trip", scenarioTaskRoundTrip},
	{"task list count cost", scenarioTaskList},
	{"extend leases", scenarioExtendLeases},
	{"agents", scenarioAgents},
	{"agent computed fields and touch", scenarioAgentComputed},
	{"messages", scenarioMessages},
	{"questions", scenarioQuestions},
	{"audit", scenarioAudit},
	{"audit append only", scenarioAuditAppendOnly},
	{"audit chain concurrent writers", scenarioAuditChain},
	{"spawns and kv", scenarioSpawnsKV},
	{"atomic semantics", scenarioAtomic},
	{"next id", scenarioNextID},
	{"concurrent writes", scenarioConcurrent},
	{"batch isolation", scenarioBatchIsolation},
	{"timeseries", scenarioTimeseries},
	{"costs", scenarioCosts},
	{"search", scenarioSearch},
	{"extension sql", scenarioExtensionSQL},
	{"keyset pagination", scenarioKeysetPagination},
}

func ptr[T any](v T) *T { return &v }

func write(t *testing.T, s store.Store, fn func(store.Tx) error) {
	t.Helper()
	if err := s.Atomic(context.Background(), fn); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, s store.Store, fn func(store.Tx) error) {
	t.Helper()
	if err := s.View(context.Background(), fn); err != nil {
		t.Fatal(err)
	}
}

func wantErr(t *testing.T, want, got error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func same(t *testing.T, want, got any) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("mismatch\nwant %+v\ngot  %+v", want, got)
	}
}

func testBrief(id string, created time.Time) *model.Brief {
	return &model.Brief{
		ID: id, Title: "Title " + id, Body: "# body\nline", Repos: []string{"SigmaUno/foo", "SigmaUno/bar"},
		Status: "open", Budget: model.BriefBudget{MaxCostEUR: 12.5, MaxParallelAgent: 7},
		AllowedProviders: []string{"anthropic", "deepseek"}, CrossReview: true, PlanStatus: "none", CreatedAt: created,
	}
}

func scenarioBriefs(t *testing.T, s store.Store) {
	b1 := testBrief("B-1", time.UnixMilli(1760000000123).UTC())
	b2 := testBrief("B-2", time.UnixMilli(1000).UTC())
	b2.Repos, b2.AllowedProviders, b2.Status, b2.CrossReview = nil, nil, "closed", false
	b10 := testBrief("B-10", time.UnixMilli(1000).UTC())
	write(t, s, func(tx store.Tx) error {
		for _, b := range []*model.Brief{b1, b2, b10} {
			if err := tx.InsertBrief(b); err != nil {
				return err
			}
		}
		return nil
	})
	b2.Repos, b2.AllowedProviders = []string{}, []string{}
	wantErr(t, store.ErrDuplicate, s.Atomic(context.Background(), func(tx store.Tx) error { return tx.InsertBrief(b1) }))

	b1.Title, b1.Budget.MaxCostEUR, b1.AllowedProviders, b1.Status, b1.AutoDoneOnMerge = "new", 99.25, []string{"openai"}, "closed", true
	b1.LeaderID, b1.QualityGates = ptr("rex"), model.QualityGates{Tests: true, MinTestsPassed: 3, MaxPRAgeHours: 1.5}
	write(t, s, func(tx store.Tx) error { return tx.UpdateBrief(b1) })
	wantErr(t, store.ErrNotFound, s.Atomic(context.Background(), func(tx store.Tx) error {
		return tx.UpdateBrief(testBrief("B-404", time.Now()))
	}))

	read(t, s, func(tx store.Tx) error {
		for _, want := range []*model.Brief{b1, b2} {
			got, err := tx.GetBrief(want.ID)
			if err != nil {
				return err
			}
			same(t, want, got)
		}
		_, err := tx.GetBrief("B-404")
		wantErr(t, store.ErrNotFound, err)
		all, err := tx.ListBriefs(store.BriefFilter{})
		if err != nil {
			return err
		}
		if len(all) != 3 || all[0].ID != "B-10" || all[1].ID != "B-2" || all[2].ID != "B-1" {
			t.Fatalf("order: %+v", all)
		}
		closed, err := tx.ListBriefs(store.BriefFilter{Status: "closed"})
		if err != nil {
			return err
		}
		if len(closed) != 2 || closed[0].ID != "B-2" || closed[1].ID != "B-1" {
			t.Fatalf("closed: %+v", closed)
		}
		none, err := tx.ListBriefs(store.BriefFilter{Status: "nope"})
		if err != nil || len(none) != 0 {
			t.Fatalf("none: %v %v", none, err)
		}
		return nil
	})
}

func scenarioPlans(t *testing.T, s store.Store) {
	created := time.UnixMilli(1700000000000).UTC()
	decided := created.Add(time.Hour)
	p1 := &model.Plan{ID: "P-1", BriefID: "B-1", Status: model.PlanProposed, ProposedBy: "rex", CreatedAt: created}
	p2 := &model.Plan{ID: "P-2", BriefID: "B-1", Status: model.PlanApproved, ProposedBy: "rex", Note: ptr("ok"),
		Warnings:  []model.PlanFinding{{Severity: "warning", Rule: "budget_missing", Task: "a", Message: "no task budget"}},
		CreatedAt: created.Add(time.Second), DecidedAt: &decided}
	p3 := &model.Plan{ID: "P-3", BriefID: "B-2", Status: model.PlanProposed, ProposedBy: "rex", Note: ptr(""), CreatedAt: created}
	write(t, s, func(tx store.Tx) error {
		for _, p := range []*model.Plan{p1, p2, p3} {
			if err := tx.InsertPlan(p); err != nil {
				return err
			}
		}
		return nil
	})
	p3.Note = nil
	wantErr(t, store.ErrDuplicate, s.Atomic(context.Background(), func(tx store.Tx) error { return tx.InsertPlan(p1) }))
	p1.Status, p1.Note, p1.DecidedAt = model.PlanRejected, ptr("no"), &decided
	write(t, s, func(tx store.Tx) error { return tx.UpdatePlan(p1) })
	wantErr(t, store.ErrNotFound, s.Atomic(context.Background(), func(tx store.Tx) error {
		return tx.UpdatePlan(&model.Plan{ID: "P-404", CreatedAt: created})
	}))
	read(t, s, func(tx store.Tx) error {
		for _, want := range []*model.Plan{p1, p2, p3} {
			got, err := tx.GetPlan(want.ID)
			if err != nil {
				return err
			}
			same(t, want, got)
		}
		_, err := tx.GetPlan("P-404")
		wantErr(t, store.ErrNotFound, err)
		for _, c := range []struct {
			brief, status string
			want          []string
		}{
			{"", "", []string{"P-1", "P-3", "P-2"}},
			{"B-1", "", []string{"P-1", "P-2"}},
			{"", model.PlanProposed, []string{"P-3"}},
			{"B-9", "", nil},
		} {
			got, err := tx.ListPlans(store.PlanFilter{BriefID: c.brief, Status: c.status})
			if err != nil {
				return err
			}
			var ids []string
			for _, p := range got {
				ids = append(ids, p.ID)
			}
			same(t, c.want, ids)
		}
		return nil
	})
}

func taskMin(id, brief string) *model.Task {
	now := time.UnixMilli(time.Now().UnixMilli()).UTC()
	return &model.Task{
		ID: id, BriefID: brief, PlanID: "P-1", Title: "t " + id, Spec: "spec", Repo: "o/r",
		Branch: "sub/" + id, Role: model.RoleWorker, Status: model.StatusReady,
		AcceptanceCriteria: []string{}, DependsOn: []string{},
		CreatedAt: now, UpdatedAt: now,
	}
}

func taskFull(id string) *model.Task {
	x := taskMin(id, "B-1")
	hb := time.UnixMilli(1_700_000_000_123).UTC()
	lease := hb.Add(15 * time.Minute)
	claimed := hb.Add(-time.Minute)
	x.AcceptanceCriteria = []string{"a", "b \"quoted\" ü"}
	x.DependsOn = []string{"T-1", "T-2"}
	x.Assignee = ptr("claude-1")
	x.Reviewer = ptr("codex-1")
	x.Status = model.StatusInProgress
	x.ProgressPct = 40
	x.LastHeartbeatAt = &hb
	x.LeaseExpiresAt = &lease
	x.ClaimedAt = &claimed
	x.PRURL = ptr("https://example.com/pr/1")
	x.Budget = model.Budget{MaxMinutes: 120, MaxCostEUR: 5.5}
	x.CostUsed = 0.4
	x.MinutesUsed = 12
	x.ReviewRounds = 2
	x.LeaseExpiries = 2
	x.Tests = &model.TestResult{Passed: 10, Failed: 1, ReportedAt: hb}
	x.BlockedReason = ptr("budget_exceeded")
	x.Priority = 3
	x.ComponentID = ptr("C-1")
	x.Paths = model.Paths{"internal/api/**", "web/app/*.tsx"}
	return x
}

func scenarioTaskRoundTrip(t *testing.T, s store.Store) {
	full, minimal := taskFull("T-1"), taskMin("T-2", "B-1")
	zeroTests := taskMin("T-3", "B-1")
	zeroTests.Tests = &model.TestResult{ReportedAt: time.UnixMilli(5).UTC()}
	nilSlices := taskMin("T-4", "B-1")
	nilSlices.AcceptanceCriteria, nilSlices.DependsOn = nil, nil
	write(t, s, func(tx store.Tx) error {
		for _, x := range []*model.Task{full, minimal, zeroTests, nilSlices} {
			if err := tx.InsertTask(x); err != nil {
				return err
			}
		}
		return nil
	})
	nilSlices.AcceptanceCriteria, nilSlices.DependsOn = []string{}, []string{}
	wantErr(t, store.ErrDuplicate, s.Atomic(context.Background(), func(tx store.Tx) error {
		return tx.InsertTask(taskMin("T-1", "B-1"))
	}))
	read(t, s, func(tx store.Tx) error {
		for _, want := range []*model.Task{full, minimal, zeroTests, nilSlices} {
			got, err := tx.GetTask(want.ID)
			if err != nil {
				return err
			}
			same(t, want, got)
		}
		_, err := tx.GetTask("T-99")
		wantErr(t, store.ErrNotFound, err)
		return nil
	})

	updated := taskFull("T-2")
	updated.CreatedAt = minimal.CreatedAt
	write(t, s, func(tx store.Tx) error { return tx.UpdateTask(updated) })
	write(t, s, func(tx store.Tx) error { return tx.UpdateTask(nilSlices) })
	read(t, s, func(tx store.Tx) error {
		got, err := tx.GetTask("T-2")
		if err != nil {
			return err
		}
		same(t, updated, got)
		got, err = tx.GetTask("T-4")
		if err != nil {
			return err
		}
		same(t, nilSlices, got)
		return nil
	})
	wantErr(t, store.ErrNotFound, s.Atomic(context.Background(), func(tx store.Tx) error {
		return tx.UpdateTask(taskMin("T-404", "B-1"))
	}))
}

func scenarioTaskList(t *testing.T, s store.Store) {
	write(t, s, func(tx store.Tx) error {
		for _, n := range []int{10, 2, 1, 11, 3} {
			x := taskMin(fmt.Sprintf("T-%d", n), "B-1")
			x.CostUsed = float64(n)
			switch n {
			case 1:
				x.Status, x.Assignee = model.StatusInProgress, ptr("a1")
			case 2:
				x.Status, x.Assignee, x.Role = model.StatusAssigned, ptr("a1"), model.RoleTester
			case 3:
				x.Status = model.StatusCancelled
			case 10:
				x.Priority = 1
			case 11:
				x.BriefID, x.PlanID, x.Reviewer, x.Status = "B-2", "P-2", ptr("r1"), model.StatusInReview
			}
			if err := tx.InsertTask(x); err != nil {
				return err
			}
		}
		return nil
	})
	cases := []struct {
		name string
		f    store.TaskFilter
		want []string
	}{
		{"all", store.TaskFilter{}, []string{"T-10", "T-1", "T-2", "T-3", "T-11"}},
		{"brief", store.TaskFilter{BriefID: "B-1"}, []string{"T-10", "T-1", "T-2", "T-3"}},
		{"plan", store.TaskFilter{PlanID: "P-2"}, []string{"T-11"}},
		{"status in", store.TaskFilter{Status: []string{model.StatusInProgress, model.StatusAssigned}}, []string{"T-1", "T-2"}},
		{"assignee", store.TaskFilter{Assignee: "a1"}, []string{"T-1", "T-2"}},
		{"assignee+role", store.TaskFilter{Assignee: "a1", Role: model.RoleTester}, []string{"T-2"}},
		{"reviewer", store.TaskFilter{Reviewer: "r1"}, []string{"T-11"}},
		{"limit", store.TaskFilter{Limit: 2}, []string{"T-10", "T-1"}},
		{"none", store.TaskFilter{BriefID: "B-9"}, []string{}},
	}
	read(t, s, func(tx store.Tx) error {
		for _, c := range cases {
			got, err := tx.ListTasks(c.f)
			if err != nil {
				return err
			}
			ids := []string{}
			for _, x := range got {
				ids = append(ids, x.ID)
			}
			if !reflect.DeepEqual(ids, c.want) {
				t.Errorf("%s: want %v got %v", c.name, c.want, ids)
			}
		}
		counts, err := tx.CountTasksByStatus()
		if err != nil {
			return err
		}
		same(t, map[string]int{
			model.StatusInProgress: 1, model.StatusAssigned: 1, model.StatusCancelled: 1,
			model.StatusReady: 1, model.StatusInReview: 1,
		}, counts)
		if c, err := tx.BriefCost("B-1"); err != nil || c != 16 {
			t.Errorf("brief cost B-1: %v %v", c, err)
		}
		if c, err := tx.BriefCost("B-404"); err != nil || c != 0 {
			t.Errorf("brief cost empty: %v %v", c, err)
		}
		return nil
	})
}

func scenarioExtendLeases(t *testing.T, s store.Store) {
	base := time.UnixMilli(1_700_000_000_000).UTC()
	lease := base.Add(10 * time.Minute)
	mk := func(id, status, assignee string) *model.Task {
		x := taskMin(id, "B-1")
		x.Status, x.Assignee, x.LastHeartbeatAt, x.LeaseExpiresAt = status, ptr(assignee), &base, &lease
		return x
	}
	write(t, s, func(tx store.Tx) error {
		for _, x := range []*model.Task{
			mk("T-1", model.StatusInProgress, "a1"),
			mk("T-2", model.StatusInProgress, "a1"),
			mk("T-3", model.StatusAssigned, "a1"),
			mk("T-4", model.StatusInProgress, "a2"),
			mk("T-5", model.StatusInProgress, "a1"),
		} {
			if err := tx.InsertTask(x); err != nil {
				return err
			}
		}
		return nil
	})
	hb := base.Add(time.Minute).UnixMilli()
	hb2 := base.Add(2 * time.Minute).UnixMilli()
	later := lease.Add(5 * time.Minute).UnixMilli()
	latest := lease.Add(9 * time.Minute).UnixMilli()
	earlier := lease.Add(-5 * time.Minute).UnixMilli()
	write(t, s, func(tx store.Tx) error {
		if err := tx.ExtendLeases(nil); err != nil {
			return err
		}
		return tx.ExtendLeases([]store.LeaseRow{
			{TaskID: "T-1", Assignee: "a1", Heartbeat: hb, Lease: later},
			{TaskID: "T-2", Assignee: "a1", Heartbeat: hb, Lease: earlier},
			{TaskID: "T-3", Assignee: "a1", Heartbeat: hb, Lease: later},
			{TaskID: "T-4", Assignee: "a1", Heartbeat: hb, Lease: later},
			{TaskID: "T-404", Assignee: "a1", Heartbeat: hb, Lease: later},
			{TaskID: "T-5", Assignee: "a1", Heartbeat: hb2, Lease: latest},
			{TaskID: "T-5", Assignee: "a1", Heartbeat: hb, Lease: later},
		})
	})
	read(t, s, func(tx store.Tx) error {
		for id, want := range map[string][2]int64{
			"T-1": {hb, later}, "T-2": {base.UnixMilli(), lease.UnixMilli()}, "T-3": {base.UnixMilli(), lease.UnixMilli()},
			"T-4": {base.UnixMilli(), lease.UnixMilli()}, "T-5": {hb2, latest},
		} {
			x, err := tx.GetTask(id)
			if err != nil {
				return err
			}
			if got := [2]int64{x.LastHeartbeatAt.UnixMilli(), x.LeaseExpiresAt.UnixMilli()}; got != want {
				t.Errorf("%s: got %v want %v", id, got, want)
			}
		}
		return nil
	})
}

func scenarioAgents(t *testing.T, s store.Store) {
	created := time.UnixMilli(1700000000123).UTC()
	seen := time.UnixMilli(1700000005000).UTC()
	expires := time.UnixMilli(1700086400456).UTC()
	full := &model.Agent{ID: "claude-1", Role: "worker", Kind: "claude-code", Model: "m", Provider: "anthropic",
		Status: "paused", PauseReason: ptr("loop"), LastSeenAt: &seen, CreatedAt: created, Spawned: true, TokenExpiresAt: &expires, TokenHash: "h1"}
	bare := &model.Agent{ID: "rex", Role: "leader", Kind: "other", Model: "m2", Provider: "openai",
		Status: "active", CreatedAt: created, TokenHash: "h2"}
	write(t, s, func(tx store.Tx) error {
		if err := tx.InsertAgent(full); err != nil {
			return err
		}
		return tx.InsertAgent(bare)
	})
	for _, a := range []*model.Agent{
		{ID: "rex", Role: "worker", Status: "active", TokenHash: "other"},
		{ID: "new", Role: "worker", Status: "active", TokenHash: "h1"},
	} {
		wantErr(t, store.ErrDuplicate, s.Atomic(context.Background(), func(tx store.Tx) error { return tx.InsertAgent(a) }))
	}
	read(t, s, func(tx store.Tx) error {
		got, err := tx.GetAgent("claude-1")
		if err != nil {
			return err
		}
		same(t, full, got)
		got, err = tx.GetAgentByTokenHash("h2")
		if err != nil {
			return err
		}
		same(t, bare, got)
		_, err = tx.GetAgent("nope")
		wantErr(t, store.ErrNotFound, err)
		_, err = tx.GetAgentByTokenHash("zzz")
		wantErr(t, store.ErrNotFound, err)
		return nil
	})

	err := s.Atomic(context.Background(), func(tx store.Tx) error {
		a, err := tx.GetAgent("rex")
		if err != nil {
			return err
		}
		a.Status, a.PauseReason, a.TokenHash = "paused", ptr("loop"), ""
		if err := tx.UpdateAgent(a); err != nil {
			return err
		}
		if a, err = tx.GetAgentByTokenHash("h2"); err != nil {
			return err
		}
		if a.Status != "paused" || str(a.PauseReason) != "loop" {
			t.Errorf("update: %+v", a)
		}
		a.TokenHash, a.PauseReason, a.Status, a.Spawned = "h3", nil, "active", true
		if err := tx.UpdateAgent(a); err != nil {
			return err
		}
		if _, err := tx.GetAgentByTokenHash("h2"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("old hash still valid: %v", err)
		}
		if a, err = tx.GetAgentByTokenHash("h3"); err != nil {
			return err
		}
		if a.PauseReason != nil || !a.Spawned {
			t.Errorf("update 2: %+v", a)
		}
		a.TokenExpiresAt = &expires
		if err := tx.UpdateAgent(a); err != nil {
			return err
		}
		if a, err = tx.GetAgent("rex"); err != nil {
			return err
		}
		if a.TokenExpiresAt == nil || !a.TokenExpiresAt.Equal(expires) {
			t.Errorf("update expiry: %v", a.TokenExpiresAt)
		}
		return tx.UpdateAgent(&model.Agent{ID: "ghost", Status: "active"})
	})
	wantErr(t, store.ErrNotFound, err)
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func scenarioAgentComputed(t *testing.T, s store.Store) {
	write(t, s, func(tx store.Tx) error {
		for _, x := range []struct{ id, role, status string }{
			{"w-3", "worker", "active"}, {"w-1", "worker", "revoked"}, {"w-2", "worker", "active"},
			{"rex", "leader", "active"}, {"W-0", "worker", "active"},
		} {
			if err := tx.InsertAgent(&model.Agent{ID: x.id, Role: x.role, Kind: "k", Model: "m", Provider: "p",
				Status: x.status, CreatedAt: time.Now(), TokenHash: "h-" + x.id}); err != nil {
				return err
			}
		}
		for _, x := range []struct{ id, assignee, status string }{
			{"T-1", "w-1", "assigned"}, {"T-2", "w-1", "in_progress"}, {"T-3", "w-1", "changes_requested"},
			{"T-4", "w-1", "submitted"}, {"T-5", "w-1", "done"}, {"T-6", "w-2", "assigned"}, {"T-7", "", "ready"},
		} {
			task := taskMin(x.id, "B-1")
			task.Status, task.Assignee = x.status, nil
			if x.assignee != "" {
				task.Assignee = ptr(x.assignee)
			}
			if err := tx.InsertTask(task); err != nil {
				return err
			}
		}
		return nil
	})
	ids := func(l []*model.Agent) []string {
		o := []string{}
		for _, a := range l {
			o = append(o, a.ID)
		}
		return o
	}
	read(t, s, func(tx store.Tx) error {
		all, err := tx.ListAgents(store.AgentFilter{})
		if err != nil {
			return err
		}
		same(t, []string{"W-0", "rex", "w-1", "w-2", "w-3"}, ids(all))
		w1, w2, w3 := all[2], all[3], all[4]
		if w1.AssignedCount != 3 || str(w1.CurrentTaskID) != "T-2" {
			t.Errorf("w-1: %+v", w1)
		}
		if w2.AssignedCount != 1 || w2.CurrentTaskID != nil || w3.AssignedCount != 0 || w3.CurrentTaskID != nil {
			t.Errorf("w-2/w-3: %+v %+v", w2, w3)
		}
		l, err := tx.ListAgents(store.AgentFilter{Role: "worker", Status: "active"})
		if err != nil {
			return err
		}
		same(t, []string{"W-0", "w-2", "w-3"}, ids(l))
		if l, err = tx.ListAgents(store.AgentFilter{Limit: 2}); err != nil || len(l) != 2 {
			t.Errorf("limit: %v %v", l, err)
		}
		if l, err = tx.ListAgents(store.AgentFilter{Role: "tester"}); err != nil || l == nil || len(l) != 0 {
			t.Errorf("empty: %v %v", l, err)
		}
		return nil
	})
	write(t, s, func(tx store.Tx) error {
		if err := tx.TouchAgents(map[string]int64{"w-1": 5000, "rex": 7000, "unknown": 1}); err != nil {
			return err
		}
		if err := tx.TouchAgents(nil); err != nil {
			return err
		}
		return tx.TouchAgents(map[string]int64{"w-1": 4000, "rex": 9000})
	})
	read(t, s, func(tx store.Tx) error {
		for id, want := range map[string]int64{"w-1": 5000, "rex": 9000, "w-2": 0} {
			a, err := tx.GetAgent(id)
			if err != nil {
				return err
			}
			var got int64
			if a.LastSeenAt != nil {
				got = a.LastSeenAt.UnixMilli()
			}
			if got != want {
				t.Errorf("%s last seen %d want %d", id, got, want)
			}
		}
		return nil
	})
}

func scenarioMessages(t *testing.T, s store.Store) {
	base := time.Date(2026, 11, 20, 21, 4, 5, 123_000_000, time.UTC)
	var ids []int64
	write(t, s, func(tx store.Tx) error {
		for i := 0; i < 205; i++ {
			m := &model.Message{ThreadKind: "task", ThreadID: "T-1", AuthorID: "a", AuthorRole: "worker",
				Kind: "note", Body: fmt.Sprintf("hello %d", i), CreatedAt: base.Add(time.Duration(i) * time.Second)}
			if err := tx.InsertMessage(m); err != nil {
				return err
			}
			ids = append(ids, m.ID)
		}
		return tx.InsertMessage(&model.Message{ThreadKind: "agent", ThreadID: "a", AuthorID: "owner",
			AuthorRole: "owner", Kind: "message", Body: "other", CreatedAt: base})
	})
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] || ids[i-1] == 0 {
			t.Fatalf("ids not increasing at %d: %d %d", i, ids[i-1], ids[i])
		}
	}
	read(t, s, func(tx store.Tx) error {
		all, err := tx.ListMessages("task", "T-1", 0, 0)
		if err != nil {
			return err
		}
		if len(all) != 200 {
			t.Fatalf("default limit: %d", len(all))
		}
		same(t, &model.Message{ID: ids[0], ThreadKind: "task", ThreadID: "T-1", AuthorID: "a", AuthorRole: "worker",
			Kind: "note", Body: "hello 0", CreatedAt: base}, all[0])
		since, err := tx.ListMessages("task", "T-1", ids[202], 0)
		if err != nil || len(since) != 2 || since[0].ID != ids[203] {
			t.Fatalf("since: %v %+v", err, since)
		}
		lim, err := tx.ListMessages("task", "T-1", 0, 2)
		if err != nil || len(lim) != 2 || lim[1].ID != ids[1] {
			t.Fatalf("limit: %v %+v", err, lim)
		}
		none, err := tx.ListMessages("brief", "B-1", 0, 0)
		if err != nil || none == nil || len(none) != 0 {
			t.Fatalf("none: %v %v", none, err)
		}
		return nil
	})
}

func scenarioQuestions(t *testing.T, s store.Store) {
	base := time.Date(2026, 11, 20, 21, 4, 5, 0, time.UTC)
	full := &model.Question{ID: "Q-1", BriefID: ptr("B-1"), TaskID: ptr("T-3"), AgentID: "claude-1", Question: "which?",
		Options: []string{"a", "b"}, Status: "open", CreatedAt: base}
	bare := &model.Question{ID: "Q-2", AgentID: "claude-2", Question: "why?", Status: "open", CreatedAt: base.Add(time.Second)}
	early := &model.Question{ID: "Q-3", AgentID: "x", Question: "early", Status: "open", CreatedAt: base.Add(-time.Hour)}
	write(t, s, func(tx store.Tx) error {
		for _, q := range []*model.Question{full, bare, early} {
			if err := tx.InsertQuestion(q); err != nil {
				return err
			}
		}
		return nil
	})
	bare.Options = []string{}
	wantErr(t, store.ErrDuplicate, s.Atomic(context.Background(), func(tx store.Tx) error { return tx.InsertQuestion(full) }))
	at := base.Add(time.Minute)
	full.Status, full.Answer, full.AnsweredAt = "answered", ptr("a"), &at
	write(t, s, func(tx store.Tx) error { return tx.UpdateQuestion(full) })
	wantErr(t, store.ErrNotFound, s.Atomic(context.Background(), func(tx store.Tx) error {
		return tx.UpdateQuestion(&model.Question{ID: "Q-404", AgentID: "x", Status: "open", CreatedAt: base})
	}))
	read(t, s, func(tx store.Tx) error {
		for _, want := range []*model.Question{full, bare} {
			got, err := tx.GetQuestion(want.ID)
			if err != nil {
				return err
			}
			same(t, want, got)
		}
		_, err := tx.GetQuestion("Q-404")
		wantErr(t, store.ErrNotFound, err)
		for status, want := range map[string][]string{"": {"Q-3", "Q-1", "Q-2"}, "open": {"Q-3", "Q-2"}, "answered": {"Q-1"}} {
			got, err := tx.ListQuestions(store.QuestionFilter{Status: status})
			if err != nil {
				return err
			}
			ids := []string{}
			for _, q := range got {
				ids = append(ids, q.ID)
			}
			same(t, want, ids)
		}
		return nil
	})
}

func scenarioAudit(t *testing.T, s store.Store) {
	ts := time.Date(2026, 11, 20, 21, 4, 5, 123_000_000, time.UTC)
	full := &model.AuditEvent{
		TS: ts, AgentID: "claude-1", Role: "worker", Tool: "claim_task", TaskID: ptr("T-3"),
		Before: map[string]any{"status": "assigned"},
		After:  map[string]any{"status": "in_progress", "n": float64(2), "nested": map[string]any{"a": []any{"x"}}},
		Note:   ptr("hello"),
	}
	minimal := &model.AuditEvent{TS: ts.Add(time.Second), AgentID: "owner", Role: "owner", Tool: "pause_all",
		Before: map[string]any{}}
	third := &model.AuditEvent{TS: ts.Add(2 * time.Second), AgentID: "claude-1", Role: "worker", Tool: "submit", TaskID: ptr("T-4")}
	write(t, s, func(tx store.Tx) error {
		for _, ev := range []*model.AuditEvent{full, minimal, third} {
			if err := tx.InsertAudit(ev); err != nil {
				return err
			}
		}
		for i := 0; i < 200; i++ {
			if err := tx.InsertAudit(&model.AuditEvent{TS: ts, AgentID: "bulk", Role: "worker", Tool: "x"}); err != nil {
				return err
			}
		}
		return nil
	})
	if full.ID == 0 || minimal.ID <= full.ID || third.ID <= minimal.ID {
		t.Fatalf("ids not ascending: %d %d %d", full.ID, minimal.ID, third.ID)
	}
	list := func(f store.AuditFilter) []*model.AuditEvent {
		var out []*model.AuditEvent
		read(t, s, func(tx store.Tx) (err error) { out, err = tx.ListAudit(f); return })
		return out
	}
	all := list(store.AuditFilter{})
	if len(all) != 200 {
		t.Fatalf("default limit: %d", len(all))
	}
	same(t, []*model.AuditEvent{full, minimal, third}, all[:3])
	if got := list(store.AuditFilter{AgentID: "claude-1"}); len(got) != 2 {
		t.Fatalf("agent filter: %d", len(got))
	}
	if got := list(store.AuditFilter{TaskID: "T-4", Tool: "submit"}); len(got) != 1 || got[0].ID != third.ID {
		t.Fatalf("task+tool filter: %+v", got)
	}
	if got := list(store.AuditFilter{SinceID: full.ID, Limit: 1}); len(got) != 1 || got[0].ID != minimal.ID {
		t.Fatalf("since+limit: %+v", got)
	}
	if got := list(store.AuditFilter{AgentID: "nobody"}); got == nil || len(got) != 0 {
		t.Fatalf("empty: %#v", got)
	}
	same(t, []*model.AuditEvent{full}, list(store.AuditFilter{Limit: 1}))
}

func scenarioSpawnsKV(t *testing.T, s store.Store) {
	ts := time.Date(2026, 11, 20, 21, 0, 0, 0, time.UTC)
	sp1 := &model.Spawn{ID: "S-1", BriefID: "B-1", AgentID: "claude-b1-1", Kind: "claude-code", Model: "m", Provider: "anthropic",
		Status: "requested", Spawner: "noop", RequestedBy: "rex", CreatedAt: ts}
	sp2 := &model.Spawn{ID: "S-2", BriefID: "B-2", AgentID: "codex-b2-1", Kind: "codex", Model: "m2", Provider: "openai",
		Status: "failed", Spawner: "docker", Error: ptr("not implemented"), RequestedBy: "rex", CreatedAt: ts}
	sp10 := &model.Spawn{ID: "S-10", BriefID: "B-1", AgentID: "x", Kind: "opencode", Model: "m3", Provider: "deepseek",
		Status: "requested", Spawner: "noop", RequestedBy: "rex", CreatedAt: ts}
	write(t, s, func(tx store.Tx) error {
		for _, sp := range []*model.Spawn{sp10, sp2, sp1} {
			if err := tx.InsertSpawn(sp); err != nil {
				return err
			}
		}
		return nil
	})
	wantErr(t, store.ErrDuplicate, s.Atomic(context.Background(), func(tx store.Tx) error { return tx.InsertSpawn(sp1) }))
	sp1.Status, sp1.Error = "failed", ptr("boom")
	write(t, s, func(tx store.Tx) error { return tx.UpdateSpawn(sp1) })
	sp2.Status, sp2.Error = "spawned", nil
	write(t, s, func(tx store.Tx) error { return tx.UpdateSpawn(sp2) })
	wantErr(t, store.ErrNotFound, s.Atomic(context.Background(), func(tx store.Tx) error {
		return tx.UpdateSpawn(&model.Spawn{ID: "S-99"})
	}))
	read(t, s, func(tx store.Tx) error {
		all, err := tx.ListSpawns(store.SpawnFilter{})
		if err != nil {
			return err
		}
		same(t, []*model.Spawn{sp1, sp2, sp10}, all)
		b1, err := tx.ListSpawns(store.SpawnFilter{BriefID: "B-1"})
		if err != nil {
			return err
		}
		same(t, []*model.Spawn{sp1, sp10}, b1)
		if none, err := tx.ListSpawns(store.SpawnFilter{BriefID: "B-none"}); err != nil || none == nil || len(none) != 0 {
			t.Fatalf("empty: %#v %v", none, err)
		}
		if v, ok, err := tx.GetKV("missing"); err != nil || ok || v != "" {
			t.Fatalf("missing kv: %q %v %v", v, ok, err)
		}
		return nil
	})
	write(t, s, func(tx store.Tx) error {
		if err := tx.SetKV("charter", "one"); err != nil {
			return err
		}
		if err := tx.SetKV("paused", ""); err != nil {
			return err
		}
		return tx.SetKV("charter", "two")
	})
	read(t, s, func(tx store.Tx) error {
		if v, ok, err := tx.GetKV("charter"); err != nil || !ok || v != "two" {
			t.Fatalf("charter: %q %v %v", v, ok, err)
		}
		if v, ok, err := tx.GetKV("paused"); err != nil || !ok || v != "" {
			t.Fatalf("paused: %q %v %v", v, ok, err)
		}
		return nil
	})
}

func nextID(t *testing.T, s store.Store, prefix string) string {
	t.Helper()
	var id string
	write(t, s, func(tx store.Tx) (err error) { id, err = tx.NextID(prefix); return })
	return id
}

func scenarioAtomic(t *testing.T, s store.Store) {
	ctx := context.Background()
	sentinel := errors.New("nope")
	err := s.Atomic(ctx, func(tx store.Tx) error {
		if _, err := tx.NextID("R"); err != nil {
			return err
		}
		if err := tx.SetKV("k", "v"); err != nil {
			return err
		}
		return sentinel
	})
	wantErr(t, sentinel, err)
	err = s.Atomic(ctx, func(tx store.Tx) error {
		if _, err := tx.NextID("R"); err != nil {
			return err
		}
		panic("kaboom")
	})
	if err == nil || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("panic err = %v", err)
	}
	if id := nextID(t, s, "R"); id != "R-1" {
		t.Fatalf("id = %s, want R-1 (failed ops rolled back)", id)
	}
	read(t, s, func(tx store.Tx) error {
		if _, ok, err := tx.GetKV("k"); err != nil || ok {
			t.Fatalf("kv survived rollback: %v %v", ok, err)
		}
		return nil
	})
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	wantErr(t, context.Canceled, s.Atomic(cctx, func(tx store.Tx) error { _, err := tx.NextID("R"); return err }))
	if id := nextID(t, s, "R"); id != "R-2" {
		t.Fatalf("id = %s, want R-2 (cancelled op must not run)", id)
	}
}

func scenarioConcurrent(t *testing.T, s store.Store) {
	const workers, per = 32, 15
	var (
		mu   sync.Mutex
		seen = map[string]bool{}
		wg   sync.WaitGroup
		fail = errors.New("fail")
	)
	errc := make(chan error, workers*per)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				bad := (w+i)%7 == 0
				err := s.Atomic(context.Background(), func(tx store.Tx) error {
					id, err := tx.NextID("T")
					if err != nil {
						return err
					}
					if bad {
						if err := tx.InsertTask(taskMin("X-"+id, "B-1")); err != nil {
							return err
						}
						return fail
					}
					if err := tx.InsertTask(taskMin(id, "B-1")); err != nil {
						return err
					}
					mu.Lock()
					defer mu.Unlock()
					if seen[id] {
						return fmt.Errorf("duplicate %s", id)
					}
					seen[id] = true
					return nil
				})
				if bad && !errors.Is(err, fail) || !bad && err != nil {
					errc <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Fatal(err)
	}
	read(t, s, func(tx store.Tx) error {
		tasks, err := tx.ListTasks(store.TaskFilter{})
		if err != nil {
			return err
		}
		if len(tasks) != len(seen) {
			t.Fatalf("tasks %d, committed ids %d", len(tasks), len(seen))
		}
		for i, x := range tasks {
			if want := fmt.Sprintf("T-%d", i+1); x.ID != want {
				t.Fatalf("task %d = %s, want %s (gap from rolled-back op?)", i, x.ID, want)
			}
		}
		return nil
	})
}

func scenarioAuditAppendOnly(t *testing.T, s store.Store) {
	ts := time.Date(2026, 11, 20, 21, 4, 5, 0, time.UTC)
	mk := func(i int) *model.AuditEvent {
		return &model.AuditEvent{TS: ts.Add(time.Duration(i) * time.Second), AgentID: "a", Role: "worker", Tool: "update_status",
			TaskID: ptr("T-1"), Before: map[string]any{"i": float64(i)}, After: map[string]any{"i": float64(i + 1)}}
	}
	list := func() []*model.AuditEvent {
		var out []*model.AuditEvent
		read(t, s, func(tx store.Tx) (err error) { out, err = tx.ListAudit(store.AuditFilter{}); return })
		return out
	}
	write(t, s, func(tx store.Tx) error {
		for i := 0; i < 3; i++ {
			if err := tx.InsertAudit(mk(i)); err != nil {
				return err
			}
		}
		return nil
	})
	before := list()
	if len(before) != 3 {
		t.Fatalf("events: %d", len(before))
	}

	sentinel := errors.New("rollback")
	wantErr(t, sentinel, s.Atomic(context.Background(), func(tx store.Tx) error {
		if err := tx.InsertAudit(mk(10)); err != nil {
			return err
		}
		return sentinel
	}))
	same(t, before, list())

	before[0].Tool, before[0].Before["i"] = "tampered", float64(99)
	write(t, s, func(tx store.Tx) error {
		for i := 3; i < 6; i++ {
			if err := tx.InsertAudit(mk(i)); err != nil {
				return err
			}
		}
		return nil
	})
	all := list()
	if len(all) != 6 {
		t.Fatalf("events: %d", len(all))
	}
	for i, ev := range all {
		if i > 0 && ev.ID <= all[i-1].ID {
			t.Fatalf("ids not ascending at %d", i)
		}
		if want := mk(i); ev.Tool != want.Tool || !reflect.DeepEqual(ev.Before, want.Before) || !ev.TS.Equal(want.TS) {
			t.Fatalf("event %d changed: %+v", i, ev)
		}
	}
}

func scenarioNextID(t *testing.T, s store.Store) {
	for _, want := range []string{"B-1", "B-2", "T-1", "B-3", "T-2", "Q-1"} {
		if got := nextID(t, s, want[:1]); got != want {
			t.Fatalf("id = %s, want %s", got, want)
		}
	}
	const workers, per = 24, 10
	var (
		mu   sync.Mutex
		seen = map[string]bool{}
		wg   sync.WaitGroup
	)
	errc := make(chan error, workers*per)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				var id string
				err := s.Atomic(context.Background(), func(tx store.Tx) (err error) {
					id, err = tx.NextID("S")
					return
				})
				if err != nil {
					errc <- err
					continue
				}
				mu.Lock()
				if seen[id] {
					errc <- fmt.Errorf("duplicate id %s", id)
				}
				seen[id] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Fatal(err)
	}
	for i := 1; i <= workers*per; i++ {
		if id := fmt.Sprintf("S-%d", i); !seen[id] {
			t.Fatalf("missing %s", id)
		}
	}
	if id := nextID(t, s, "S"); id != fmt.Sprintf("S-%d", workers*per+1) {
		t.Fatalf("next id after burst: %s", id)
	}
}

func scenarioBatchIsolation(t *testing.T, s store.Store) {
	write(t, s, func(tx store.Tx) error { return tx.InsertBrief(testBrief("B-seed", time.Now())) })
	errBoom := errors.New("boom")
	const n = 96
	start := make(chan struct{})
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = s.Atomic(context.Background(), func(tx store.Tx) error {
				if err := tx.SetKV(fmt.Sprintf("k%d", i), "v"); err != nil {
					return err
				}
				if err := tx.InsertBrief(testBrief(fmt.Sprintf("B-%d", i), time.Now())); err != nil {
					return err
				}
				switch i % 4 {
				case 1:
					return errBoom
				case 2:
					_ = tx.InsertBrief(testBrief("B-seed", time.Now()))
				case 3:
					if i%8 == 3 {
						panic("kaboom")
					}
					return tx.UpdateTask(taskMin("T-404", "B-1"))
				}
				return nil
			})
		}(i)
	}
	close(start)
	wg.Wait()
	read(t, s, func(tx store.Tx) error {
		committed := 1
		for i, err := range errs {
			ok := i%4 == 0 || (i%4 == 2 && err == nil)
			switch {
			case i%4 == 0 && err != nil:
				t.Errorf("op %d: %v", i, err)
			case i%4 == 1 && !errors.Is(err, errBoom):
				t.Errorf("op %d: want boom, got %v", i, err)
			case i%4 == 3 && i%8 == 3 && (err == nil || !strings.Contains(err.Error(), "kaboom")):
				t.Errorf("op %d: want panic error, got %v", i, err)
			case i%4 == 3 && i%8 != 3 && !errors.Is(err, store.ErrNotFound):
				t.Errorf("op %d: want not found, got %v", i, err)
			}
			_, has, kerr := tx.GetKV(fmt.Sprintf("k%d", i))
			if kerr != nil {
				return kerr
			}
			_, berr := tx.GetBrief(fmt.Sprintf("B-%d", i))
			if berr != nil && !errors.Is(berr, store.ErrNotFound) {
				return berr
			}
			if has != ok || (berr == nil) != ok {
				t.Errorf("op %d: committed effects kv=%v brief=%v, want %v", i, has, berr == nil, ok)
			}
			if ok {
				committed++
			}
		}
		briefs, err := tx.ListBriefs(store.BriefFilter{})
		if err != nil {
			return err
		}
		if len(briefs) != committed {
			t.Errorf("briefs %d, want %d", len(briefs), committed)
		}
		return nil
	})
}

func scenarioAuditChain(t *testing.T, s store.Store) {
	const writers = 300
	ts := time.Date(2026, 11, 20, 21, 0, 0, 0, time.UTC)
	fail := errors.New("rolled back")
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := s.Atomic(context.Background(), func(tx store.Tx) error {
				e := &model.AuditEvent{TS: ts.Add(time.Duration(i) * time.Millisecond), AgentID: fmt.Sprintf("w-%03d", i),
					Role: "worker", Tool: "update_status", TaskID: ptr(fmt.Sprintf("T-%d", i)),
					Before: map[string]any{"status": "assigned", "nested": map[string]any{"b": i, "a": "<&>"}},
					After:  map[string]any{"status": "in_progress", "cost": 0.1 * float64(i)}}
				if err := tx.InsertAudit(e); err != nil {
					return err
				}
				if i%7 == 0 {
					return fail
				}
				return nil
			})
			if err != nil && !errors.Is(err, fail) {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	var (
		all []*model.AuditEvent
		chk model.AuditCheck
	)
	read(t, s, func(tx store.Tx) (err error) {
		if all, err = tx.ListAudit(store.AuditFilter{Limit: 1000}); err != nil {
			return err
		}
		chk, err = tx.VerifyAudit()
		return err
	})
	committed := writers - (writers+6)/7
	if len(all) != committed {
		t.Fatalf("events: %d want %d", len(all), committed)
	}
	last := all[len(all)-1]
	same(t, model.AuditCheck{OK: true, Events: int64(committed), Head: model.AuditHead{ID: last.ID, Hash: last.Hash}}, chk)
	seen := map[string]bool{}
	for _, e := range all {
		if len(e.Hash) != 64 || seen[e.Hash] {
			t.Fatalf("event %d has bad or duplicate hash %q", e.ID, e.Hash)
		}
		seen[e.Hash] = true
	}

	mid := all[len(all)/2]
	read(t, s, func(tx store.Tx) error {
		for _, c := range []struct {
			max  int64
			want model.AuditHead
		}{
			{0, model.AuditHead{}},
			{mid.ID, model.AuditHead{ID: mid.ID, Hash: mid.Hash}},
			{1 << 62, model.AuditHead{ID: last.ID, Hash: last.Hash}},
		} {
			h, err := tx.AuditHead(c.max)
			if err != nil {
				return err
			}
			same(t, c.want, h)
		}
		return nil
	})
}

func scenarioSearch(t *testing.T, s store.Store) {
	t0 := time.UnixMilli(1760000000000).UTC()
	at := func(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }
	var msgID, pctID int64
	write(t, s, func(tx store.Tx) error {
		b := testBrief("B-1", at(0))
		b.Title = "Login revamp"
		if err := tx.InsertBrief(b); err != nil {
			return err
		}
		for i, x := range []struct{ title, spec string }{{"Add LOGIN form", "spec"}, {"other", "fix login bug"}, {"unrelated", "nope"}} {
			k := taskMin(fmt.Sprintf("T-%d", i+1), "B-1")
			k.Title, k.Spec, k.UpdatedAt = x.title, x.spec, at(i+1)
			if err := tx.InsertTask(k); err != nil {
				return err
			}
		}
		for i, a := range []*model.Agent{
			{ID: "login-bot", Role: "worker", Kind: "other", Model: "m", Provider: "openai", Status: "active", TokenHash: "s1"},
			{ID: "x", Role: "worker", Kind: "other", Model: "sonnet-login", Provider: "openai", Status: "active", TokenHash: "s2"},
		} {
			a.CreatedAt = at(i + 3)
			if err := tx.InsertAgent(a); err != nil {
				return err
			}
		}
		m := &model.Message{ThreadKind: "task", ThreadID: "T-1", AuthorID: "a", AuthorRole: "worker", Kind: "note", Body: "about login", CreatedAt: at(5)}
		if err := tx.InsertMessage(m); err != nil {
			return err
		}
		msgID = m.ID
		p := &model.Message{ThreadKind: "task", ThreadID: "T-1", AuthorID: "a", AuthorRole: "worker", Kind: "note", Body: "100% done", CreatedAt: at(6)}
		if err := tx.InsertMessage(p); err != nil {
			return err
		}
		pctID = p.ID
		return nil
	})
	search := func(q store.SearchQuery) []store.SearchHit {
		t.Helper()
		var hits []store.SearchHit
		read(t, s, func(tx store.Tx) error {
			st, ok := tx.(store.SearchTx)
			if !ok {
				t.Fatal("store does not implement search")
			}
			var err error
			hits, err = st.Search(q)
			return err
		})
		return hits
	}
	all := []store.SearchHit{
		{Kind: "message", ID: fmt.Sprint(msgID), Field: "body", Text: "about login", Weight: 1, At: at(5)},
		{Kind: "agent", ID: "x", Field: "model", Text: "sonnet-login", Weight: 1, At: at(4)},
		{Kind: "agent", ID: "login-bot", Field: "id", Text: "login-bot", Weight: 3, At: at(3)},
		{Kind: "task", ID: "T-2", Field: "spec", Text: "fix login bug", Weight: 1, At: at(2)},
		{Kind: "task", ID: "T-1", Field: "title", Text: "Add LOGIN form", Weight: 3, At: at(1)},
		{Kind: "brief", ID: "B-1", Field: "title", Text: "Login revamp", Weight: 3, At: at(0)},
	}
	same(t, all, search(store.SearchQuery{Text: "login", Limit: 10}))
	same(t, all[:2], search(store.SearchQuery{Text: "LOGIN", Limit: 2}))
	same(t, all[3:5], search(store.SearchQuery{Text: "login", Kind: "task", Limit: 10}))
	same(t, []store.SearchHit{{Kind: "message", ID: fmt.Sprint(pctID), Field: "body", Text: "100% done", Weight: 1, At: at(6)}},
		search(store.SearchQuery{Text: "100%", Limit: 10}))
	if hits := search(store.SearchQuery{Text: "_", Limit: 10}); len(hits) != 0 {
		t.Fatalf("underscore must match literally: %+v", hits)
	}
}

type extensionSQL interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
}

func scenarioExtensionSQL(t *testing.T, s store.Store) {
	upsert := func(k, v string) {
		write(t, s, func(tx store.Tx) error {
			x, ok := tx.(extensionSQL)
			if !ok {
				t.Fatal("write tx does not expose SQL")
			}
			_, err := x.Exec(`INSERT INTO kv(k, v) VALUES(?, ? || '?') ON CONFLICT(k) DO UPDATE SET v = excluded.v`, k, v)
			return err
		})
	}
	upsert("ext", "one")
	upsert("ext", "two")
	read(t, s, func(tx store.Tx) error {
		x, ok := tx.(extensionSQL)
		if !ok {
			t.Fatal("read tx does not expose SQL")
		}
		rows, err := x.Query(`SELECT k, v FROM kv WHERE k = ? AND v <> '?'`, "ext")
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		var got []string
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				return err
			}
			got = append(got, k+"="+v)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		same(t, []string{"ext=two?"}, got)
		v, ok, err := tx.GetKV("ext")
		if err != nil || !ok || v != "two?" {
			t.Fatalf("kv: %q %v %v", v, ok, err)
		}
		return nil
	})
}
