package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SigmaUno/subspace/internal/testmode"
)

func TestSoakSmoke(t *testing.T) {
	dir := t.TempDir()
	out, series := filepath.Join(dir, "report.json"), filepath.Join(dir, "series.csv")
	args := []string{
		"-duration", testmode.Size("17s", "20s"), "-sample", "1s", "-warmup", testmode.Size("4s", "5s"), "-seed", "42",
		"-workers", "6", "-testers", "2", "-reviewers", "2", "-sse", "4", "-briefs", "2", "-max-plan", "8",
		"-lease", "3s", "-agent-life", "6s", "-brief-life", "12s",
		"-restart-every", "6s", "-pause-every", "7s", "-backup-every", "5s", "-verify-every", "5s",
		"-streamers", "8", "-streamer-life", "5s",
		"-max-heap-slope", "1000", "-max-goroutine-slope", "100000",
		"-dir", filepath.Join(dir, "data"), "-out", out, "-csv", series,
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, stdout.String(), stderr.String())
	}

	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var rep Report
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.Passed || rep.ViolationCount != 0 || rep.Aborted != "" {
		t.Fatalf("soak failed: %s", stdout.String())
	}
	for _, a := range []string{"restart", "backup", "pause_all", "agent_join", "claim", "submit", "propose_plan", "bulk_register", "log_output", "create_schedule"} {
		if rep.Actions[a] == 0 {
			t.Errorf("action %s never happened: %v", a, rep.Actions)
		}
	}
	if rep.SSE.Connects == 0 || rep.SSE.Events == 0 {
		t.Errorf("no SSE traffic: %+v", rep.SSE)
	}
	if len(rep.Trends) != 2 || rep.Trends[0].Points < minTrendPoints {
		t.Errorf("trends not computed: %+v", rep.Trends)
	}

	f, err := os.Open(series)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != rep.Samples+1 || rep.Samples < 15 {
		t.Fatalf("csv has %d rows for %d samples", len(rows), rep.Samples)
	}
	for _, r := range rows[1:] {
		if r[1] == "0" || r[2] == "0" {
			t.Fatalf("empty runtime sample: %v", r)
		}
	}
}
