// Command loadtest runs simulated agents against a Subspace server over real HTTP (REST + MCP + SSE)
// and reports latency, throughput and errors. See SPEC §11.
//
//	go run ./cmd/loadtest -agents 300 -duration 60s                      # in-process server, temp DB
//	go run ./cmd/loadtest -url https://s.example.com -owner-token "$TOK"  # external server
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"syscall"
	"time"

	"github.com/SigmaUno/subspace/internal/loadtest"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run parses flags, runs the load test and returns the exit code: 0 ok, 1 error rate breached, 2 usage/setup error.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("loadtest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfg loadtest.Config
	var asJSON bool
	var profileDir, outPath string
	fs.IntVar(&cfg.Agents, "agents", 300, "number of simulated workers (plus 1 leader and the reviewers)")
	fs.DurationVar(&cfg.Duration, "duration", 60*time.Second, "length of the workload phase")
	fs.DurationVar(&cfg.Ramp, "ramp", 0, "start the workers gradually over this period of the workload phase (0 = all at once)")
	fs.StringVar(&cfg.URL, "url", "", "base URL of an external server (empty = start one in-process on a temp DB)")
	fs.StringVar(&cfg.OwnerToken, "owner-token", os.Getenv("SUBSPACE_OWNER_TOKEN"), "owner token of the external server (default $SUBSPACE_OWNER_TOKEN)")
	fs.IntVar(&cfg.SSESubs, "sse-subs", 100, "number of SSE subscribers on /api/v1/events (max 1000)")
	fs.Float64Var(&cfg.MaxErrorRate, "max-error-rate", 0, "maximum tolerated fraction of failed requests (0..1)")
	fs.IntVar(&cfg.TasksPerAgent, "tasks-per-agent", 2, "tasks created per worker up front (more are created on demand)")
	fs.IntVar(&cfg.Heartbeats, "heartbeats", 3, "heartbeats per task between claim and submit")
	fs.IntVar(&cfg.Reviewers, "reviewers", 0, "number of reviewers (0 = one per 25 workers)")
	fs.DurationVar(&cfg.Think, "think", 50*time.Millisecond, "pause between an agent's calls")
	fs.DurationVar(&cfg.Drain, "drain", 30*time.Second, "grace period after -duration to finish reviews and done marks")
	fs.BoolVar(&asJSON, "json", false, "print the report as JSON")
	fs.StringVar(&outPath, "out", "", "also write the JSON report to this file")
	fs.StringVar(&profileDir, "profile", "", "write cpu.pprof and heap.pprof to this directory (covers the in-process server too)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "unexpected arguments: %v\n", fs.Args())
		return 2
	}
	if profileDir != "" {
		stopProfile, err := startProfile(profileDir)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "loadtest: profile: %v\n", err)
			return 2
		}
		defer func() {
			if err := stopProfile(); err != nil {
				_, _ = fmt.Fprintf(stderr, "loadtest: profile: %v\n", err)
			}
		}()
	}
	rep, err := loadtest.Run(ctx, cfg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "loadtest: %v\n", err)
		return 2
	}
	if outPath != "" {
		if err := writeJSON(outPath, rep); err != nil {
			_, _ = fmt.Fprintf(stderr, "loadtest: write report: %v\n", err)
			return 2
		}
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		rep.WriteText(stdout)
	}
	if rep.Breached() {
		_, _ = fmt.Fprintf(stderr, "loadtest: error rate %.4f exceeds -max-error-rate %.4f\n", rep.ErrorRate, rep.MaxErrorRate)
		return 1
	}
	return 0
}

// startProfile starts a CPU profile in dir; the returned func stops it and writes a heap profile.
func startProfile(dir string) (func() error, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	cpu, err := os.Create(filepath.Join(dir, "cpu.pprof")) //nolint:gosec
	if err != nil {
		return nil, err
	}
	if err := pprof.StartCPUProfile(cpu); err != nil {
		_ = cpu.Close()
		return nil, err
	}
	return func() error {
		pprof.StopCPUProfile()
		if err := cpu.Close(); err != nil {
			return err
		}
		runtime.GC()
		heap, err := os.Create(filepath.Join(dir, "heap.pprof")) //nolint:gosec
		if err != nil {
			return err
		}
		werr := pprof.WriteHeapProfile(heap)
		if cerr := heap.Close(); werr == nil {
			werr = cerr
		}
		return werr
	}, nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}
