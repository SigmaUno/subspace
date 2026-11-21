package retention_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/agentout"
	"github.com/SigmaUno/subspace/internal/retention"
	"github.com/SigmaUno/subspace/internal/transcript"
)

type taskBriefs struct{}

func (taskBriefs) Task(context.Context, string) (transcript.Info, error) {
	return transcript.Info{BriefID: "B-1"}, nil
}

func TestTranscriptsKeepChainOfCustodyThroughArchiveAndRestore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st := openStore(t, filepath.Join(root, "t.db"))
	t.Cleanup(func() { _ = st.Close() })
	clk := &clock{t: t0}
	live, archive := filepath.Join(root, "transcripts"), filepath.Join(root, "retention")

	cfg := transcript.DefaultConfig()
	cfg.Dir, cfg.Now, cfg.SegmentLines = live, clk.Now, 2
	tr, err := transcript.New(st, taskBriefs{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	task := "T-1"
	var in []agentout.Line
	for i, text := range []string{"one", "two", "three", "four", "five"} {
		in = append(in, agentout.Line{AgentID: "w1", TaskID: &task, Stream: "stdout", Text: text, TS: t0.Add(time.Duration(i) * time.Second)})
	}
	tr.Add(in)
	if err := tr.Flush(ctx, ""); err != nil {
		t.Fatal(err)
	}
	before, err := tr.Read(ctx, task, transcript.ReadOptions{})
	if err != nil || len(before.Lines) != 5 || before.Segments != 3 {
		t.Fatalf("seed: %+v %v", before, err)
	}
	var originals [][]byte
	for _, f := range []string{"T-1-000000000001", "T-1-000000000003", "T-1-000000000005"} {
		b, err := os.ReadFile(filepath.Join(live, task, f+".ndjson.gz"))
		if err != nil {
			t.Fatal(err)
		}
		originals = append(originals, b)
	}

	clk.Set(t0.Add(40 * day))
	svc, err := retention.New(retention.Options{Store: st, Dir: archive, Files: live, Now: clk.Now,
		Policy: func() retention.Policy { p := retention.DefaultPolicy(); p.TranscriptsDays = 30; return p }()})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := svc.RunOnce(ctx)
	if err != nil || rep.Rows[retention.Transcripts] != 3 {
		t.Fatalf("run: %+v %v", rep, err)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM task_transcripts`); n != 0 {
		t.Fatalf("%d manifest rows left", n)
	}
	if left, _ := filepath.Glob(filepath.Join(live, task, "*.ndjson.gz")); len(left) != 0 {
		t.Fatalf("live files left: %v", left)
	}
	for i, name := range []string{"T-1-000000000001", "T-1-000000000003", "T-1-000000000005"} {
		b, err := os.ReadFile(filepath.Join(archive, "transcript-files", task, name+".ndjson.gz"))
		if err != nil || !bytes.Equal(b, originals[i]) {
			t.Fatalf("archived copy of %s: %v", name, err)
		}
	}
	if n := count(t, st, `SELECT COUNT(*) FROM audit WHERE tool = 'retention_archive'`); n != 1 {
		t.Fatalf("audit events: %d", n)
	}
	v, err := svc.Verify(ctx)
	if err != nil || !v.OK {
		t.Fatalf("verify: %+v %v", v, err)
	}
	gone, err := tr.Read(ctx, task, transcript.ReadOptions{})
	if err != nil || len(gone.Lines) != 0 || gone.Segments != 0 {
		t.Fatalf("archived transcript still served: %+v %v", gone, err)
	}

	archivedFile := filepath.Join(archive, "transcript-files", task, "T-1-000000000003.ndjson.gz")
	tampered := bytes.Clone(originals[1])
	tampered[len(tampered)-12] ^= 0x01
	if err := os.WriteFile(archivedFile, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if v, err := svc.Verify(ctx); err != nil || v.OK || v.Error == "" {
		t.Fatalf("tampered archive verified: %+v %v", v, err)
	}
	segs := listSegments(t, svc, retention.Transcripts)
	if len(segs) != 1 {
		t.Fatalf("segments: %+v", segs)
	}
	if _, err := svc.Restore(ctx, segs[0].ID); err == nil {
		t.Fatal("restored a tampered file")
	}
	if err := os.WriteFile(archivedFile, originals[1], 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Restore(ctx, segs[0].ID); err != nil {
		t.Fatal(err)
	}
	after, err := tr.Read(ctx, task, transcript.ReadOptions{})
	if err != nil || len(after.Lines) != 5 || after.Lines[4].Text != "five" || after.Lines[4].Offset != 5 {
		t.Fatalf("restored transcript: %+v %v", after, err)
	}
	for i, name := range []string{"T-1-000000000001", "T-1-000000000003", "T-1-000000000005"} {
		b, err := os.ReadFile(filepath.Join(live, task, name+".ndjson.gz"))
		if err != nil || !bytes.Equal(b, originals[i]) {
			t.Fatalf("restored file %s: %v", name, err)
		}
	}
	if n := count(t, st, `SELECT COUNT(*) FROM audit WHERE tool = 'retention_restore'`); n != 1 {
		t.Fatalf("restore audit events: %d", n)
	}
}

func listSegments(t *testing.T, svc *retention.Service, k retention.Kind) []retention.Segment {
	t.Helper()
	status, err := svc.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []retention.Segment
	for _, s := range status.Segments {
		if s.Kind == k && s.Status != retention.StatusRestored {
			out = append(out, s)
		}
	}
	return out
}
