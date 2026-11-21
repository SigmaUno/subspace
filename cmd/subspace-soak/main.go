package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("subspace-soak", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfg Config
	fs.DurationVar(&cfg.Duration, "duration", 30*time.Minute, "soak length (up to 24h)")
	fs.DurationVar(&cfg.Sample, "sample", 10*time.Second, "sampling and invariant check interval")
	fs.DurationVar(&cfg.Warmup, "warmup", 5*time.Minute, "samples before this are ignored by trend checks")
	fs.Uint64Var(&cfg.Seed, "seed", 0, "workload seed (0 = time based)")
	fs.IntVar(&cfg.Workers, "workers", 24, "concurrent worker agent slots")
	fs.IntVar(&cfg.Testers, "testers", 4, "concurrent tester agent slots")
	fs.IntVar(&cfg.Reviewers, "reviewers", 4, "reviewer agents")
	fs.IntVar(&cfg.SSE, "sse", 16, "SSE subscriber slots (each reconnects continuously)")
	fs.IntVar(&cfg.Briefs, "briefs", 3, "open briefs kept in flight")
	fs.IntVar(&cfg.MaxPlan, "max-plan", 30, "largest plan the leader proposes")
	fs.DurationVar(&cfg.Lease, "lease", time.Minute, "server lease duration")
	fs.DurationVar(&cfg.AgentLife, "agent-life", 3*time.Minute, "longest agent session before it leaves or crashes")
	fs.DurationVar(&cfg.BriefLife, "brief-life", 8*time.Minute, "age after which the owner closes a brief")
	fs.DurationVar(&cfg.RestartEvery, "restart-every", 5*time.Minute, "mean interval between server restarts (0 = off)")
	fs.DurationVar(&cfg.PauseEvery, "pause-every", 3*time.Minute, "mean interval between pause_all cycles (0 = off)")
	fs.DurationVar(&cfg.BackupEvery, "backup-every", 4*time.Minute, "mean interval between backups (0 = off)")
	fs.DurationVar(&cfg.VerifyEvery, "verify-every", 2*time.Minute, "interval between audit hash chain verifications (0 = only at the end)")
	fs.Float64Var(&cfg.MaxHeapSlope, "max-heap-slope", 0.5, "largest live heap growth after warmup, MiB per minute")
	fs.Float64Var(&cfg.MaxGoroutineSlope, "max-goroutine-slope", 0.5, "largest goroutine growth after warmup, per minute")
	fs.StringVar(&cfg.Dir, "dir", "", "data directory (default: a removed temp dir)")
	out := fs.String("out", "soak-report.json", "JSON report path")
	series := fs.String("csv", "soak-series.csv", "CSV time series path")
	level := fs.String("log-level", "error", "server log level (debug, info, warn, error)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(*level)); err != nil {
		_, _ = fmt.Fprintln(stderr, "log-level:", err)
		return 2
	}
	cfg.Log = slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: lv}))
	if cfg.Seed == 0 {
		cfg.Seed = uint64(time.Now().UnixNano()) //nolint:gosec // non-negative wall clock
	}

	rep, err := Run(ctx, cfg)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "soak:", err)
		return 2
	}
	if err := writeReport(*out, rep); err != nil {
		_, _ = fmt.Fprintln(stderr, "report:", err)
		return 2
	}
	if err := writeSeries(*series, rep.series); err != nil {
		_, _ = fmt.Fprintln(stderr, "csv:", err)
		return 2
	}
	summarize(stdout, rep)
	if !rep.Passed {
		return 1
	}
	return 0
}

func create(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	return os.Create(filepath.Clean(path))
}

func writeReport(path string, rep *Report) error {
	f, err := create(path)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func writeSeries(path string, samples []Sample) error {
	f, err := create(path)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	_ = w.Write(csvHeader)
	for _, s := range samples {
		_ = w.Write(s.record())
	}
	w.Flush()
	if err := w.Error(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func summarize(w io.Writer, rep *Report) {
	_, _ = fmt.Fprintf(w, "soak seed=%d elapsed=%.0fs samples=%d requests=%d p99=%.1fms\n",
		rep.Seed, rep.Elapsed, rep.Samples, rep.Latency.Count, rep.Latency.P99ms)
	for _, k := range sortedCounts(rep.Actions) {
		_, _ = fmt.Fprintf(w, "  action %-18s %d\n", k, rep.Actions[k])
	}
	for _, k := range sortedCounts(rep.Errors) {
		_, _ = fmt.Fprintf(w, "  error  %-18s %d\n", k, rep.Errors[k])
	}
	_, _ = fmt.Fprintf(w, "  sse connects=%d failures=%d events=%d lagged=%d\n", rep.SSE.Connects, rep.SSE.Failures, rep.SSE.Events, rep.SSE.Lagged)
	_, _ = fmt.Fprintf(w, "  peak goroutines=%d heap_live=%.1fMiB fds=%d wal=%.1fMiB writer_queue=%d p99=%.1fms\n",
		rep.Peak.Goroutines, float64(rep.Peak.HeapLive)/(1<<20), rep.Peak.FDs, float64(rep.Peak.WAL)/(1<<20), rep.Peak.WriterQueue, rep.Peak.P99ms)
	for _, t := range rep.Trends {
		_, _ = fmt.Fprintf(w, "  trend %-10s slope=%.3f %s limit=%.3f points=%d exceeded=%t\n", t.Metric, t.Slope, t.Unit, t.Limit, t.Points, t.Exceeded)
	}
	for _, v := range rep.Violations {
		_, _ = fmt.Fprintf(w, "  VIOLATION t=%.0fs %s: %s\n", v.At, v.Invariant, v.Detail)
	}
	if rep.Aborted != "" {
		_, _ = fmt.Fprintln(w, "  aborted:", rep.Aborted)
	}
	if rep.Passed {
		_, _ = fmt.Fprintln(w, "RESULT: PASS")
	} else {
		_, _ = fmt.Fprintf(w, "RESULT: FAIL (%d violations)\n", rep.ViolationCount)
	}
}
