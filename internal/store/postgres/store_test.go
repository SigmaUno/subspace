package postgres_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/postgres"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

type env struct {
	s    store.Store
	exec func(stmt string) error
}

type backend struct {
	name string
	open func(t *testing.T) env
}

func backends() []backend {
	bs := []backend{{"sqlite", openSQLite}}
	if os.Getenv("SUBSPACE_TEST_PG_DSN") != "" {
		bs = append(bs, backend{"postgres", func(t *testing.T) env {
			s, db := openPG(t)
			return env{s: s, exec: func(stmt string) error { _, err := db.Exec(stmt); return err }}
		}})
	}
	return bs
}

func openSQLite(t *testing.T) env {
	t.Helper()
	path := t.TempDir() + "/t.db"
	s, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return env{s: s, exec: func(stmt string) error { _, err := db.Exec(stmt); return err }}
}

func pgSchemaDSN(t *testing.T) (string, *sql.DB) {
	t.Helper()
	base := os.Getenv("SUBSPACE_TEST_PG_DSN")
	if base == "" {
		t.Skip("SUBSPACE_TEST_PG_DSN not set")
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "subspace_test_" + hex.EncodeToString(b)
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	var dsn string
	if strings.HasPrefix(base, "postgres://") || strings.HasPrefix(base, "postgresql://") {
		u, err := url.Parse(base)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		dsn = u.String()
	} else {
		dsn = base + " search_path=" + schema
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return dsn, db
}

func openPGAt(t *testing.T, dsn string) *postgres.Store {
	t.Helper()
	s, err := postgres.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func openPG(t *testing.T) (*postgres.Store, *sql.DB) {
	t.Helper()
	dsn, db := pgSchemaDSN(t)
	return openPGAt(t, dsn), db
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

func TestStoreConformance(t *testing.T) {
	scenarios := []struct {
		name string
		run  func(t *testing.T, e env)
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
		{"spawns and kv", scenarioSpawnsKV},
		{"atomic semantics", scenarioAtomic},
		{"concurrent writes", scenarioConcurrent},
	}
	for _, b := range backends() {
		t.Run(b.name, func(t *testing.T) {
			for _, sc := range scenarios {
				t.Run(sc.name, func(t *testing.T) { sc.run(t, b.open(t)) })
			}
		})
	}
}

func testBrief(id string, created time.Time) *model.Brief {
	return &model.Brief{
		ID: id, Title: "Title " + id, Body: "# body\nline", Repos: []string{"SigmaUno/foo", "SigmaUno/bar"},
		Status: "open", Budget: model.BriefBudget{MaxCostEUR: 12.5, MaxParallelAgent: 7},
		AllowedProviders: []string{"anthropic", "deepseek"}, PlanStatus: "none", CreatedAt: created,
	}
}

func scenarioBriefs(t *testing.T, e env) {
	b1 := testBrief("B-1", time.UnixMilli(1760000000123).UTC())
	b2 := testBrief("B-2", time.UnixMilli(1000).UTC())
	b2.Repos, b2.AllowedProviders, b2.Status = nil, nil, "closed"
	b10 := testBrief("B-10", time.UnixMilli(1000).UTC())
	write(t, e.s, func(tx store.Tx) error {
		for _, b := range []*model.Brief{b1, b2, b10} {
			if err := tx.InsertBrief(b); err != nil {
				return err
			}
		}
		return nil
	})
	b2.Repos, b2.AllowedProviders = []string{}, []string{}
	wantErr(t, store.ErrDuplicate, e.s.Atomic(context.Background(), func(tx store.Tx) error { return tx.InsertBrief(b1) }))

	b1.Title, b1.Budget.MaxCostEUR, b1.AllowedProviders, b1.Status = "new", 99.25, []string{"openai"}, "closed"
	write(t, e.s, func(tx store.Tx) error { return tx.UpdateBrief(b1) })
	wantErr(t, store.ErrNotFound, e.s.Atomic(context.Background(), func(tx store.Tx) error {
		return tx.UpdateBrief(testBrief("B-404", time.Now()))
	}))

	read(t, e.s, func(tx store.Tx) error {
		for _, want := range []*model.Brief{b1, b2} {
			got, err := tx.GetBrief(want.ID)
			if err != nil {
				return err
			}
			same(t, want, got)
		}
		_, err := tx.GetBrief("B-404")
		wantErr(t, store.ErrNotFound, err)
		all, err := tx.ListBriefs("")
		if err != nil {
			return err
		}
		if len(all) != 3 || all[0].ID != "B-10" || all[1].ID != "B-2" || all[2].ID != "B-1" {
			t.Fatalf("order: %+v", all)
		}
		closed, err := tx.ListBriefs("closed")
		if err != nil {
			return err
		}
		if len(closed) != 2 || closed[0].ID != "B-2" || closed[1].ID != "B-1" {
			t.Fatalf("closed: %+v", closed)
		}
		none, err := tx.ListBriefs("nope")
		if err != nil || len(none) != 0 {
			t.Fatalf("none: %v %v", none, err)
		}
		return nil
	})
}

func scenarioPlans(t *testing.T, e env) {
	created := time.UnixMilli(1700000000000).UTC()
	decided := created.Add(time.Hour)
	p1 := &model.Plan{ID: "P-1", BriefID: "B-1", Status: model.PlanProposed, ProposedBy: "rex", CreatedAt: created}
	p2 := &model.Plan{ID: "P-2", BriefID: "B-1", Status: model.PlanApproved, ProposedBy: "rex", Note: ptr("ok"),
		CreatedAt: created.Add(time.Second), DecidedAt: &decided}
	p3 := &model.Plan{ID: "P-3", BriefID: "B-2", Status: model.PlanProposed, ProposedBy: "rex", Note: ptr(""), CreatedAt: created}
	write(t, e.s, func(tx store.Tx) error {
		for _, p := range []*model.Plan{p1, p2, p3} {
			if err := tx.InsertPlan(p); err != nil {
				return err
			}
		}
		return nil
	})
	p3.Note = nil
	wantErr(t, store.ErrDuplicate, e.s.Atomic(context.Background(), func(tx store.Tx) error { return tx.InsertPlan(p1) }))
	p1.Status, p1.Note, p1.DecidedAt = model.PlanRejected, ptr("no"), &decided
	write(t, e.s, func(tx store.Tx) error { return tx.UpdatePlan(p1) })
	wantErr(t, store.ErrNotFound, e.s.Atomic(context.Background(), func(tx store.Tx) error {
		return tx.UpdatePlan(&model.Plan{ID: "P-404", CreatedAt: created})
	}))
	read(t, e.s, func(tx store.Tx) error {
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
			got, err := tx.ListPlans(c.brief, c.status)
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
	x.Tests = &model.TestResult{Passed: 10, Failed: 1, ReportedAt: hb}
	x.BlockedReason = ptr("budget_exceeded")
	return x
}

func scenarioTaskRoundTrip(t *testing.T, e env) {
	full, minimal := taskFull("T-1"), taskMin("T-2", "B-1")
	zeroTests := taskMin("T-3", "B-1")
	zeroTests.Tests = &model.TestResult{ReportedAt: time.UnixMilli(5).UTC()}
	nilSlices := taskMin("T-4", "B-1")
	nilSlices.AcceptanceCriteria, nilSlices.DependsOn = nil, nil
	write(t, e.s, func(tx store.Tx) error {
		for _, x := range []*model.Task{full, minimal, zeroTests, nilSlices} {
			if err := tx.InsertTask(x); err != nil {
				return err
			}
		}
		return nil
	})
	nilSlices.AcceptanceCriteria, nilSlices.DependsOn = []string{}, []string{}
	wantErr(t, store.ErrDuplicate, e.s.Atomic(context.Background(), func(tx store.Tx) error {
		return tx.InsertTask(taskMin("T-1", "B-1"))
	}))
	read(t, e.s, func(tx store.Tx) error {
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
	write(t, e.s, func(tx store.Tx) error { return tx.UpdateTask(updated) })
	write(t, e.s, func(tx store.Tx) error { return tx.UpdateTask(nilSlices) })
	read(t, e.s, func(tx store.Tx) error {
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
	wantErr(t, store.ErrNotFound, e.s.Atomic(context.Background(), func(tx store.Tx) error {
		return tx.UpdateTask(taskMin("T-404", "B-1"))
	}))
}

func scenarioTaskList(t *testing.T, e env) {
	write(t, e.s, func(tx store.Tx) error {
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
		{"all", store.TaskFilter{}, []string{"T-1", "T-2", "T-3", "T-10", "T-11"}},
		{"brief", store.TaskFilter{BriefID: "B-1"}, []string{"T-1", "T-2", "T-3", "T-10"}},
		{"plan", store.TaskFilter{PlanID: "P-2"}, []string{"T-11"}},
		{"status in", store.TaskFilter{Status: []string{model.StatusInProgress, model.StatusAssigned}}, []string{"T-1", "T-2"}},
		{"assignee", store.TaskFilter{Assignee: "a1"}, []string{"T-1", "T-2"}},
		{"assignee+role", store.TaskFilter{Assignee: "a1", Role: model.RoleTester}, []string{"T-2"}},
		{"reviewer", store.TaskFilter{Reviewer: "r1"}, []string{"T-11"}},
		{"limit", store.TaskFilter{Limit: 2}, []string{"T-1", "T-2"}},
		{"none", store.TaskFilter{BriefID: "B-9"}, []string{}},
	}
	read(t, e.s, func(tx store.Tx) error {
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
		if c, err := tx.BriefCost("B-1"); err != nil || c != 13 {
			t.Errorf("brief cost B-1: %v %v", c, err)
		}
		if c, err := tx.BriefCost("B-404"); err != nil || c != 0 {
			t.Errorf("brief cost empty: %v %v", c, err)
		}
		return nil
	})
}

func scenarioExtendLeases(t *testing.T, e env) {
	base := time.UnixMilli(1_700_000_000_000).UTC()
	lease := base.Add(10 * time.Minute)
	mk := func(id, status, assignee string) *model.Task {
		x := taskMin(id, "B-1")
		x.Status, x.Assignee, x.LastHeartbeatAt, x.LeaseExpiresAt = status, ptr(assignee), &base, &lease
		return x
	}
	write(t, e.s, func(tx store.Tx) error {
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
	write(t, e.s, func(tx store.Tx) error {
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
	read(t, e.s, func(tx store.Tx) error {
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

func scenarioAgents(t *testing.T, e env) {
	created := time.UnixMilli(1700000000123).UTC()
	seen := time.UnixMilli(1700000005000).UTC()
	full := &model.Agent{ID: "claude-1", Role: "worker", Kind: "claude-code", Model: "m", Provider: "anthropic",
		Status: "paused", PauseReason: ptr("loop"), LastSeenAt: &seen, CreatedAt: created, Spawned: true, TokenHash: "h1"}
	bare := &model.Agent{ID: "rex", Role: "leader", Kind: "other", Model: "m2", Provider: "openai",
		Status: "active", CreatedAt: created, TokenHash: "h2"}
	write(t, e.s, func(tx store.Tx) error {
		if err := tx.InsertAgent(full); err != nil {
			return err
		}
		return tx.InsertAgent(bare)
	})
	for _, a := range []*model.Agent{
		{ID: "rex", Role: "worker", Status: "active", TokenHash: "other"},
		{ID: "new", Role: "worker", Status: "active", TokenHash: "h1"},
	} {
		wantErr(t, store.ErrDuplicate, e.s.Atomic(context.Background(), func(tx store.Tx) error { return tx.InsertAgent(a) }))
	}
	read(t, e.s, func(tx store.Tx) error {
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

	err := e.s.Atomic(context.Background(), func(tx store.Tx) error {
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

func scenarioAgentComputed(t *testing.T, e env) {
	write(t, e.s, func(tx store.Tx) error {
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
	read(t, e.s, func(tx store.Tx) error {
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
	write(t, e.s, func(tx store.Tx) error {
		if err := tx.TouchAgents(map[string]int64{"w-1": 5000, "rex": 7000, "unknown": 1}); err != nil {
			return err
		}
		if err := tx.TouchAgents(nil); err != nil {
			return err
		}
		return tx.TouchAgents(map[string]int64{"w-1": 4000, "rex": 9000})
	})
	read(t, e.s, func(tx store.Tx) error {
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

func scenarioMessages(t *testing.T, e env) {
	base := time.Date(2026, 11, 20, 21, 4, 5, 123_000_000, time.UTC)
	var ids []int64
	write(t, e.s, func(tx store.Tx) error {
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
	read(t, e.s, func(tx store.Tx) error {
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

func scenarioQuestions(t *testing.T, e env) {
	base := time.Date(2026, 11, 20, 21, 4, 5, 0, time.UTC)
	full := &model.Question{ID: "Q-1", BriefID: ptr("B-1"), TaskID: ptr("T-3"), AgentID: "claude-1", Question: "which?",
		Options: []string{"a", "b"}, Status: "open", CreatedAt: base}
	bare := &model.Question{ID: "Q-2", AgentID: "claude-2", Question: "why?", Status: "open", CreatedAt: base.Add(time.Second)}
	early := &model.Question{ID: "Q-3", AgentID: "x", Question: "early", Status: "open", CreatedAt: base.Add(-time.Hour)}
	write(t, e.s, func(tx store.Tx) error {
		for _, q := range []*model.Question{full, bare, early} {
			if err := tx.InsertQuestion(q); err != nil {
				return err
			}
		}
		return nil
	})
	bare.Options = []string{}
	wantErr(t, store.ErrDuplicate, e.s.Atomic(context.Background(), func(tx store.Tx) error { return tx.InsertQuestion(full) }))
	at := base.Add(time.Minute)
	full.Status, full.Answer, full.AnsweredAt = "answered", ptr("a"), &at
	write(t, e.s, func(tx store.Tx) error { return tx.UpdateQuestion(full) })
	wantErr(t, store.ErrNotFound, e.s.Atomic(context.Background(), func(tx store.Tx) error {
		return tx.UpdateQuestion(&model.Question{ID: "Q-404", AgentID: "x", Status: "open", CreatedAt: base})
	}))
	read(t, e.s, func(tx store.Tx) error {
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
			got, err := tx.ListQuestions(status)
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

func scenarioAudit(t *testing.T, e env) {
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
	write(t, e.s, func(tx store.Tx) error {
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
		read(t, e.s, func(tx store.Tx) (err error) { out, err = tx.ListAudit(f); return })
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
	for _, stmt := range []string{
		`UPDATE audit SET note='tampered'`,
		`DELETE FROM audit WHERE id=1`,
		`DELETE FROM audit`,
	} {
		if err := e.exec(stmt); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("%q: want append-only failure, got %v", stmt, err)
		}
	}
	same(t, []*model.AuditEvent{full}, list(store.AuditFilter{Limit: 1}))
}

func scenarioSpawnsKV(t *testing.T, e env) {
	ts := time.Date(2026, 11, 20, 21, 0, 0, 0, time.UTC)
	sp1 := &model.Spawn{ID: "S-1", BriefID: "B-1", AgentID: "claude-b1-1", Kind: "claude-code", Model: "m", Provider: "anthropic",
		Status: "requested", Spawner: "noop", RequestedBy: "rex", CreatedAt: ts}
	sp2 := &model.Spawn{ID: "S-2", BriefID: "B-2", AgentID: "codex-b2-1", Kind: "codex", Model: "m2", Provider: "openai",
		Status: "failed", Spawner: "docker", Error: ptr("not implemented"), RequestedBy: "rex", CreatedAt: ts}
	sp10 := &model.Spawn{ID: "S-10", BriefID: "B-1", AgentID: "x", Kind: "opencode", Model: "m3", Provider: "deepseek",
		Status: "requested", Spawner: "noop", RequestedBy: "rex", CreatedAt: ts}
	write(t, e.s, func(tx store.Tx) error {
		for _, sp := range []*model.Spawn{sp10, sp2, sp1} {
			if err := tx.InsertSpawn(sp); err != nil {
				return err
			}
		}
		return nil
	})
	wantErr(t, store.ErrDuplicate, e.s.Atomic(context.Background(), func(tx store.Tx) error { return tx.InsertSpawn(sp1) }))
	sp1.Status, sp1.Error = "failed", ptr("boom")
	write(t, e.s, func(tx store.Tx) error { return tx.UpdateSpawn(sp1) })
	sp2.Status, sp2.Error = "spawned", nil
	write(t, e.s, func(tx store.Tx) error { return tx.UpdateSpawn(sp2) })
	wantErr(t, store.ErrNotFound, e.s.Atomic(context.Background(), func(tx store.Tx) error {
		return tx.UpdateSpawn(&model.Spawn{ID: "S-99"})
	}))
	read(t, e.s, func(tx store.Tx) error {
		all, err := tx.ListSpawns("")
		if err != nil {
			return err
		}
		same(t, []*model.Spawn{sp1, sp2, sp10}, all)
		b1, err := tx.ListSpawns("B-1")
		if err != nil {
			return err
		}
		same(t, []*model.Spawn{sp1, sp10}, b1)
		if none, err := tx.ListSpawns("B-none"); err != nil || none == nil || len(none) != 0 {
			t.Fatalf("empty: %#v %v", none, err)
		}
		if v, ok, err := tx.GetKV("missing"); err != nil || ok || v != "" {
			t.Fatalf("missing kv: %q %v %v", v, ok, err)
		}
		return nil
	})
	write(t, e.s, func(tx store.Tx) error {
		if err := tx.SetKV("charter", "one"); err != nil {
			return err
		}
		if err := tx.SetKV("paused", ""); err != nil {
			return err
		}
		return tx.SetKV("charter", "two")
	})
	read(t, e.s, func(tx store.Tx) error {
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

func scenarioAtomic(t *testing.T, e env) {
	ctx := context.Background()
	sentinel := errors.New("nope")
	err := e.s.Atomic(ctx, func(tx store.Tx) error {
		if _, err := tx.NextID("R"); err != nil {
			return err
		}
		if err := tx.SetKV("k", "v"); err != nil {
			return err
		}
		return sentinel
	})
	wantErr(t, sentinel, err)
	err = e.s.Atomic(ctx, func(tx store.Tx) error {
		if _, err := tx.NextID("R"); err != nil {
			return err
		}
		panic("kaboom")
	})
	if err == nil || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("panic err = %v", err)
	}
	if id := nextID(t, e.s, "R"); id != "R-1" {
		t.Fatalf("id = %s, want R-1 (failed ops rolled back)", id)
	}
	read(t, e.s, func(tx store.Tx) error {
		if _, ok, err := tx.GetKV("k"); err != nil || ok {
			t.Fatalf("kv survived rollback: %v %v", ok, err)
		}
		return nil
	})
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	wantErr(t, context.Canceled, e.s.Atomic(cctx, func(tx store.Tx) error { _, err := tx.NextID("R"); return err }))
	if id := nextID(t, e.s, "R"); id != "R-2" {
		t.Fatalf("id = %s, want R-2 (cancelled op must not run)", id)
	}
}

func scenarioConcurrent(t *testing.T, e env) {
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
				err := e.s.Atomic(context.Background(), func(tx store.Tx) error {
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
	read(t, e.s, func(tx store.Tx) error {
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
