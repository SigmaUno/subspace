package transcript_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/agentout"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/testdb"
	"github.com/SigmaUno/subspace/internal/testmain"
	"github.com/SigmaUno/subspace/internal/transcript"
)

func TestMain(m *testing.M) { testmain.Run(m) }

var t0 = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

type tasks struct {
	mu     sync.Mutex
	briefs map[string]string
	status map[string]string
}

func (k *tasks) Task(_ context.Context, id string) (transcript.Info, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if b, ok := k.briefs[id]; ok {
		return transcript.Info{BriefID: b, Status: k.status[id]}, nil
	}
	return transcript.Info{}, model.NotFound("task %s not found", id)
}

func (k *tasks) set(id, status string) {
	k.mu.Lock()
	k.status[id] = status
	k.mu.Unlock()
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Second)
	return c.t
}

type rig struct {
	t     *testing.T
	st    store.Store
	dir   string
	clk   *clock
	tasks *tasks
}

func newRig(t *testing.T) *rig {
	t.Helper()
	return &rig{t: t, st: testdb.Open(t, testdb.Path(t, "t.db")), dir: filepath.Join(t.TempDir(), "transcripts"), clk: &clock{t: t0},
		tasks: &tasks{briefs: map[string]string{"T-1": "B-1", "T-2": "B-1", "T-9": "B-2"}, status: map[string]string{}}}
}

func (r *rig) service(mut func(*transcript.Config)) *transcript.Service {
	r.t.Helper()
	cfg := transcript.DefaultConfig()
	cfg.Dir, cfg.Now = r.dir, r.clk.Now
	if mut != nil {
		mut(&cfg)
	}
	svc, err := transcript.New(r.st, r.tasks, cfg)
	if err != nil {
		r.t.Fatal(err)
	}
	return svc
}

func line(task, agent, text string, at int) agentout.Line {
	return agentout.Line{AgentID: agent, TaskID: &task, Stream: "stdout", Text: text, TS: t0.Add(time.Duration(at) * time.Second)}
}

func burst(task string, from, n int) []agentout.Line {
	out := make([]agentout.Line, n)
	for i := range out {
		out[i] = line(task, "w1", fmt.Sprintf("%s line %03d", task, from+i), from+i)
	}
	return out
}

func gunzip(t *testing.T, b []byte) []transcript.Entry {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	var out []transcript.Entry
	for _, l := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		if len(l) == 0 {
			continue
		}
		var e transcript.Entry
		if err := json.Unmarshal(l, &e); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func readAll(t *testing.T, svc *transcript.Service, task string) []transcript.Entry {
	t.Helper()
	var out []transcript.Entry
	for since := int64(0); ; {
		p, err := svc.Read(context.Background(), task, transcript.ReadOptions{Since: since, Limit: 37})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p.Lines...)
		if !p.More {
			return out
		}
		since = p.NextSince
	}
}

func files(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".ndjson.gz") {
			out = append(out, p)
		}
		return nil
	})
	slices.Sort(out)
	return out
}

func TestSegmentsRotateAndPageByOffset(t *testing.T) {
	r := newRig(t)
	svc := r.service(func(c *transcript.Config) { c.SegmentLines = 10 })
	svc.Add(burst("T-1", 0, 25))
	svc.Add(burst("T-2", 0, 3))
	if err := svc.Flush(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if got := files(t, r.dir); len(got) != 4 {
		t.Fatalf("want 3 segments for T-1 and 1 for T-2, got %v", got)
	}

	got := readAll(t, svc, "T-1")
	if len(got) != 25 {
		t.Fatalf("read %d lines", len(got))
	}
	for i, e := range got {
		if e.Offset != int64(i+1) || e.Text != fmt.Sprintf("T-1 line %03d", i) || e.AgentID != "w1" {
			t.Fatalf("entry %d: %+v", i, e)
		}
	}

	p, err := svc.Read(context.Background(), "T-1", transcript.ReadOptions{Since: 8, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Lines) != 5 || p.Lines[0].Offset != 9 || p.NextSince != 13 || !p.More || p.Gap || p.Segments != 3 ||
		p.FirstOffset != 1 || p.LastOffset != 25 {
		t.Fatalf("page: %+v", p)
	}

	p, err = svc.Read(context.Background(), "T-1", transcript.ReadOptions{Query: "LINE 01", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Lines) != 10 || p.Lines[0].Offset != 11 || p.More {
		t.Fatalf("search: %+v", p)
	}

	p, err = svc.Read(context.Background(), "T-1", transcript.ReadOptions{At: t0.Add(17 * time.Second), Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Lines) != 3 || p.Lines[0].Text != "T-1 line 017" || !p.More {
		t.Fatalf("jump to time: %+v", p)
	}

	var gz bytes.Buffer
	err = svc.Download(context.Background(), "T-1", func(size int64) io.Writer {
		var n int64
		for _, f := range files(t, filepath.Join(r.dir, "T-1")) {
			st, _ := os.Stat(f)
			n += st.Size()
		}
		if size != n {
			t.Fatalf("size %d, files %d", size, n)
		}
		return &gz
	})
	if err != nil {
		t.Fatal(err)
	}
	if all := gunzip(t, gz.Bytes()); len(all) != 25 || all[24].Offset != 25 {
		t.Fatalf("download holds %d entries", len(all))
	}
}

func TestReadFlushesPendingLines(t *testing.T) {
	r := newRig(t)
	svc := r.service(nil)
	svc.Add(burst("T-1", 0, 2))
	if got := readAll(t, svc, "T-1"); len(got) != 2 {
		t.Fatalf("pending lines not visible: %v", got)
	}
	if _, err := svc.Read(context.Background(), "T-404", transcript.ReadOptions{}); model.AsError(err).Code != "not_found" {
		t.Fatalf("unknown task: %v", err)
	}
	if _, err := svc.Read(context.Background(), "T-1", transcript.ReadOptions{Since: -1}); model.AsError(err).Code != "bad_request" {
		t.Fatalf("negative since: %v", err)
	}
}

func TestSecretsNeverReachTheSegmentBytes(t *testing.T) {
	r := newRig(t)
	svc := r.service(nil)
	tail := "abcdefghijklmnopqrstuvwxyz0123456789"
	secrets := []string{
		"gh" + "p_" + tail,
		"sk" + "-ant-api03-" + tail,
		"AK" + "IA" + strings.ToUpper(tail[:16]),
		"hunter2hunter2hunter2",
		"Zm9vYmFyYmF6cXV4MTIzNDU2Nzg5MGFiY2RlZmdoaWo",
		"MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7",
		"qZ1xK8vLn3uJ0aT5dRwYh2oB6cEfG9sPiMlNkXtVrUe4",
	}
	pem := strings.Repeat("-", 5) + "%s RSA PRIVATE KEY" + strings.Repeat("-", 5)
	input := []string{
		"export GITHUB_TOKEN=" + secrets[0],
		"key " + secrets[1] + " leaked",
		"aws " + secrets[2],
		"db_password: " + secrets[3],
		"Authorization: Bearer " + secrets[4],
		fmt.Sprintf(pem, "BEGIN"),
		secrets[5],
		secrets[6],
		fmt.Sprintf(pem, "END"),
		"build ok",
	}
	lines := make([]agentout.Line, len(input))
	for i, text := range input {
		lines[i] = line("T-1", "w1", text, i)
	}
	svc.Add(lines)
	if err := svc.Flush(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	fs := files(t, r.dir)
	if len(fs) != 1 {
		t.Fatalf("segments: %v", fs)
	}
	raw, err := os.ReadFile(fs[0])
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(zr)
	for _, s := range secrets {
		if bytes.Contains(raw, []byte(s)) || bytes.Contains(plain, []byte(s)) {
			t.Fatalf("secret %q reached the segment bytes", s)
		}
	}
	got := readAll(t, svc, "T-1")
	if len(got) != len(input) || got[9].Text != "build ok" || got[0].Text != "export GITHUB_TOKEN=ghp_****" {
		t.Fatalf("redacted lines: %+v", got)
	}
	for _, i := range []int{6, 7} {
		if got[i].Text != "****" {
			t.Fatalf("key body line %d: %q", i, got[i].Text)
		}
	}
}

func TestSizeLimitDropsOldestSegmentsPerBrief(t *testing.T) {
	r := newRig(t)
	svc := r.service(func(c *transcript.Config) { c.SegmentLines, c.MaxBytes = 20, 6000 })
	rng := rand.New(rand.NewSource(7))
	noise := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = "abcdefghijklmnopqrstuvwxyz "[rng.Intn(27)]
		}
		return string(b)
	}
	svc.Add([]agentout.Line{line("T-9", "w9", "other brief", 0)})
	for i := range 12 {
		batch := make([]agentout.Line, 20)
		for j := range batch {
			batch[j] = line("T-1", "w1", noise(80), i*20+j)
		}
		svc.Add(batch)
		if err := svc.Flush(context.Background(), ""); err != nil {
			t.Fatal(err)
		}
	}
	p, err := svc.Read(context.Background(), "T-1", transcript.ReadOptions{Limit: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if p.Bytes > 6000 || p.Segments >= 12 || p.Segments == 0 || !p.Gap || p.FirstOffset <= 1 || p.LastOffset != 240 {
		t.Fatalf("limit not enforced: %+v", p)
	}
	if len(p.Lines) != int(p.LastOffset-p.FirstOffset+1) || p.Lines[0].Offset != p.FirstOffset {
		t.Fatalf("newest lines must stay contiguous: %d lines from %d", len(p.Lines), p.FirstOffset)
	}
	if got := readAll(t, svc, "T-9"); len(got) != 1 {
		t.Fatalf("another brief lost its transcript: %v", got)
	}
	if n := len(files(t, filepath.Join(r.dir, "T-1"))); n != p.Segments {
		t.Fatalf("%d files for %d segments", n, p.Segments)
	}
	var pruned []*model.AuditEvent
	err = r.st.View(context.Background(), func(tx store.Tx) (e error) {
		pruned, e = tx.ListAudit(store.AuditFilter{Tool: "transcript_prune", Limit: 100})
		return
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pruned) != 12-p.Segments || pruned[0].Before["sha256"] == "" || pruned[0].After["reason"] != "size_limit" {
		t.Fatalf("prune audit: %d events, want %d", len(pruned), 12-p.Segments)
	}
}

func TestRestartContinuesOffsetsAndKeepsContent(t *testing.T) {
	r := newRig(t)
	a := r.service(nil)
	a.Add(burst("T-1", 0, 4))
	if err := a.Flush(context.Background(), "T-1"); err != nil {
		t.Fatal(err)
	}

	b := r.service(nil)
	b.Add(burst("T-1", 4, 3))
	got := readAll(t, b, "T-1")
	if len(got) != 7 || got[3].Offset != 4 || got[4].Offset != 5 || got[6].Text != "T-1 line 006" {
		t.Fatalf("after restart: %+v", got)
	}
}

func TestTamperedSegmentIsRefused(t *testing.T) {
	r := newRig(t)
	svc := r.service(nil)
	svc.Add(burst("T-1", 0, 3))
	if err := svc.Flush(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	f := files(t, r.dir)[0]
	raw, _ := os.ReadFile(f)
	raw[len(raw)/2] ^= 0xff
	if err := os.WriteFile(f, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Read(context.Background(), "T-1", transcript.ReadOptions{})
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("tampered segment served: %v", err)
	}
	sum := sha256.Sum256(raw)
	if !strings.Contains(err.Error(), hex.EncodeToString(sum[:])) {
		t.Fatalf("error should name the computed digest: %v", err)
	}
	err = svc.Download(context.Background(), "T-1", func(int64) io.Writer { t.Fatal("download started"); return io.Discard })
	if err == nil {
		t.Fatal("tampered download served")
	}
}

func TestSettledTasksFlushWithoutWaitingForTheInterval(t *testing.T) {
	r := newRig(t)
	svc := r.service(func(c *transcript.Config) { c.FlushEvery, c.CheckEvery = time.Hour, 10*time.Millisecond })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Run(ctx)
	svc.Add(burst("T-1", 0, 2))
	svc.Add(burst("T-2", 0, 2))
	r.tasks.set("T-1", model.StatusInProgress)
	time.Sleep(100 * time.Millisecond)
	if n := len(files(t, r.dir)); n != 0 {
		t.Fatalf("running tasks were flushed: %d files", n)
	}
	r.tasks.set("T-1", model.StatusSubmitted)
	deadline := time.Now().Add(3 * time.Second)
	for len(files(t, filepath.Join(r.dir, "T-1"))) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("submitting did not flush the transcript")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := len(files(t, filepath.Join(r.dir, "T-2"))); n != 0 {
		t.Fatalf("another task was flushed: %d files", n)
	}
}

func TestPeriodicFlushWritesWithoutEvents(t *testing.T) {
	r := newRig(t)
	svc := r.service(func(c *transcript.Config) { c.FlushEvery = 20 * time.Millisecond })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Run(ctx)
	svc.Add(burst("T-1", 0, 2))
	deadline := time.Now().Add(3 * time.Second)
	for len(files(t, r.dir)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("interval flush never ran")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestKeyBlockMaskingFollowsTheAgentThatPrintedIt(t *testing.T) {
	r := newRig(t)
	svc := r.service(nil)
	pem := strings.Repeat("-", 5) + "%s RSA PRIVATE KEY" + strings.Repeat("-", 5)
	body := "QUJDREVGR0g="
	svc.Add([]agentout.Line{
		line("T-1", "w1", fmt.Sprintf(pem, "BEGIN"), 0),
		line("T-1", "t1", "fixture.go:3: "+fmt.Sprintf(pem, "END"), 1),
		line("T-1", "w1", body, 2),
		line("T-1", "t1", "tests ok", 3),
		line("T-1", "w1", fmt.Sprintf(pem, "END"), 4),
	})
	if err := svc.Flush(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	got := readAll(t, svc, "T-1")
	if len(got) != 5 || got[2].Text != "****" || got[3].Text != "tests ok" {
		t.Fatalf("lines: %+v", got)
	}
}

func TestPerTaskCachesStayBoundedAndOffsetsContinue(t *testing.T) {
	r := newRig(t)
	n := transcript.MaxCachedTasks + 50
	for i := range n {
		r.tasks.briefs[fmt.Sprintf("T-%d", 100+i)] = "B-1"
	}
	svc := r.service(func(c *transcript.Config) { c.MaxBytes = 0 })
	for i := range n {
		svc.Add(burst(fmt.Sprintf("T-%d", 100+i), 0, 1))
	}
	if err := svc.Flush(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if c := svc.Cached(); c > transcript.MaxCachedTasks {
		t.Fatalf("caching %d tasks, limit %d", c, transcript.MaxCachedTasks)
	}
	for _, id := range []string{"T-100", fmt.Sprintf("T-%d", 100+n-1)} {
		svc.Add(burst(id, 1, 2))
		got := readAll(t, svc, id)
		if len(got) != 3 || got[0].Offset != 1 || got[1].Offset != 2 || got[2].Offset != 3 {
			t.Fatalf("%s offsets after the cache dropped it: %+v", id, got)
		}
	}
}
