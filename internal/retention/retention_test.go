package retention_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/idempotency"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/retention"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/testdb"
	"github.com/SigmaUno/subspace/internal/testmain"
	"github.com/SigmaUno/subspace/internal/testmode"
)

func TestMain(m *testing.M) { testmain.Run(m) }

var (
	t0       = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	day      = 24 * time.Hour
	errCrash = errors.New("simulated crash")
	tables   = map[string]string{
		"audit": "id", "messages": "id", "usage_records": "key", "idempotency_keys": "agent_id, key",
	}
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

type sqlTx interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
}

func openStore(t *testing.T, path string) store.Store {
	t.Helper()
	return testdb.Open(t, path)
}

func newService(t *testing.T, st store.Store, dir string, clk *clock, mut func(*retention.Policy)) *retention.Service {
	t.Helper()
	pol := retention.DefaultPolicy()
	pol.AuditDays, pol.MessagesDays, pol.UsageDays, pol.IdempotencyDays = 30, 30, 30, 30
	if mut != nil {
		mut(&pol)
	}
	svc, err := retention.New(retention.Options{Store: st, Policy: pol, Dir: dir, Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func seed(t *testing.T, st store.Store, r *rand.Rand, n int, body int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		ts := t0.Add(time.Duration(i) * 60 * day / time.Duration(n))
		note := fmt.Sprintf("note %d %d", i, r.Int63())
		err := st.Atomic(ctx, func(tx store.Tx) error {
			if err := tx.InsertAudit(&model.AuditEvent{TS: ts, AgentID: fmt.Sprint("a", r.Intn(5)), Role: "worker", Tool: "update_status",
				Note: &note, Before: map[string]any{"n": i}, After: map[string]any{"n": i + 1, "s": strings.Repeat("x", r.Intn(40))}}); err != nil {
				return err
			}
			if err := tx.InsertMessage(&model.Message{ThreadKind: "task", ThreadID: fmt.Sprint("T-", r.Intn(9)), AuthorID: "a",
				AuthorRole: "worker", Kind: "message", Body: strings.Repeat("m", body+r.Intn(20)), CreatedAt: ts}); err != nil {
				return err
			}
			return tx.(store.TimeseriesTx).PutUsage(store.UsageRecord{Key: fmt.Sprintf("u-%05d", i), TS: ts, AgentID: "a",
				TokensIn: r.Int63n(1 << 40), TokensOut: int64(i)})
		})
		if err != nil {
			t.Fatal(err)
		}
		rec := &idempotency.Record{Tool: "add_note", ArgsHash: fmt.Sprint(r.Int63()), Result: []byte(`{"ok":true}`), ExpiresAt: t0.Add(1000 * day)}
		if err := st.(idempotency.Store).PutIdempotency(ctx, fmt.Sprint("a", i%3), fmt.Sprint("k", i), rec, ts); err != nil {
			t.Fatal(err)
		}
	}
}

func query(t *testing.T, st store.Store, q string, args ...any) [][]any {
	t.Helper()
	var out [][]any
	err := st.View(context.Background(), func(tx store.Tx) error {
		rows, err := tx.(sqlTx).Query(q, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return err
			}
			out = append(out, vals)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func count(t *testing.T, st store.Store, q string, args ...any) int64 {
	t.Helper()
	return query(t, st, q, args...)[0][0].(int64)
}

func dump(t *testing.T, st store.Store, maxAudit int64) map[string]string {
	t.Helper()
	out := map[string]string{}
	for table, keys := range tables {
		q := `SELECT * FROM ` + table + ` ORDER BY ` + keys
		if table == "audit" {
			q = fmt.Sprintf(`SELECT * FROM audit WHERE id <= %d ORDER BY id`, maxAudit)
		}
		out[table] = fmt.Sprint(query(t, st, q))
	}
	return out
}

func archived(t *testing.T, svc *retention.Service) map[retention.Kind]int64 {
	t.Helper()
	st, err := svc.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[retention.Kind]int64{}
	for _, s := range st.Segments {
		if s.Status != retention.StatusRestored {
			out[s.Kind] += s.Rows
		}
	}
	return out
}

func restoreAll(t *testing.T, svc *retention.Service) {
	t.Helper()
	ctx := context.Background()
	st, err := svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var audits []retention.Segment
	for _, s := range st.Segments {
		if s.Status == retention.StatusRestored {
			continue
		}
		if s.Kind == retention.Audit {
			audits = append(audits, s)
			continue
		}
		if _, err := svc.Restore(ctx, s.ID); err != nil {
			t.Fatal(err)
		}
	}
	if len(audits) > 1 {
		if _, err := svc.Restore(ctx, audits[0].ID); model.AsError(err).Code != "conflict" {
			t.Fatalf("restoring the oldest audit segment first: %v", err)
		}
	}
	for i := len(audits) - 1; i >= 0; i-- {
		if _, err := svc.Restore(ctx, audits[i].ID); err != nil {
			t.Fatal(err)
		}
	}
}

func mustVerify(t *testing.T, svc *retention.Service) *retention.Verification {
	t.Helper()
	v, err := svc.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !v.OK {
		t.Fatalf("verification failed: %+v", v)
	}
	return v
}

func TestCrashDuringArchivalResumesIdempotently(t *testing.T) {
	ctx := context.Background()
	for s := int64(1); s <= int64(testmode.Size(4, 10)); s++ {
		t.Run(fmt.Sprint("seed", s), func(t *testing.T) {
			r := rand.New(rand.NewSource(s))
			dir := t.TempDir()
			path, arch := testdb.Path(t, "s.db"), filepath.Join(dir, "retention")
			st := openStore(t, path)
			n := 120 + r.Intn(120)
			seed(t, st, r, n, 10)
			before := dump(t, st, int64(n))
			clk := &clock{t: t0.Add(60 * day)}
			pol := func(p *retention.Policy) { p.Batch, p.SegmentRows = 1+r.Intn(30), 10+r.Intn(60) }

			svc := newService(t, st, arch, clk, pol)
			failAt, calls := 1+r.Intn(40), 0
			svc.SetFault(func(string) error {
				calls++
				if calls == failAt {
					return errCrash
				}
				return nil
			})
			if _, err := svc.RunOnce(ctx); err != nil && !errors.Is(err, errCrash) {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}

			st = openStore(t, path)
			defer func() { _ = st.Close() }()
			svc = newService(t, st, arch, clk, pol)
			if _, err := svc.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			mustVerify(t, svc)
			cutoff := clk.Now().Add(-30 * day).UnixMilli()
			got := archived(t, svc)
			for k, q := range map[retention.Kind]string{
				retention.Audit: "audit WHERE id <= ? AND ts", retention.Messages: "messages WHERE ? > 0 AND created_at",
				retention.Usage: "usage_records WHERE ? > 0 AND ts", retention.Idempotency: "idempotency_keys WHERE ? > 0 AND created_at",
			} {
				if old := count(t, st, `SELECT count(*) FROM `+q+` < ?`, n, cutoff); old != 0 {
					t.Fatalf("%s: %d rows older than the cutoff left", k, old)
				}
				if live := count(t, st, `SELECT count(*) FROM `+q+` >= ?`, n, cutoff); live+got[k] != int64(n) {
					t.Fatalf("%s: live %d + archived %d != %d", k, live, got[k], n)
				}
			}

			restoreAll(t, svc)
			after := dump(t, st, int64(n))
			for table := range tables {
				if before[table] != after[table] {
					t.Fatalf("%s differs after restore", table)
				}
			}
			v := mustVerify(t, svc)
			if v.Segments != 0 || v.Chain.Events != count(t, st, `SELECT count(*) FROM audit`) {
				t.Fatalf("after restoring everything: %+v", v)
			}
		})
	}
}

func TestArchivalWithConcurrentWriters(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	st := openStore(t, testdb.Path(t, "s.db"))
	defer func() { _ = st.Close() }()
	seed(t, st, rand.New(rand.NewSource(7)), 400, 10)
	clk := &clock{t: t0.Add(60 * day)}
	svc := newService(t, st, filepath.Join(dir, "retention"), clk, func(p *retention.Policy) { p.Batch, p.SegmentRows = 25, 70 })

	var (
		wg       sync.WaitGroup
		messages atomic.Int64
	)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				err := st.Atomic(ctx, func(tx store.Tx) error {
					if err := tx.InsertAudit(&model.AuditEvent{TS: clk.Now(), AgentID: fmt.Sprint("w", w), Role: "worker", Tool: "heartbeat"}); err != nil {
						return err
					}
					return tx.InsertMessage(&model.Message{ThreadKind: "agent", ThreadID: "w", AuthorID: "w", AuthorRole: "worker",
						Kind: "message", Body: "live", CreatedAt: clk.Now()})
				})
				if err == nil {
					messages.Add(1)
				}
			}
		}()
	}
	for i := 0; i < 3; i++ {
		clk.Set(clk.Now().Add(5 * day))
		if _, err := svc.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	wg.Wait()

	mustVerify(t, svc)
	got := archived(t, svc)
	if got[retention.Audit] == 0 || got[retention.Messages] == 0 {
		t.Fatalf("nothing archived: %v", got)
	}
	if live, top := count(t, st, `SELECT count(*) FROM audit`), count(t, st, `SELECT max(id) FROM audit`); live+got[retention.Audit] != top {
		t.Fatalf("audit: live %d + archived %d != %d", live, got[retention.Audit], top)
	}
	if live := count(t, st, `SELECT count(*) FROM messages`); live+got[retention.Messages] != 400+messages.Load() {
		t.Fatalf("messages: live %d + archived %d != %d", live, got[retention.Messages], 400+messages.Load())
	}
}

func TestAppendOnlyOutsideArchivedSegments(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st := openStore(t, testdb.Path(t, "s.db"))
	defer func() { _ = st.Close() }()
	seed(t, st, rand.New(rand.NewSource(3)), 100, 10)
	exec := func(q string) error {
		return st.Atomic(ctx, func(tx store.Tx) error { _, err := tx.(sqlTx).Exec(q); return err })
	}
	if err := exec(`DELETE FROM audit WHERE id = 1`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("delete before archival: %v", err)
	}
	svc := newService(t, st, filepath.Join(dir, "retention"), &clock{t: t0.Add(60 * day)}, nil)
	if _, err := svc.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a := status.Anchor.ID
	if a == 0 {
		t.Fatal("no audit anchor after archival")
	}
	for _, q := range []string{
		fmt.Sprintf(`DELETE FROM audit WHERE id = %d`, a+1),
		`DELETE FROM audit`,
		fmt.Sprintf(`UPDATE audit SET note = 'x' WHERE id = %d`, a+1),
		`DELETE FROM retention_segments`,
		`UPDATE retention_segments SET last_id = last_id + 10`,
		`UPDATE retention_segments SET sha256 = 'x'`,
		`UPDATE retention_segments SET status = 'pending'`,
	} {
		if err := exec(q); err == nil {
			t.Fatalf("%s succeeded", q)
		}
	}
	mustVerify(t, svc)

	forged := fmt.Sprintf(`INSERT INTO retention_segments(kind, seq, file, row_count, bytes, sha256, cutoff, first_id, last_id, prev_hash,
		last_hash, status, created_at, updated_at) VALUES('audit', 99, 'audit/forged.ndjson.gz', 5, 1, 'x', 0, %d, %d, '', '', 'archived', 0, 0)`, a+1, a+5)
	if err := exec(forged); err != nil {
		t.Fatal(err)
	}
	if err := exec(fmt.Sprintf(`DELETE FROM audit WHERE id BETWEEN %d AND %d`, a+1, a+5)); err != nil {
		t.Fatal(err)
	}
	if v, err := svc.Verify(ctx); err != nil || v.OK || v.BrokenSegment == 0 {
		t.Fatalf("forged segment not detected: %+v %v", v, err)
	}
}

func TestCheckpointAndVacuumWhenIdle(t *testing.T) {
	if testdb.Postgres() {
		t.Skip("WAL checkpoint and vacuum are SQLite maintenance")
	}
	ctx := context.Background()
	dir := t.TempDir()
	st := openStore(t, testdb.Path(t, "s.db"))
	defer func() { _ = st.Close() }()
	seed(t, st, rand.New(rand.NewSource(5)), 600, 2000)
	svc := newService(t, st, filepath.Join(dir, "retention"), &clock{t: t0.Add(60 * day)}, nil)
	first, err := svc.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.Rows[retention.Messages] == 0 || first.Checkpointed {
		t.Fatalf("first run: %+v", first)
	}
	idle, err := svc.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !idle.Checkpointed || idle.VacuumedPages == 0 || len(idle.Archived) != 0 {
		t.Fatalf("idle run: %+v", idle)
	}
	if mode := query(t, st, `PRAGMA auto_vacuum`)[0][0].(int64); mode != 2 {
		t.Fatalf("auto_vacuum %d, want incremental", mode)
	}
	mustVerify(t, svc)
}
