package postgres_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/postgres"
	"github.com/SigmaUno/subspace/internal/store/storetest"
)

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

func TestStoreConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		s, _ := openPG(t)
		return s
	})
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
		AllowedProviders: []string{"anthropic", "deepseek"}, PlanStatus: "none", CreatedAt: created,
	}
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

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func nextID(t *testing.T, s store.Store, prefix string) string {
	t.Helper()
	var id string
	write(t, s, func(tx store.Tx) (err error) { id, err = tx.NextID(prefix); return })
	return id
}
