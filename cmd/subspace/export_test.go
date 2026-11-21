package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/idempotency"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
	"github.com/SigmaUno/subspace/internal/testmain"
	"github.com/SigmaUno/subspace/internal/testmode"
)

func TestMain(m *testing.M) { testmain.Run(m) }

func ptr[T any](v T) *T { return &v }

func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func populate(t *testing.T, path string) {
	t.Helper()
	st, err := sqlite.Open(path)
	mustDo(t, err)
	ctx := context.Background()
	at := time.Date(2026, 11, 20, 21, 0, 0, 0, time.UTC)
	mustDo(t, st.Atomic(ctx, func(tx store.Tx) error {
		for range 3 {
			if _, err := tx.NextID("T"); err != nil {
				return err
			}
		}
		if err := tx.SetKV("charter", "be careful\n\"quoted\" ü"); err != nil {
			return err
		}
		if err := tx.SetKV("paused", ""); err != nil {
			return err
		}
		if err := tx.InsertBrief(&model.Brief{ID: "B-1", Title: "brief", Body: "# body", Repos: []string{"o/r"}, Status: "open",
			Budget: model.BriefBudget{MaxCostEUR: 12.5, MaxParallelAgent: 7}, AllowedProviders: []string{"anthropic"},
			CrossReview: true, AutoDoneOnMerge: true, PlanStatus: "approved", CreatedAt: at}); err != nil {
			return err
		}
		if err := tx.InsertPlan(&model.Plan{ID: "P-1", BriefID: "B-1", Status: "approved", ProposedBy: "rex", Note: ptr("n"),
			Warnings: []model.PlanFinding{{Severity: "warning", Rule: "no_tester", Message: "m"}}, CreatedAt: at, DecidedAt: ptr(at.Add(time.Minute))}); err != nil {
			return err
		}
		if err := tx.InsertAgent(&model.Agent{ID: "claude-1", Role: "worker", Kind: "claude-code", Model: "m", Provider: "anthropic",
			Status: "active", CreatedAt: at, Spawned: true, TokenHash: "hash-of-claude-1", LastSeenAt: ptr(at)}); err != nil {
			return err
		}
		if err := tx.InsertTask(&model.Task{ID: "T-1", BriefID: "B-1", PlanID: "P-1", Title: "t", Spec: "s", AcceptanceCriteria: []string{"a", "b"},
			Repo: "o/r", Branch: "sub/t-1", Role: "worker", Assignee: ptr("claude-1"), DependsOn: []string{}, Priority: 3, Status: "in_progress",
			ProgressPct: 40, LastHeartbeatAt: ptr(at), LeaseExpiresAt: ptr(at.Add(time.Minute)), PRURL: ptr("https://x/pr/1"),
			Budget: model.Budget{MaxMinutes: 60, MaxCostEUR: 5.5}, CostUsed: 0.4, MinutesUsed: 12, ReviewRounds: 1,
			Tests: &model.TestResult{Passed: 9, Failed: 1, ReportedAt: at}, BlockedReason: ptr("x"), LeaseExpiries: 2, CreatedAt: at, UpdatedAt: at}); err != nil {
			return err
		}
		if err := tx.InsertQuestion(&model.Question{ID: "Q-1", BriefID: ptr("B-1"), TaskID: ptr("T-1"), AgentID: "claude-1", Question: "why?",
			Options: []string{"a", "b"}, Status: "answered", Answer: ptr("a"), AnsweredAt: ptr(at), CreatedAt: at}); err != nil {
			return err
		}
		if err := tx.InsertSpawn(&model.Spawn{ID: "S-1", BriefID: "B-1", AgentID: "claude-1", Kind: "claude-code", Model: "m", Provider: "anthropic",
			Status: "failed", Spawner: "hc", Error: ptr("boom"), RequestedBy: "rex", CreatedAt: at}); err != nil {
			return err
		}
		for i := range 5 {
			if err := tx.InsertMessage(&model.Message{ThreadKind: "task", ThreadID: "T-1", AuthorID: "claude-1", AuthorRole: "worker",
				Kind: "note", Body: fmt.Sprintf("note %d \u2603", i), CreatedAt: at.Add(time.Duration(i) * time.Second)}); err != nil {
				return err
			}
		}
		for i := range 6 {
			e := &model.AuditEvent{TS: at.Add(time.Duration(i) * time.Second), AgentID: "claude-1", Role: "worker", Tool: "claim_task",
				After: map[string]any{"n": i, "status": "in_progress"}}
			if i%2 == 0 {
				e.TaskID, e.Note, e.Before = ptr("T-1"), ptr("note"), map[string]any{"status": "assigned"}
			}
			if err := tx.InsertAudit(e); err != nil {
				return err
			}
		}
		return nil
	}))
	mustDo(t, st.SetMirrorLink(ctx, "T-1", "o/r", "https://x/issues/1", "digest"))
	mustDo(t, st.PutIdempotency(ctx, "claude-1", "key-1", &idempotency.Record{Tool: "submit", ArgsHash: "h", Result: []byte(`{"ok":true}`),
		ExpiresAt: time.Now().Add(time.Hour)}, time.Now()))
	mustDo(t, st.Close())
	raw, err := sql.Open("sqlite", "file:"+path)
	mustDo(t, err)
	for _, q := range []string{
		`INSERT INTO hillclimb_jobs VALUES ('H-1','B-1','o/r','make bench','min',NULL,0,0,'running',NULL,1.5,1,1,0,1)`,
		`INSERT INTO hillclimb_attempts (job_id, agent_id, branch, value, status, measured_at, log) VALUES ('H-1','claude-1','b',1.5,'ok',2,'log')`,
		`INSERT INTO hillclimb_attempts (job_id, agent_id, branch, value, status, measured_at, log) VALUES ('H-1','claude-1','b',NULL,'failed',3,'')`,
		`INSERT INTO mq_candidates (id, repo, pr, head, base, task_id, state, enqueued_at, updated_at) VALUES ('M-1','o/r',7,'h','main','T-1','queued',1,2)`,
	} {
		_, err := raw.Exec(q)
		mustDo(t, err)
	}
	mustDo(t, raw.Close())
}

func runCmd(t *testing.T, fn func([]string, io.Writer) error, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := fn(args, &out)
	return out.String(), err
}

func archiveLines(t *testing.T, path string) [][]byte {
	t.Helper()
	f, err := os.Open(path)
	mustDo(t, err)
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	mustDo(t, err)
	sc := bufio.NewScanner(gz)
	sc.Buffer(nil, maxLineBytes)
	var out [][]byte
	for sc.Scan() {
		out = append(out, bytes.Clone(sc.Bytes()))
	}
	mustDo(t, sc.Err())
	return out
}

func writeArchive(t *testing.T, path string, lines [][]byte) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	for _, l := range lines {
		_, _ = gz.Write(append(bytes.Clone(l), '\n'))
	}
	mustDo(t, gz.Close())
	mustDo(t, os.WriteFile(path, buf.Bytes(), 0o600))
}

func seal(lines [][]byte) [][]byte {
	body := lines[:len(lines)-1]
	sum := sha256.New()
	for _, l := range body {
		sum.Write(l)
		sum.Write([]byte{'\n'})
	}
	return append(slices.Clone(body), []byte(`{"sha256":"`+hex.EncodeToString(sum.Sum(nil))+`"}`))
}

func dataLines(t *testing.T, path string) []string {
	t.Helper()
	lines := archiveLines(t, path)
	var out []string
	for _, l := range lines[1 : len(lines)-1] {
		out = append(out, string(l))
	}
	return out
}

func TestExportImportRoundTripSQLite(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.db"), filepath.Join(dir, "dst.db")
	populate(t, src)
	a, b := filepath.Join(dir, "a.jsonl.gz"), filepath.Join(dir, "b.jsonl.gz")

	out, err := runCmd(t, runExport, "--db", src, "--out", a)
	mustDo(t, err)
	if !strings.Contains(out, "exported hillclimb_attempts: 2 rows") || !strings.Contains(out, "exported audit: 6 rows") {
		t.Fatalf("export output:\n%s", out)
	}
	hd := archiveLines(t, a)[0]
	var h line
	mustDo(t, json.Unmarshal(hd, &h))
	names := map[string]bool{}
	for _, tb := range h.Header.Tables {
		names[tb.Name] = true
	}
	for _, want := range []string{"tasks", "briefs", "plans", "agents", "messages", "questions", "audit", "spawns", "kv", "seq", "mirror_issues", "idempotency_keys", "hillclimb_jobs", "hillclimb_attempts", "mq_candidates"} {
		if !names[want] {
			t.Errorf("header misses table %s", want)
		}
	}

	out, err = runCmd(t, runImport, "--db", dst, "--in", a)
	mustDo(t, err)
	if !strings.Contains(out, "imported messages: 5 rows") || !strings.Contains(out, "import complete") {
		t.Fatalf("import output:\n%s", out)
	}
	_, err = runCmd(t, runExport, "--db", dst, "--out", b)
	mustDo(t, err)
	if want, got := dataLines(t, a), dataLines(t, b); !slices.Equal(want, got) {
		t.Fatalf("round trip changed data\nwant %d lines, got %d", len(want), len(got))
	}

	st, err := sqlite.Open(dst)
	mustDo(t, err)
	defer func() { _ = st.Close() }()
	mustDo(t, st.View(context.Background(), func(tx store.Tx) error {
		ag, err := tx.GetAgentByTokenHash("hash-of-claude-1")
		if err != nil || ag.ID != "claude-1" || !ag.Spawned {
			t.Fatalf("agent by token hash: %+v %v", ag, err)
		}
		c, err := tx.VerifyAudit()
		if err != nil || !c.OK || c.Events != 6 {
			t.Fatalf("audit chain: %+v %v", c, err)
		}
		return nil
	}))
	var id string
	mustDo(t, st.Atomic(context.Background(), func(tx store.Tx) (err error) { id, err = tx.NextID("T"); return }))
	if id != "T-4" {
		t.Fatalf("next id %s, want T-4", id)
	}
	var m model.Message
	mustDo(t, st.Atomic(context.Background(), func(tx store.Tx) error {
		m = model.Message{ThreadKind: "task", ThreadID: "T-1", AuthorID: "x", AuthorRole: "worker", Kind: "note", Body: "b", CreatedAt: time.Now()}
		return tx.InsertMessage(&m)
	}))
	if m.ID != 6 {
		t.Fatalf("next message id %d, want 6", m.ID)
	}
}

func TestImportRefusesNonEmptyTargetWithoutForce(t *testing.T) {
	dir := t.TempDir()
	src, dst, arc := filepath.Join(dir, "src.db"), filepath.Join(dir, "dst.db"), filepath.Join(dir, "a.jsonl.gz")
	populate(t, src)
	_, err := runCmd(t, runExport, "--db", src, "--out", arc)
	mustDo(t, err)
	_, err = runCmd(t, runImport, "--db", dst, "--in", arc)
	mustDo(t, err)

	_, err = runCmd(t, runImport, "--db", dst, "--in", arc)
	if err == nil || !strings.Contains(err.Error(), "not empty") || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("second import: %v", err)
	}
	_, err = runCmd(t, runImport, "--db", dst, "--in", arc, "--force")
	mustDo(t, err)
	b := filepath.Join(dir, "b.jsonl.gz")
	_, err = runCmd(t, runExport, "--db", dst, "--out", b)
	mustDo(t, err)
	if !slices.Equal(dataLines(t, arc), dataLines(t, b)) {
		t.Fatal("forced import did not replace the target exactly")
	}
	_, err = runCmd(t, runAuditVerify, "--db", dst)
	mustDo(t, err)
	st, err := sqlite.Open(dst)
	mustDo(t, err)
	defer func() { _ = st.Close() }()
	err = st.Atomic(context.Background(), func(tx store.Tx) error {
		return tx.InsertAudit(&model.AuditEvent{TS: time.Now(), AgentID: "a", Role: "worker", Tool: "x"})
	})
	mustDo(t, err)
	raw, err := sql.Open("sqlite", "file:"+dst)
	mustDo(t, err)
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(`DELETE FROM audit WHERE id = 1`); err == nil {
		t.Fatal("audit delete trigger missing after forced import")
	}
}

func TestImportDetectsTampering(t *testing.T) {
	dir := t.TempDir()
	src, arc := filepath.Join(dir, "src.db"), filepath.Join(dir, "a.jsonl.gz")
	populate(t, src)
	_, err := runCmd(t, runExport, "--db", src, "--out", arc)
	mustDo(t, err)
	orig := archiveLines(t, arc)
	find := func(prefix string) int {
		for i, l := range orig {
			if bytes.HasPrefix(l, []byte(prefix)) {
				return i
			}
		}
		t.Fatalf("no line with prefix %s", prefix)
		return -1
	}
	msg, aud := find(`{"t":"messages"`), find(`{"t":"audit"`)

	for _, tc := range []struct {
		name, want string
		mutate     func([][]byte) [][]byte
	}{
		{"edited row", "checksum mismatch", func(l [][]byte) [][]byte {
			l[msg] = bytes.Replace(l[msg], []byte("note 0"), []byte("note X"), 1)
			return l
		}},
		{"edited audit row, resealed", "audit hash chain broken", func(l [][]byte) [][]byte {
			l[aud] = bytes.Replace(l[aud], []byte(`"tool":"claim_task"`), []byte(`"tool":"approve_plan"`), 1)
			return seal(l)
		}},
		{"audit event removed, resealed", "audit hash chain broken", func(l [][]byte) [][]byte {
			return seal(slices.Delete(l, aud+1, aud+2))
		}},
		{"message removed, resealed", "header says", func(l [][]byte) [][]byte {
			return seal(slices.Delete(l, msg, msg+1))
		}},
		{"truncated", "missing sha256 trailer", func(l [][]byte) [][]byte { return l[:len(l)-1] }},
		{"wrong version", "unsupported schema version", func(l [][]byte) [][]byte {
			l[0] = bytes.Replace(l[0], []byte(`"schema_version":1`), []byte(`"schema_version":99`), 1)
			return seal(l)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := filepath.Join(dir, "bad.jsonl.gz")
			writeArchive(t, bad, tc.mutate(slices.Clone(orig)))
			dst := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "_")+".db")
			_, err := runCmd(t, runImport, "--db", dst, "--in", bad)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want %q", err, tc.want)
			}
			if _, err := os.Stat(dst); err == nil {
				t.Fatal("target was created for a rejected archive")
			}
		})
	}
}

func TestExportStreamsLargeDatasetWithinMemoryBound(t *testing.T) {
	if testing.Short() {
		t.Skip("large dataset")
	}
	const bodySize = 2048
	rows := testmode.Size(10000, 20000)
	bound := uint64(testmode.Size(6<<20, 12<<20))
	dir := t.TempDir()
	src, dst, arc := filepath.Join(dir, "src.db"), filepath.Join(dir, "dst.db"), filepath.Join(dir, "big.jsonl.gz")
	st, err := sqlite.Open(src)
	mustDo(t, err)
	mustDo(t, st.Close())
	raw, err := sql.Open("sqlite", "file:"+src)
	mustDo(t, err)
	block := make([]byte, bodySize/2)
	_, _ = rand.Read(block)
	body := hex.EncodeToString(block)
	tx, err := raw.Begin()
	mustDo(t, err)
	stmt, err := tx.Prepare(`INSERT INTO messages (thread_kind, thread_id, author_id, author_role, kind, body, created_at) VALUES ('task','T-1','a','worker','note',?,?)`)
	mustDo(t, err)
	for i := range rows {
		_, err := stmt.Exec(fmt.Sprintf("%08d", i)+body, i)
		mustDo(t, err)
	}
	mustDo(t, tx.Commit())
	mustDo(t, raw.Close())
	if uint64(rows*bodySize) < 3*bound {
		t.Fatal("dataset too small to prove streaming")
	}

	peak := measurePeak(t, func() {
		_, err := runCmd(t, runExport, "--db", src, "--out", arc)
		mustDo(t, err)
	})
	if peak > bound {
		t.Fatalf("export peak heap %d MiB over bound", peak>>20)
	}
	peak = measurePeak(t, func() {
		_, err := runCmd(t, runImport, "--db", dst, "--in", arc)
		mustDo(t, err)
	})
	if peak > bound {
		t.Fatalf("import peak heap %d MiB over bound", peak>>20)
	}
	check, err := sql.Open("sqlite", "file:"+dst)
	mustDo(t, err)
	defer func() { _ = check.Close() }()
	var n int
	var last string
	mustDo(t, check.QueryRow(`SELECT count(*), max(substr(body, 1, 8)) FROM messages`).Scan(&n, &last))
	if n != rows || last != fmt.Sprintf("%08d", rows-1) {
		t.Fatalf("imported %d rows, last %s", n, last)
	}
}

func measurePeak(t *testing.T, fn func()) uint64 {
	t.Helper()
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var (
		mu   sync.Mutex
		peak uint64
	)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				mu.Lock()
				peak = max(peak, m.HeapAlloc)
				mu.Unlock()
			}
		}
	}()
	fn()
	close(stop)
	<-done
	mu.Lock()
	defer mu.Unlock()
	if peak < base.HeapAlloc {
		return 0
	}
	return peak - base.HeapAlloc
}

func TestRoundTripSQLiteToPostgres(t *testing.T) {
	base := os.Getenv("SUBSPACE_TEST_PG_DSN")
	if base == "" {
		t.Skip("SUBSPACE_TEST_PG_DSN not set")
	}
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	schema := "subspace_export_" + hex.EncodeToString(suffix)
	admin, err := sql.Open("pgx", base)
	mustDo(t, err)
	_, err = admin.Exec(`CREATE SCHEMA ` + schema)
	mustDo(t, err)
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	dsn := base + " search_path=" + schema
	if strings.HasPrefix(base, "postgres://") || strings.HasPrefix(base, "postgresql://") {
		u, err := url.Parse(base)
		mustDo(t, err)
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		dsn = u.String()
	}

	dir := t.TempDir()
	src, arc, back := filepath.Join(dir, "src.db"), filepath.Join(dir, "a.jsonl.gz"), filepath.Join(dir, "b.jsonl.gz")
	populate(t, src)
	_, err = runCmd(t, runExport, "--db", src, "--out", arc)
	mustDo(t, err)
	_, err = runCmd(t, runImport, "--db", dsn, "--in", arc)
	if err == nil || !strings.Contains(err.Error(), "has no table") {
		t.Fatalf("extension tables must not be dropped silently: %v", err)
	}
	_, err = runCmd(t, runImport, "--db", dsn, "--in", arc, "--force")
	if err == nil || !strings.Contains(err.Error(), "has no table") {
		t.Fatalf("forced import: %v", err)
	}

	core := filepath.Join(dir, "core.jsonl.gz")
	lines := archiveLines(t, arc)
	var h line
	mustDo(t, json.Unmarshal(lines[0], &h))
	keep := map[string]bool{}
	for _, c := range coreTables {
		keep[c.name] = true
	}
	var tables []headerTable
	for _, tb := range h.Header.Tables {
		if keep[tb.Name] {
			tables = append(tables, tb)
		}
	}
	h.Header.Tables = tables
	first, err := json.Marshal(h)
	mustDo(t, err)
	filtered := [][]byte{first}
	for _, l := range lines[1 : len(lines)-1] {
		var r line
		mustDo(t, json.Unmarshal(l, &r))
		if keep[r.T] {
			filtered = append(filtered, l)
		}
	}
	writeArchive(t, core, seal(append(filtered, nil)))

	_, err = runCmd(t, runImport, "--db", dsn, "--in", core)
	mustDo(t, err)
	_, err = runCmd(t, runExport, "--db", dsn, "--out", back)
	mustDo(t, err)

	rowsOf := func(path string) map[string][]map[string]any {
		out := map[string][]map[string]any{}
		for _, l := range archiveLines(t, path)[1:] {
			dec := json.NewDecoder(bytes.NewReader(l))
			dec.UseNumber()
			var r line
			mustDo(t, dec.Decode(&r))
			if r.T != "" {
				out[r.T] = append(out[r.T], r.R)
			}
		}
		return out
	}
	want, got := rowsOf(core), rowsOf(back)
	for _, c := range coreTables {
		if len(want[c.name]) != len(got[c.name]) {
			t.Fatalf("%s: %d rows, want %d", c.name, len(got[c.name]), len(want[c.name]))
		}
		for i, row := range got[c.name] {
			for col, v := range row {
				w, ok := want[c.name][i][col]
				if !ok && col == "id_num" {
					continue
				}
				if !sameValue(w, v) {
					t.Errorf("%s row %d column %s: %v, want %v", c.name, i, col, v, w)
				}
			}
		}
	}

	_, err = runCmd(t, runImport, "--db", dsn, "--in", core)
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("non-empty postgres target: %v", err)
	}
	pgBack := filepath.Join(dir, "pg.db")
	_, err = runCmd(t, runImport, "--db", pgBack, "--in", back)
	mustDo(t, err)
	_, err = runCmd(t, runAuditVerify, "--db", pgBack)
	mustDo(t, err)
}

func sameValue(a, b any) bool {
	if fmt.Sprint(a) == fmt.Sprint(b) {
		return true
	}
	as, aok := a.(string)
	bs, bok := b.(string)
	if !aok || !bok {
		return false
	}
	var av, bv any
	if json.Unmarshal([]byte(as), &av) != nil || json.Unmarshal([]byte(bs), &bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}
