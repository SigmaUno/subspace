package sqlite_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

const fixtureGlob = "testdata/fixtures/*.db"

var fixtureRows = map[string]int{
	"briefs": 2, "plans": 2, "tasks": 13, "agents": 7, "messages": 12, "questions": 3, "audit": 16, "spawns": 3,
}

var fixtureTaskStatuses = map[string]int{
	"backlog": 2, "ready": 1, "assigned": 1, "in_progress": 1, "submitted": 1, "in_review": 1,
	"changes_requested": 1, "approved": 1, "done": 1, "blocked": 1, "needs_owner": 1, "cancelled": 1,
}

var fixtureBoards = map[string]map[string]int{
	"B-1": {
		"ready": 1, "assigned": 1, "in_progress": 1, "submitted": 1, "in_review": 1,
		"changes_requested": 1, "approved": 1, "done": 1, "blocked": 1, "needs_owner": 1, "cancelled": 1,
	},
	"B-2": {"backlog": 2},
}

var upgradedColumns = []string{
	"briefs.cross_review", "briefs.auto_done_on_merge", "plans.warnings", "tasks.priority", "tasks.component_id",
	"tasks.lease_expiries", "agents.token_expires_at", "audit.hash",
}

var fixtureNow = time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)

func fixturePaths(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(fixtureGlob)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) < 6 {
		t.Fatalf("found %d fixtures, want at least 6", len(paths))
	}
	return paths
}

func copyFixture(t *testing.T, src string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 200<<10 {
		t.Fatalf("%s is %d bytes, limit is 200 KiB", src, len(data))
	}
	dst := filepath.Join(t.TempDir(), filepath.Base(src))
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dst
}

func rawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func tableColumns(t *testing.T, path string) map[string][]string {
	t.Helper()
	db := rawDB(t, path)
	rows, err := db.Query(`SELECT m.name, p.name FROM sqlite_master m, pragma_table_info(m.name) p
		WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%' ORDER BY m.name, p.cid`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]string{}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatal(err)
		}
		out[table] = append(out[table], column)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, cols := range out {
		sort.Strings(cols)
	}
	return out
}

func columnDefault(t *testing.T, db *sql.DB, table, column string) sql.NullString {
	t.Helper()
	var dflt sql.NullString
	err := db.QueryRow(`SELECT dflt_value FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&dflt)
	if err != nil {
		t.Fatal(err)
	}
	return dflt
}

func rowCounts(t *testing.T, path string) map[string]int {
	t.Helper()
	db := rawDB(t, path)
	out := map[string]int{}
	for table := range fixtureRows {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[table] = n
	}
	return out
}

func snapshot(t *testing.T, path string) string {
	t.Helper()
	db := rawDB(t, path)
	h := sha256.New()
	tables := map[string]bool{}
	rows, err := db.Query(`SELECT type, name, coalesce(sql, '') FROM sqlite_master ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var typ, name, ddl string
		if err := rows.Scan(&typ, &name, &ddl); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		_, _ = fmt.Fprintf(h, "%s|%s|%s\n", typ, name, ddl)
		if typ == "table" {
			tables[name] = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	names := make([]string, 0, len(tables))
	for n := range tables {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		rows, err := db.Query(`SELECT * FROM "` + name + `" ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			_, _ = fmt.Fprintf(h, "%s|%v\n", name, vals)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
	}
	return hex.EncodeToString(h.Sum(nil))
}

func openClose(t *testing.T, path string, fn func(store.Tx)) {
	t.Helper()
	s, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if fn != nil {
		if err := s.View(context.Background(), func(tx store.Tx) error { fn(tx); return nil }); err != nil {
			_ = s.Close()
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func tokenHash(id string) string {
	h := sha256.Sum256([]byte("fixture-token-" + id))
	return hex.EncodeToString(h[:])
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestUpgradeFixtures(t *testing.T) {
	freshColumns := tableColumns(t, func() string {
		p := filepath.Join(t.TempDir(), "fresh.db")
		openClose(t, p, nil)
		return p
	}())
	missingSeen := map[string]bool{}

	for _, src := range fixturePaths(t) {
		t.Run(strings.TrimSuffix(filepath.Base(src), ".db"), func(t *testing.T) {
			path := copyFixture(t, src)
			oldColumns := tableColumns(t, path)
			if got := rowCounts(t, path); !reflect.DeepEqual(got, fixtureRows) {
				t.Fatalf("fixture rows %v, want %v", got, fixtureRows)
			}

			openClose(t, path, func(tx store.Tx) { checkFixtureData(t, tx) })

			if got := rowCounts(t, path); !reflect.DeepEqual(got, fixtureRows) {
				t.Fatalf("rows after migration %v, want %v", got, fixtureRows)
			}
			newColumns := tableColumns(t, path)
			if !reflect.DeepEqual(newColumns, freshColumns) {
				t.Fatalf("migrated schema differs from a fresh database:\n got %v\nwant %v", newColumns, freshColumns)
			}

			db := rawDB(t, path)
			for table, cols := range newColumns {
				if _, existed := oldColumns[table]; !existed {
					continue
				}
				for _, col := range cols {
					if contains(oldColumns[table], col) {
						continue
					}
					missingSeen[table+"."+col] = true
					if table == "audit" && col == "hash" {
						continue
					}
					dflt := columnDefault(t, db, table, col)
					if !dflt.Valid {
						var notNull int
						if err := db.QueryRow(`SELECT "notnull" FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&notNull); err != nil {
							t.Fatal(err)
						}
						if notNull == 0 {
							var nonNull int
							if err := db.QueryRow(fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s IS NOT NULL`, table, col)).Scan(&nonNull); err != nil {
								t.Fatal(err)
							}
							if nonNull != 0 {
								t.Fatalf("%s.%s is nullable without default but old rows got values", table, col)
							}
							continue
						}
						t.Fatalf("%s.%s is not null and has no default", table, col)
					}
					var bad int
					q := fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s IS NOT %s`, table, col, dflt.String)
					if err := db.QueryRow(q).Scan(&bad); err != nil {
						t.Fatal(err)
					}
					if bad != 0 {
						t.Fatalf("%d rows of %s.%s differ from default %s", bad, table, col, dflt.String)
					}
				}
			}

			first := snapshot(t, path)
			openClose(t, path, func(tx store.Tx) {
				check := must(tx.VerifyAudit())
				if !check.OK || check.Events != int64(fixtureRows["audit"]) {
					t.Fatalf("audit chain after second open: %+v", check)
				}
			})
			if second := snapshot(t, path); second != first {
				t.Fatal("second Open changed the database")
			}
		})
	}

	for _, c := range upgradedColumns {
		if !missingSeen[c] {
			t.Errorf("no fixture predates column %s", c)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func checkFixtureData(t *testing.T, tx store.Tx) {
	t.Helper()

	b1 := must(tx.GetBrief("B-1"))
	if b1.Title != "Webhooks: PR status sync" || b1.Status != "open" || b1.PlanStatus != "approved" ||
		!reflect.DeepEqual(b1.Repos, []string{"SigmaUno/subspace"}) ||
		!reflect.DeepEqual(b1.AllowedProviders, []string{"anthropic", "openai"}) ||
		b1.Budget != (model.BriefBudget{MaxCostEUR: 120, MaxParallelAgent: 8}) || !b1.CrossReview {
		t.Fatalf("brief B-1: %+v", b1)
	}
	b2 := must(tx.GetBrief("B-2"))
	if b2.Status != "closed" || b2.PlanStatus != "proposed" || b2.Budget.MaxCostEUR != 15.5 || len(b2.Repos) != 2 {
		t.Fatalf("brief B-2: %+v", b2)
	}

	p1 := must(tx.GetPlan("P-1"))
	if p1.Status != "approved" || p1.BriefID != "B-1" || p1.ProposedBy != "lead-1" || p1.Note == nil || *p1.Note != "lgtm" || p1.DecidedAt == nil {
		t.Fatalf("plan P-1: %+v", p1)
	}
	if p2 := must(tx.GetPlan("P-2")); p2.Status != "proposed" || p2.DecidedAt != nil {
		t.Fatalf("plan P-2: %+v", p2)
	}

	counts := must(tx.CountTasksByStatus())
	if !reflect.DeepEqual(counts, fixtureTaskStatuses) {
		t.Fatalf("task statuses %v, want %v", counts, fixtureTaskStatuses)
	}
	t1 := must(tx.GetTask("T-1"))
	if t1.Status != "done" || t1.Assignee == nil || *t1.Assignee != "worker-1" || t1.Reviewer == nil || *t1.Reviewer != "reviewer-1" ||
		t1.PRURL == nil || !strings.HasSuffix(*t1.PRURL, "/pull/12") || t1.ReviewRounds != 1 || t1.Branch != "sub/T-1-webhook-handler" ||
		t1.Tests == nil || t1.Tests.Passed != 12 || t1.Tests.Failed != 0 || !reflect.DeepEqual(t1.AcceptanceCriteria, []string{"tests pass", "PR opened against main"}) {
		t.Fatalf("task T-1: %+v", t1)
	}
	if t2 := must(tx.GetTask("T-2")); !reflect.DeepEqual(t2.DependsOn, []string{"T-1"}) || t2.Status != "approved" {
		t.Fatalf("task T-2: %+v", t2)
	}
	t6 := must(tx.GetTask("T-6"))
	if t6.Status != "in_progress" || t6.Assignee == nil || *t6.Assignee != "worker-2" || t6.ProgressPct != 40 || t6.MinutesUsed != 12 ||
		t6.CostUsed != 0.4 || t6.Budget != (model.Budget{MaxMinutes: 120, MaxCostEUR: 5}) || t6.Tests != nil ||
		t6.LeaseExpiresAt == nil || !t6.LeaseExpiresAt.Equal(fixtureNow.Add(15*time.Minute)) {
		t.Fatalf("task T-6: %+v", t6)
	}
	if t9 := must(tx.GetTask("T-9")); t9.Status != "blocked" || t9.BlockedReason == nil || *t9.BlockedReason != "budget_exceeded" {
		t.Fatalf("task T-9: %+v", t9)
	}
	if t10 := must(tx.GetTask("T-10")); t10.Status != "needs_owner" || t10.ReviewRounds != 3 {
		t.Fatalf("task T-10: %+v", t10)
	}
	if t8 := must(tx.GetTask("T-8")); t8.Role != model.RoleTester || t8.Assignee != nil {
		t.Fatalf("task T-8: %+v", t8)
	}
	if got := must(tx.ListTasks(store.TaskFilter{BriefID: "B-2"})); len(got) != 2 {
		t.Fatalf("B-2 tasks: %d", len(got))
	}

	wantAgents := map[string]string{
		"lead-1": "active", "worker-1": "active", "worker-2": "active", "tester-1": "active",
		"reviewer-1": "active", "worker-3": "paused", "worker-4": "revoked",
	}
	for id, status := range wantAgents {
		a := must(tx.GetAgentByTokenHash(tokenHash(id)))
		if a.ID != id || a.Status != status || a.TokenHash != tokenHash(id) {
			t.Fatalf("agent %s: %+v", id, a)
		}
	}
	if a := must(tx.GetAgent("worker-3")); a.PauseReason == nil || *a.PauseReason != "loop" {
		t.Fatalf("agent worker-3: %+v", a)
	}
	if a := must(tx.GetAgent("worker-2")); !a.Spawned || a.Provider != "openai" || a.Kind != model.KindCodex {
		t.Fatalf("agent worker-2: %+v", a)
	}
	if a := must(tx.GetAgent("lead-1")); a.Spawned || a.TokenExpiresAt != nil || a.Role != model.RoleLeader {
		t.Fatalf("agent lead-1: %+v", a)
	}

	for thread, want := range map[[2]string]int{{"task", "T-1"}: 3, {"brief", "B-1"}: 3, {"agent", "worker-3"}: 1, {"task", "T-6"}: 1} {
		if got := must(tx.ListMessages(thread[0], thread[1], 0, 100)); len(got) != want {
			t.Fatalf("thread %v: %d messages, want %d", thread, len(got), want)
		}
	}
	msgs := must(tx.ListMessages("task", "T-1", 0, 100))
	if msgs[0].Kind != "status" || msgs[2].AuthorID != "reviewer-1" || !strings.HasPrefix(msgs[2].Body, "Approved.") {
		t.Fatalf("T-1 thread: %+v", msgs)
	}

	q1 := must(tx.GetQuestion("Q-1"))
	if q1.Status != "open" || q1.Answer != nil || !reflect.DeepEqual(q1.Options, []string{"X-Gitea-Delivery", "X-Forgejo-Delivery"}) {
		t.Fatalf("question Q-1: %+v", q1)
	}
	q2 := must(tx.GetQuestion("Q-2"))
	if q2.Status != "answered" || q2.Answer == nil || *q2.Answer != "202" || q2.AnsweredAt == nil || q2.TaskID == nil || *q2.TaskID != "T-4" {
		t.Fatalf("question Q-2: %+v", q2)
	}
	if q3 := must(tx.GetQuestion("Q-3")); q3.TaskID != nil || q3.AgentID != "lead-1" {
		t.Fatalf("question Q-3: %+v", q3)
	}

	events := must(tx.ListAudit(store.AuditFilter{}))
	if len(events) != fixtureRows["audit"] {
		t.Fatalf("audit events: %d", len(events))
	}
	for i, e := range events {
		if e.ID != int64(i+1) || e.Hash == "" {
			t.Fatalf("audit event %d: %+v", i, e)
		}
	}
	if e := events[0]; e.Tool != "create_brief" || e.AgentID != "owner" || e.After["id"] != "B-1" || e.TaskID != nil {
		t.Fatalf("audit first: %+v", e)
	}
	if e := events[11]; e.Tool != "done" || e.TaskID == nil || *e.TaskID != "T-1" || e.Note == nil || *e.Note != "merged in #12" ||
		e.Before["status"] != "approved" || e.After["status"] != "done" {
		t.Fatalf("audit done: %+v", e)
	}
	check := must(tx.VerifyAudit())
	if !check.OK || check.Events != int64(len(events)) || check.Head.Hash != events[len(events)-1].Hash {
		t.Fatalf("audit chain: %+v", check)
	}

	spawns := must(tx.ListSpawns(store.SpawnFilter{}))
	if len(spawns) != 3 {
		t.Fatalf("spawns: %d", len(spawns))
	}
	byID := map[string]*model.Spawn{}
	for _, s := range spawns {
		byID[s.ID] = s
	}
	if s := byID["S-2"]; s == nil || s.Status != "failed" || s.Error == nil || *s.Error != "not implemented" || s.AgentID != "worker-4" {
		t.Fatalf("spawn S-2: %+v", s)
	}
	if s := byID["S-1"]; s == nil || s.Status != "spawned" || s.Spawner != "noop" || s.RequestedBy != "lead-1" {
		t.Fatalf("spawn S-1: %+v", s)
	}

	next := must(tx.NextID("T"))
	if next != "T-14" {
		t.Fatalf("next task id %s, want T-14", next)
	}
}

func TestUpgradeFixturesServe(t *testing.T) {
	for _, src := range fixturePaths(t) {
		t.Run(strings.TrimSuffix(filepath.Base(src), ".db"), func(t *testing.T) {
			env := servertest.Start(t, server.Config{
				PublicURL:  "http://sub.test",
				DBPath:     copyFixture(t, src),
				FlushEvery: 50 * time.Millisecond,
				SweepEvery: 50 * time.Millisecond,
				Lease:      time.Minute,
				Now:        func() time.Time { return fixtureNow },
			})

			code, state := env.REST("GET", "/api/v1/state", nil)
			if code != 200 {
				t.Fatalf("state: %d %v", code, state)
			}
			counts, _ := state["counts"].(map[string]any)
			if counts["agents"] != float64(7) || counts["agents_active"] != float64(5) ||
				counts["open_questions"] != float64(2) || counts["proposed_plans"] != float64(1) {
				t.Fatalf("state counts: %v", counts)
			}
			if got := numberMap(counts["tasks"]); !reflect.DeepEqual(got, fixtureTaskStatuses) {
				t.Fatalf("state tasks %v, want %v", got, fixtureTaskStatuses)
			}

			for brief, want := range fixtureBoards {
				code, board := env.REST("GET", "/api/v1/briefs/"+brief+"/board", nil)
				if code != 200 {
					t.Fatalf("board %s: %d %v", brief, code, board)
				}
				columns, _ := board["columns"].(map[string]any)
				if len(columns) != len(model.AllStatuses) {
					t.Fatalf("board %s has %d columns", brief, len(columns))
				}
				got := map[string]int{}
				for status, v := range columns {
					if n := len(v.([]any)); n > 0 {
						got[status] = n
					}
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("board %s %v, want %v", brief, got, want)
				}
			}
		})
	}
}

func numberMap(v any) map[string]int {
	out := map[string]int{}
	m, _ := v.(map[string]any)
	for k, n := range m {
		if f, _ := n.(float64); f > 0 {
			out[k] = int(f)
		}
	}
	return out
}

func TestUpgradeFixturesSchemaVersionGuard(t *testing.T) {
	for _, src := range fixturePaths(t) {
		t.Run(strings.TrimSuffix(filepath.Base(src), ".db"), func(t *testing.T) {
			path := copyFixture(t, src)
			openClose(t, path, nil)
			db := rawDB(t, path)
			var got store.SchemaVersion
			if err := db.QueryRow(`SELECT version, hash FROM schema_version WHERE id = 1`).Scan(&got.Version, &got.Hash); err != nil {
				t.Fatal(err)
			}
			if got != sqlite.BinarySchema() {
				t.Fatalf("schema_version %+v, binary %+v", got, sqlite.BinarySchema())
			}
			if _, err := db.Exec(`UPDATE schema_version SET version = version + 1`); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before := snapshot(t, path)

			if s, err := sqlite.Open(path); !errors.Is(err, store.ErrSchemaNewer) {
				if s != nil {
					_ = s.Close()
				}
				t.Fatalf("opened a newer schema: %v", err)
			}
			cfg := server.Config{PublicURL: "http://sub.test", DBPath: path, OwnerToken: servertest.OwnerToken, Now: func() time.Time { return fixtureNow }}
			if s, err := server.New(context.Background(), cfg); !errors.Is(err, store.ErrSchemaNewer) {
				if s != nil {
					_ = s.Close()
				}
				t.Fatalf("server started against a newer schema: %v", err)
			}

			cfg.SchemaReadOnly = true
			env := servertest.Start(t, cfg)
			if code, _ := env.REST("GET", "/readyz", nil); code != http.StatusServiceUnavailable {
				t.Fatalf("read-only readyz %d", code)
			}
			if code, state := env.REST("GET", "/api/v1/state", nil); code != http.StatusOK || numberMap(state["counts"].(map[string]any)["tasks"])["done"] != 1 {
				t.Fatalf("read-only state: %d %v", code, state)
			}
			if code, _ := env.REST("POST", "/api/v1/briefs", map[string]any{"title": "nope", "repos": []string{"o/r"}}); code < 400 {
				t.Fatalf("read-only server accepted a write: %d", code)
			}
			env.Stop()
			if after := snapshot(t, path); after != before {
				t.Fatal("read-only open changed the database")
			}
		})
	}
}
